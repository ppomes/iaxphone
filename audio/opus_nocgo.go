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
//go:build !cgo

package audio

// OpusAvailable reports whether this binary can encode and decode Opus:
// not without cgo and libopus.
const OpusAvailable = false

// OpusRate is the sample rate Opus runs at, see opus.go.
const OpusRate = 48000

// OpusBitrate is the encoder's target bitrate, see opus.go.
const OpusBitrate = 32000

// Opus is unavailable without cgo.
type Opus struct{}

// NewOpus returns ErrNoCgo.
func NewOpus() (*Opus, error) { return nil, ErrNoCgo }

// Encode encodes nothing.
func (*Opus) Encode(dst []byte, _ []int16) []byte { return dst }

// Decode decodes nothing.
func (*Opus) Decode(dst []int16, _ []byte) []int16 { return dst }

// Samples returns 0.
func (*Opus) Samples([]byte) int { return 0 }

// Conceal appends a frame period of silence.
func (*Opus) Conceal(dst []int16) []int16 {
	return append(dst, make([]int16, FrameSamples(OpusRate))...)
}

// Rate returns OpusRate.
func (*Opus) Rate() int { return OpusRate }

// Close does nothing.
func (*Opus) Close() error { return nil }
