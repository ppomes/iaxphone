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
	"sync"
	"time"
)

// Device is a full-duplex sound device at SampleRate, mono, 16-bit.
//
// It is kept behind an interface so the protocol and codec code can be
// tested without cgo or a sound card: Malgo is the real one (miniaudio via
// github.com/gen2brain/malgo), NullDevice the test one.
type Device interface {
	// Start begins capture and playback. capture is called with each
	// FrameSamples frame recorded; playback must fill the frame to play.
	// Both run on the device's own goroutine and must not block.
	Start(capture func(pcm []int16), playback func(pcm []int16)) error
	// Close stops the device; the callbacks are not called after it returns.
	Close() error
}

// DeviceInfo describes a sound device for listing.
type DeviceInfo struct {
	Name    string
	Capture bool // false: playback
	Default bool
}

// NullDevice is a Device that captures silence and discards playback, on a
// 20 ms ticker.
type NullDevice struct {
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// Start implements Device.
func (d *NullDevice) Start(capture func([]int16), playback func([]int16)) error {
	d.stop = make(chan struct{})
	d.done = make(chan struct{})
	go func() {
		defer close(d.done)
		in := make([]int16, FrameSamples)
		out := make([]int16, FrameSamples)
		tick := time.NewTicker(FramePeriod)
		defer tick.Stop()
		for {
			select {
			case <-d.stop:
				return
			case <-tick.C:
				clear(in)
				capture(in)
				playback(out)
			}
		}
	}()
	return nil
}

// Close implements Device.
func (d *NullDevice) Close() error {
	if d.stop == nil {
		return nil
	}
	d.once.Do(func() { close(d.stop) })
	<-d.done
	return nil
}
