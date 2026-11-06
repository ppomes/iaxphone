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

import "sync/atomic"

// Player plays a Tone on a Device outside a call: the keypad's feedback
// while the user dials. Capture is ignored.
type Player struct {
	dev    Device
	tone   atomic.Pointer[Tone]
	frames atomic.Int64
	peak   atomic.Int32
}

// NewPlayer wraps a device; Start opens it.
func NewPlayer(dev Device) *Player { return &Player{dev: dev} }

// Start starts the device at SampleRate; it plays silence until SetTone.
func (p *Player) Start() error {
	return p.dev.Start(SampleRate, func([]int16) {}, p.onPlayback)
}

// SetTone plays tone from now on, nil for silence.
func (p *Player) SetTone(tone *Tone) { p.tone.Store(tone) }

// Frames returns how many frames of tone were played.
func (p *Player) Frames() int64 { return p.frames.Load() }

// Level returns the peak playback level in dBFS since the previous call,
// −∞ for silence, and resets it.
func (p *Player) Level() float64 { return dbfs(p.peak.Swap(0)) }

// Stop closes the device.
func (p *Player) Stop() error { return p.dev.Close() }

func (p *Player) onPlayback(pcm []int16) {
	tone := p.tone.Load()
	if tone == nil {
		clear(pcm)
		return
	}
	tone.Fill(pcm)
	p.frames.Add(1)
	updatePeak(&p.peak, pcm)
}
