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
	"testing"
	"time"
)

func TestDTMFTone(t *testing.T) {
	if DTMFTone('x') != nil || DTMFTone(' ') != nil {
		t.Error("tone for a non-key")
	}
	for _, key := range []byte("123A456B789C*0#D") {
		tone := DTMFTone(key)
		if tone == nil {
			t.Fatalf("no tone for %c", key)
		}
		// Continuous: still sounding after the second it was built with.
		pcm := make([]int16, SampleRate+160)
		tone.Fill(pcm)
		var peak int16
		for _, v := range pcm[SampleRate:] {
			if v > peak {
				peak = v
			}
		}
		if db := 20 * math.Log10(float64(peak)/32767); db < -16 || db > -11.5 {
			t.Errorf("%c: peak %.1f dBFS after a second", key, db)
		}
	}
	// 5 sits at 770 Hz + 1336 Hz: both frequencies are there, 440 is not.
	pcm := make([]int16, SampleRate)
	DTMFTone('5').Fill(pcm)
	for _, f := range []float64{770, 1336, 440} {
		var re, im float64
		for i, v := range pcm {
			re += float64(v) * math.Cos(2*math.Pi*f*float64(i)/SampleRate)
			im += float64(v) * math.Sin(2*math.Pi*f*float64(i)/SampleRate)
		}
		mag := math.Hypot(re, im) / float64(len(pcm))
		if present := mag > 1000; present != (f != 440) {
			t.Errorf("%v Hz magnitude %.0f", f, mag)
		}
	}
}

func TestPlayer(t *testing.T) {
	p := NewPlayer(&NullDevice{})
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if p.Frames() != 0 || !math.IsInf(p.Level(), -1) {
		t.Errorf("silent: frames %d", p.Frames())
	}
	p.SetTone(DTMFTone('1'))
	deadline := time.Now().Add(2 * time.Second)
	for p.Frames() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if p.Frames() < 3 || p.Level() > -11.5 || p.Level() == math.Inf(-1) && p.Frames() < 3 {
		t.Errorf("tone: frames %d", p.Frames())
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestToneCadenceAndLevel(t *testing.T) {
	tone := NewTone([]float64{440, 480}, 100*time.Millisecond, 50*time.Millisecond, -12)
	pcm := make([]int16, SampleRate*150/1000)
	tone.Fill(pcm)
	peak := func(s []int16) float64 {
		var p int16
		for _, v := range s {
			if v > p {
				p = v
			}
		}
		return 20 * math.Log10(float64(p)/32767)
	}
	// On: both sines in phase at the start, so the peak reaches the level.
	if db := peak(pcm[:SampleRate/10]); db < -12.5 || db > -11.5 {
		t.Errorf("on peak %.1f dBFS", db)
	}
	for i, v := range pcm[SampleRate/10:] {
		if v != 0 {
			t.Fatalf("off sample %d = %d", i, v)
		}
	}
	// The cadence repeats across Fill calls.
	tone.Fill(pcm[:160])
	if peak(pcm[:160]) < -20 {
		t.Error("tone did not restart")
	}
}
