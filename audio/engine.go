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
	"io"
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
	rate  int // the codec's sample rate, the device runs at it too
	frame int // samples per frame period at rate
	jb    *JitterBuffer
	send  func(payload []byte) error

	out    chan []byte
	closed atomic.Bool
	muted  atomic.Bool
	tone   atomic.Pointer[Tone]
	wg     sync.WaitGroup

	pcmQ []int16 // decoded audio waiting for the playback callback

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

// NewEngine builds an engine running at the codec's sample rate; depthMs is
// the jitter buffer depth. Stop closes the codec if it is an io.Closer.
func NewEngine(dev Device, codec Codec, depthMs int, send func(payload []byte) error) *Engine {
	frame := FrameSamples(codec.Rate())
	return &Engine{
		dev:   dev,
		codec: codec,
		rate:  codec.Rate(),
		frame: frame,
		jb:    NewJitterBuffer(depthMs, int(FramePeriod/time.Millisecond)),
		send:  send,
		out:   make(chan []byte, sendQueue),
		pcmQ:  make([]int16, 0, 4*frame),
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
	if err := e.dev.Start(e.rate, e.onCapture, e.onPlayback); err != nil {
		e.closed.Store(true) // nothing left for Stop to do
		close(e.out)
		e.wg.Wait()
		e.closeCodec()
		return err
	}
	return nil
}

// closeCodec frees the codec's state, if it has any to free.
func (e *Engine) closeCodec() {
	if c, ok := e.codec.(io.Closer); ok {
		_ = c.Close()
	}
}

// Receive queues a frame from the network for playback.
func (e *Engine) Receive(ts uint32, payload []byte) {
	e.jb.Put(ts, e.codec.Samples(payload)*1000/e.rate, payload)
}

// Stop stops the device and the sender; it is safe to call more than once.
func (e *Engine) Stop() error {
	if e.closed.Swap(true) {
		return nil
	}
	err := e.dev.Close()
	close(e.out)
	e.wg.Wait()
	e.closeCodec()
	return err
}

// ErrSkipped may be returned by the send callback for a frame it had no use
// for (the call is not up yet): it counts neither as sent nor as an error.
var ErrSkipped = errors.New("audio: frame skipped")

// SetTone plays tone instead of the peer's audio until SetTone(nil). The
// peer's frames keep being received and are dropped by the jitter buffer as
// late once the tone stops; a ring tone before the answer loses nothing.
// The engine plays its own copy of the tone, from its start and at the
// engine's rate: the caller's tone is left alone and may be reused.
func (e *Engine) SetTone(tone *Tone) {
	if tone != nil {
		own := *tone
		own.phases = make([]float64, len(own.freqs))
		own.setRate(e.rate)
		tone = &own
	}
	e.tone.Store(tone)
}

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
	if len(payload) == 0 {
		return // the codec could not encode this frame
	}
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
	// Top the queue up to one callback's worth: a frame may be longer or
	// shorter than the callback asks for.
	for len(e.pcmQ) < len(pcm) {
		data, lost := e.jb.Get()
		if data == nil {
			if !lost {
				break // nothing to play yet
			}
			if c, ok := e.codec.(Concealer); ok {
				e.pcmQ = c.Conceal(e.pcmQ)
			} else {
				e.pcmQ = append(e.pcmQ, make([]int16, e.frame)...) // a frame period of silence
			}
			continue
		}
		e.pcmQ = e.codec.Decode(e.pcmQ, data)
	}
	if len(e.pcmQ) == 0 {
		e.silentFrames.Add(1)
		clear(pcm)
		return
	}
	n := copy(pcm, e.pcmQ)
	clear(pcm[n:])
	e.pcmQ = append(e.pcmQ[:0], e.pcmQ[n:]...)
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
