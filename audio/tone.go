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
	"math"
	"strings"
	"time"
)

// Tone generates a cadenced sum of sine waves: on for a while, silent for a
// while, again. It is what a phone plays locally while a call rings, since
// IAX2 carries no audio before the answer.
type Tone struct {
	freqs  []float64
	phases []float64
	amp    float64
	onFor  time.Duration
	offFor time.Duration
	rate   int
	on     int // samples
	period int // samples, on + off
	pos    int
}

// NewTone returns a tone of the given frequencies, played on for on and
// off for off (off == 0: continuous), at level dBFS peak, generated at
// SampleRate (an Engine plays a copy at its own rate).
func NewTone(freqs []float64, on, off time.Duration, dbfs float64) *Tone {
	t := &Tone{
		freqs:  freqs,
		phases: make([]float64, len(freqs)),
		amp:    math.Pow(10, dbfs/20) * 32767 / float64(len(freqs)),
		onFor:  on,
		offFor: off,
	}
	t.setRate(SampleRate)
	return t
}

// setRate generates the tone at rate from its start. Not for a tone that
// is being played.
func (t *Tone) setRate(rate int) {
	t.rate = rate
	t.on = int(t.onFor.Seconds() * float64(rate))
	t.period = int((t.onFor + t.offFor).Seconds() * float64(rate))
	t.pos = 0
}

// RingTone is the North American ring and ringback tone: 440 Hz + 480 Hz,
// 2 s on, 4 s off, at -12 dBFS.
func RingTone() *Tone {
	return NewTone([]float64{440, 480}, 2*time.Second, 4*time.Second, -12)
}

// DTMF keypad frequencies (ITU-T Q.23): a key sounds its row's low
// frequency and its column's high frequency together.
var (
	dtmfRows = []string{"123A", "456B", "789C", "*0#D"}
	dtmfLow  = []float64{697, 770, 852, 941}
	dtmfHigh = []float64{1209, 1336, 1477, 1633}
)

// DTMFTone is the continuous dual tone of a keypad key at -12 dBFS, what a
// phone sounds while the key is held; nil for a byte that is not a key.
func DTMFTone(key byte) *Tone {
	for row, keys := range dtmfRows {
		if col := strings.IndexByte(keys, key); col >= 0 {
			return NewTone([]float64{dtmfLow[row], dtmfHigh[col]}, time.Second, 0, -12)
		}
	}
	return nil
}

// Fill writes the next len(pcm) samples of the tone.
func (t *Tone) Fill(pcm []int16) {
	for i := range pcm {
		if t.pos < t.on {
			var v float64
			for j, f := range t.freqs {
				v += math.Sin(t.phases[j])
				t.phases[j] += 2 * math.Pi * f / float64(t.rate)
				if t.phases[j] > 2*math.Pi {
					t.phases[j] -= 2 * math.Pi
				}
			}
			pcm[i] = int16(v * t.amp)
		} else {
			pcm[i] = 0
		}
		t.pos++
		if t.pos >= t.period {
			t.pos = 0
		}
	}
}
