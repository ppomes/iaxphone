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

// G.711 µ-law and A-law, after the reference implementation by Sun
// Microsystems (g711.c) that Asterisk and everyone else derive from.

const (
	ulawBias = 0x84
	ulawClip = 32635
)

// LinearToUlaw encodes one 16-bit sample.
func LinearToUlaw(s int16) uint8 {
	sample := int(s)
	sign := 0
	if sample < 0 {
		sample = -sample
		sign = 0x80
	}
	if sample > ulawClip {
		sample = ulawClip
	}
	sample += ulawBias
	exponent := 7
	for mask := 0x4000; sample&mask == 0 && exponent > 0; exponent-- {
		mask >>= 1
	}
	mantissa := (sample >> (exponent + 3)) & 0x0F
	return uint8(^(sign | exponent<<4 | mantissa))
}

// UlawToLinear decodes one µ-law byte.
func UlawToLinear(u uint8) int16 {
	u = ^u
	t := (int(u&0x0F) << 3) + ulawBias
	t <<= (u & 0x70) >> 4
	if u&0x80 != 0 {
		return int16(ulawBias - t)
	}
	return int16(t - ulawBias)
}

var alawSegEnd = [8]int{0x1F, 0x3F, 0x7F, 0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF}

// LinearToAlaw encodes one 16-bit sample.
func LinearToAlaw(s int16) uint8 {
	pcm := int(s) >> 3
	mask := 0xD5
	if pcm < 0 {
		mask = 0x55
		pcm = -pcm - 1
	}
	seg := 8
	for i, end := range alawSegEnd {
		if pcm <= end {
			seg = i
			break
		}
	}
	if seg >= 8 {
		return uint8(0x7F ^ mask)
	}
	aval := seg << 4
	if seg < 2 {
		aval |= (pcm >> 1) & 0x0F
	} else {
		aval |= (pcm >> seg) & 0x0F
	}
	return uint8(aval ^ mask)
}

// AlawToLinear decodes one A-law byte.
func AlawToLinear(a uint8) int16 {
	a ^= 0x55
	t := int(a&0x0F) << 4
	seg := (a & 0x70) >> 4
	switch seg {
	case 0:
		t += 8
	case 1:
		t += 0x108
	default:
		t += 0x108
		t <<= seg - 1
	}
	if a&0x80 != 0 {
		return int16(t)
	}
	return int16(-t)
}

// Ulaw is the G.711 µ-law codec.
type Ulaw struct{}

// Alaw is the G.711 A-law codec.
type Alaw struct{}

// Encode converts PCM to µ-law, appending to dst.
func (Ulaw) Encode(dst []byte, pcm []int16) []byte {
	for _, s := range pcm {
		dst = append(dst, LinearToUlaw(s))
	}
	return dst
}

// Decode converts µ-law to PCM, appending to dst.
func (Ulaw) Decode(dst []int16, data []byte) []int16 {
	for _, b := range data {
		dst = append(dst, UlawToLinear(b))
	}
	return dst
}

// Encode converts PCM to A-law, appending to dst.
func (Alaw) Encode(dst []byte, pcm []int16) []byte {
	for _, s := range pcm {
		dst = append(dst, LinearToAlaw(s))
	}
	return dst
}

// Decode converts A-law to PCM, appending to dst.
func (Alaw) Decode(dst []int16, data []byte) []int16 {
	for _, b := range data {
		dst = append(dst, AlawToLinear(b))
	}
	return dst
}
