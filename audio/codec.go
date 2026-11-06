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

import "time"

// Codec converts between 16-bit linear PCM and an on-the-wire payload.
// Implementations are stateless (G.711) or hold encoder/decoder state
// (Opus). Encode and Decode may run concurrently with each other, on the
// capture and playback sides, but neither is safe for concurrent use with
// itself.
type Codec interface {
	// Encode appends the encoding of pcm to dst. pcm is one frame at Rate.
	Encode(dst []byte, pcm []int16) []byte
	// Decode appends the decoded samples of data to dst.
	Decode(dst []int16, data []byte) []int16
	// Samples is how many samples data decodes to, without decoding it:
	// the peer chooses its frame size and need not use FrameSamples.
	Samples(data []byte) int
	// Rate is the sample rate of the PCM the codec consumes and produces.
	Rate() int
}

// Concealer is a Codec that can make up a lost frame from what came before
// it (Opus). The engine plays silence for the lost frames of other codecs.
type Concealer interface {
	// Conceal appends one frame period of concealment audio to dst.
	Conceal(dst []int16) []int16
}

// SampleRate is the narrowband rate: that of G.711, and of the local tones
// played outside a call.
const SampleRate = 8000

// FramePeriod is the duration of one voice frame.
const FramePeriod = 20 * time.Millisecond

// FrameSamples is the number of samples in one frame at rate.
func FrameSamples(rate int) int { return rate * int(FramePeriod/time.Millisecond) / 1000 }
