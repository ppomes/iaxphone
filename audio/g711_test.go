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

import "testing"

func TestUlawCodesRoundTrip(t *testing.T) {
	for c := 0; c < 256; c++ {
		code := uint8(c)
		if code == 0x7F {
			continue // negative zero decodes to 0, which re-encodes as 0xFF
		}
		if got := LinearToUlaw(UlawToLinear(code)); got != code {
			t.Errorf("ulaw %#x -> %d -> %#x", code, UlawToLinear(code), got)
		}
	}
}

func TestAlawCodesRoundTrip(t *testing.T) {
	for c := 0; c < 256; c++ {
		code := uint8(c)
		if got := LinearToAlaw(AlawToLinear(code)); got != code {
			t.Errorf("alaw %#x -> %d -> %#x", code, AlawToLinear(code), got)
		}
	}
}

func TestG711KnownValues(t *testing.T) {
	if got := LinearToUlaw(0); got != 0xFF {
		t.Errorf("ulaw(0) = %#x", got)
	}
	if got := UlawToLinear(0x80); got != 32124 {
		t.Errorf("ulaw decode 0x80 = %d", got)
	}
	if got := UlawToLinear(0x00); got != -32124 {
		t.Errorf("ulaw decode 0x00 = %d", got)
	}
	if got := LinearToAlaw(0); got != 0xD5 {
		t.Errorf("alaw(0) = %#x", got)
	}
	if got := AlawToLinear(0x2A); got != -32256 {
		t.Errorf("alaw decode 0x2A = %d", got)
	}
	if got := AlawToLinear(0xAA); got != 32256 {
		t.Errorf("alaw decode 0xAA = %d", got)
	}
}

// Encoding then decoding must stay within the quantisation step of the
// segment, and be monotonic.
func TestG711Monotonic(t *testing.T) {
	var prevU, prevA int16 = -32768, -32768
	for s := -32768; s <= 32767; s += 37 {
		in := int16(s)
		u := UlawToLinear(LinearToUlaw(in))
		a := AlawToLinear(LinearToAlaw(in))
		if u < prevU {
			t.Fatalf("ulaw not monotonic at %d: %d < %d", s, u, prevU)
		}
		if a < prevA {
			t.Fatalf("alaw not monotonic at %d: %d < %d", s, a, prevA)
		}
		if d := abs(int(u) - s); d > 1024+abs(s)/16 {
			t.Errorf("ulaw error at %d: got %d", s, u)
		}
		if d := abs(int(a) - s); d > 1024+abs(s)/16 {
			t.Errorf("alaw error at %d: got %d", s, a)
		}
		prevU, prevA = u, a
	}
}

func TestCodecSlices(t *testing.T) {
	pcm := []int16{0, 1000, -1000, 32767, -32768}
	for _, c := range []struct {
		name string
		enc  func([]byte, []int16) []byte
		dec  func([]int16, []byte) []int16
	}{
		{"ulaw", Ulaw{}.Encode, Ulaw{}.Decode},
		{"alaw", Alaw{}.Encode, Alaw{}.Decode},
	} {
		enc := c.enc(nil, pcm)
		if len(enc) != len(pcm) {
			t.Fatalf("%s: encoded %d bytes", c.name, len(enc))
		}
		dec := c.dec(nil, enc)
		if len(dec) != len(pcm) {
			t.Fatalf("%s: decoded %d samples", c.name, len(dec))
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
