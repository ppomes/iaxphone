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
)

// A tone goes through the Opus encoder and decoder and comes out as the
// same tone: its frequency is there and little else, after the codec's
// start-up frames.
func TestOpusRoundTrip(t *testing.T) {
	if !OpusAvailable {
		t.Skip("built without cgo")
	}
	codec, err := NewOpus()
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close() //nolint:errcheck
	if codec.Rate() != OpusRate {
		t.Fatalf("rate %d", codec.Rate())
	}
	frame := FrameSamples(OpusRate)
	if frame != 960 {
		t.Fatalf("frame %d samples", frame)
	}
	const freq = 1004.0
	var got []int16
	pos := 0
	for range 25 { // 500 ms
		pcm := make([]int16, frame)
		for i := range pcm {
			pcm[i] = int16(8000 * math.Sin(2*math.Pi*freq*float64(pos)/OpusRate))
			pos++
		}
		packet := codec.Encode(nil, pcm)
		if len(packet) == 0 || len(packet) > 200 {
			t.Fatalf("packet of %d bytes", len(packet))
		}
		if n := codec.Samples(packet); n != frame {
			t.Fatalf("Samples %d, want %d", n, frame)
		}
		got = codec.Decode(got, packet)
	}
	if len(got) != 25*frame {
		t.Fatalf("decoded %d samples, want %d", len(got), 25*frame)
	}
	// Skip the first 200 ms (codec warm-up and look-ahead), then measure.
	tail := got[10*frame:]
	signal := magnitude(tail, freq)
	noise := magnitude(tail, 2*freq+300)
	if signal < 7000*0.7 {
		t.Errorf("tone comes out at %.0f, want about 8000", signal)
	}
	if noise > signal/30 {
		t.Errorf("off-frequency content %.0f against %.0f", noise, signal)
	}

	// Concealment yields a frame period; an unusable packet, nothing.
	if n := len(codec.Conceal(nil)); n != frame {
		t.Errorf("Conceal gave %d samples", n)
	}
	if n := len(codec.Decode(nil, []byte{0xff, 0xff, 0xff})); n != 0 {
		t.Errorf("garbage decoded to %d samples", n)
	}
	if n := len(codec.Encode(nil, make([]int16, 7))); n != 0 {
		t.Errorf("odd frame encoded to %d bytes", n)
	}
	if err := codec.Close(); err != nil {
		t.Fatal(err)
	}
	if n := len(codec.Encode(nil, make([]int16, frame))); n != 0 {
		t.Errorf("encoded %d bytes after Close", n)
	}
}

// magnitude is the amplitude of the f component of pcm, by correlation.
func magnitude(pcm []int16, f float64) float64 {
	var re, im float64
	for i, v := range pcm {
		re += float64(v) * math.Cos(2*math.Pi*f*float64(i)/OpusRate)
		im += float64(v) * math.Sin(2*math.Pi*f*float64(i)/OpusRate)
	}
	return 2 * math.Hypot(re, im) / float64(len(pcm))
}
