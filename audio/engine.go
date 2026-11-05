// Copyright (C) 2026 Pierre POMES <pierre.pomes@gmail.com>
//
// Permission is hereby granted, free of charge, to any person obtaining a
// copy of this software and associated documentation files (the "Software"),
// to deal in the Software without restriction, including without limitation
// the rights to use, copy, modify, merge, publish, distribute, sublicense,
// and/or sell copies of the Software, and to permit persons to whom the
// Software is furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
// FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS
// IN THE SOFTWARE.

package audio

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Engine connects a Device to the network side of a call: captured frames
// are encoded and handed to send on a dedicated goroutine, received frames
// go through the jitter buffer and are decoded in the playback callback.
// It knows nothing about IAX2; the caller plugs Receive into the call's
// media channel and send into Call.SendVoice.
type Engine struct {
	dev   Device
	codec Codec
	jb    *JitterBuffer
	send  func(payload []byte) error

	out    chan []byte
	closed atomic.Bool
	muted  atomic.Bool
	tone   atomic.Pointer[Tone]
	wg     sync.WaitGroup

	decBuf []int16

	captured, sent, sendErrors, sendDropped atomic.Int64
	playedFrames, silentFrames, toneFrames  atomic.Int64
	peakIn, peakOut                         atomic.Int32
}

// Stats is a snapshot of the engine counters.
type Stats struct {
	Captured, Sent, SendErrors, SendDropped int64
	Played, Silent                          int64 // playback frames with audio / with silence
	Tone                                    int64 // playback frames taken by a local tone
	Jitter                                  JitterStats
}

// sendQueue is how many captured frames may wait for the paced sender; a
// longer burst is dropped (SendDropped).
const sendQueue = 16

// NewEngine builds an engine; depthMs is the jitter buffer depth.
func NewEngine(dev Device, codec Codec, depthMs int, send func(payload []byte) error) *Engine {
	return &Engine{
		dev:    dev,
		codec:  codec,
		jb:     NewJitterBuffer(depthMs, 1000*FrameSamples/SampleRate),
		send:   send,
		out:    make(chan []byte, sendQueue),
		decBuf: make([]int16, 0, FrameSamples),
	}
}

// Start starts the device and the sender goroutine.
func (e *Engine) Start() error {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		// Pace the output to one frame per period: sound servers deliver
		// capture in bursts (several periods at once after start-up or a
		// stall) and the network side wants evenly spaced packets.
		var next time.Time
		for payload := range e.out {
			if now := time.Now(); now.Before(next) {
				time.Sleep(next.Sub(now))
				next = next.Add(FramePeriod)
			} else {
				next = now.Add(FramePeriod)
			}
			switch err := e.send(payload); {
			case err == nil:
				e.sent.Add(1)
			case errors.Is(err, ErrSkipped):
			default:
				e.sendErrors.Add(1)
			}
		}
	}()
	if err := e.dev.Start(e.onCapture, e.onPlayback); err != nil {
		close(e.out)
		e.wg.Wait()
		return err
	}
	return nil
}

// Receive queues a frame from the network for playback.
func (e *Engine) Receive(ts uint32, payload []byte) {
	e.jb.Put(ts, payload)
}

// Stop stops the device and the sender; it is safe to call more than once.
func (e *Engine) Stop() error {
	if e.closed.Swap(true) {
		return nil
	}
	err := e.dev.Close()
	close(e.out)
	e.wg.Wait()
	return err
}

// ErrSkipped may be returned by the send callback for a frame it had no use
// for (the call is not up yet): it counts neither as sent nor as an error.
var ErrSkipped = errors.New("audio: frame skipped")

// SetTone plays tone instead of the peer's audio until SetTone(nil). The
// peer's frames keep being received and are dropped by the jitter buffer as
// late once the tone stops; a ring tone before the answer loses nothing.
func (e *Engine) SetTone(tone *Tone) { e.tone.Store(tone) }

// SetMuted replaces the captured audio with silence (the stream keeps
// flowing so the peer's jitter buffer stays fed).
func (e *Engine) SetMuted(muted bool) { e.muted.Store(muted) }

// Muted reports whether capture is muted.
func (e *Engine) Muted() bool { return e.muted.Load() }

func (e *Engine) onCapture(pcm []int16) {
	e.captured.Add(1)
	updatePeak(&e.peakIn, pcm)
	if e.muted.Load() {
		clear(pcm)
	}
	payload := e.codec.Encode(make([]byte, 0, len(pcm)), pcm)
	select {
	case e.out <- payload:
	default:
		e.sendDropped.Add(1)
	}
}

func (e *Engine) onPlayback(pcm []int16) {
	if tone := e.tone.Load(); tone != nil {
		tone.Fill(pcm)
		e.toneFrames.Add(1)
		updatePeak(&e.peakOut, pcm)
		return
	}
	data, _ := e.jb.Get()
	if data == nil {
		e.silentFrames.Add(1)
		clear(pcm)
		return
	}
	e.decBuf = e.codec.Decode(e.decBuf[:0], data)
	n := copy(pcm, e.decBuf)
	clear(pcm[n:])
	e.playedFrames.Add(1)
	updatePeak(&e.peakOut, pcm)
}

func updatePeak(p *atomic.Int32, pcm []int16) {
	var peak int32
	for _, s := range pcm {
		v := int32(s)
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	for {
		cur := p.Load()
		if peak <= cur || p.CompareAndSwap(cur, peak) {
			return
		}
	}
}

// Levels returns the peak capture and playback levels in dBFS since the
// previous call (−∞ for digital silence) and resets them.
func (e *Engine) Levels() (in, out float64) {
	return dbfs(e.peakIn.Swap(0)), dbfs(e.peakOut.Swap(0))
}

func dbfs(peak int32) float64 {
	if peak == 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(float64(peak)/32768)
}

// Stats returns a snapshot of the counters.
func (e *Engine) Stats() Stats {
	return Stats{
		Captured:    e.captured.Load(),
		Sent:        e.sent.Load(),
		SendErrors:  e.sendErrors.Load(),
		SendDropped: e.sendDropped.Load(),
		Played:      e.playedFrames.Load(),
		Silent:      e.silentFrames.Load(),
		Tone:        e.toneFrames.Load(),
		Jitter:      e.jb.Stats(),
	}
}
