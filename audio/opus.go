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
//go:build cgo

package audio

/*
#cgo pkg-config: opus
#include <opus/opus.h>

// opus_encoder_ctl is variadic, which cgo cannot call: one helper per
// setting used.
static int iaxphone_opus_setup(OpusEncoder *enc, opus_int32 bitrate) {
	int err = opus_encoder_ctl(enc, OPUS_SET_SIGNAL(OPUS_SIGNAL_VOICE));
	if (err != OPUS_OK) {
		return err;
	}
	return opus_encoder_ctl(enc, OPUS_SET_BITRATE(bitrate));
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// OpusAvailable reports whether this binary can encode and decode Opus:
// it needs cgo and libopus.
const OpusAvailable = true

// OpusRate is the sample rate Opus runs at here: 48 kHz, full band, which
// is what Asterisk's codec_opus speaks. The codec itself decides how much of
// the band the bitrate affords.
const OpusRate = 48000

// OpusBitrate is the encoder's target, in bits per second: plenty for
// wideband speech, a quarter of G.711.
const OpusBitrate = 32000

// maxOpusPacket is the largest packet Encode produces; libopus recommends
// 4000 bytes for any bitrate.
const maxOpusPacket = 4000

// Opus is the Opus codec (RFC 6716) through libopus, mono at OpusRate,
// one encoder and one decoder. Close frees them.
type Opus struct {
	enc *C.OpusEncoder
	dec *C.OpusDecoder
	buf [maxOpusPacket]byte
}

// NewOpus creates an encoder tuned for voice and a decoder.
func NewOpus() (*Opus, error) {
	var ret C.int
	enc := C.opus_encoder_create(OpusRate, 1, C.OPUS_APPLICATION_VOIP, &ret)
	if ret != C.OPUS_OK {
		return nil, opusError("create encoder", ret)
	}
	if ret = C.iaxphone_opus_setup(enc, OpusBitrate); ret != C.OPUS_OK {
		C.opus_encoder_destroy(enc)
		return nil, opusError("set up encoder", ret)
	}
	dec := C.opus_decoder_create(OpusRate, 1, &ret)
	if ret != C.OPUS_OK {
		C.opus_encoder_destroy(enc)
		return nil, opusError("create decoder", ret)
	}
	return &Opus{enc: enc, dec: dec}, nil
}

func opusError(what string, ret C.int) error {
	return fmt.Errorf("audio: opus: %s: %s", what, C.GoString(C.opus_strerror(ret)))
}

// Encode implements Codec. pcm must be a frame Opus accepts (2.5 to 60 ms:
// 120 to 2880 samples at OpusRate); anything else encodes to nothing.
func (o *Opus) Encode(dst []byte, pcm []int16) []byte {
	if o.enc == nil || len(pcm) == 0 {
		return dst
	}
	n := C.opus_encode(o.enc, (*C.opus_int16)(unsafe.Pointer(&pcm[0])), C.int(len(pcm)),
		(*C.uchar)(unsafe.Pointer(&o.buf[0])), maxOpusPacket)
	if n <= 0 {
		return dst
	}
	return append(dst, o.buf[:n]...)
}

// Decode implements Codec. A packet libopus rejects decodes to nothing.
func (o *Opus) Decode(dst []int16, data []byte) []int16 {
	if o.dec == nil || len(data) == 0 {
		return dst
	}
	n := C.opus_decoder_get_nb_samples(o.dec, (*C.uchar)(unsafe.Pointer(&data[0])), C.opus_int32(len(data)))
	if n <= 0 {
		return dst
	}
	return o.decode(dst, data, int(n))
}

// decode appends up to n samples decoded from data (nil: concealment).
func (o *Opus) decode(dst []int16, data []byte, n int) []int16 {
	start := len(dst)
	dst = append(dst, make([]int16, n)...)
	var in *C.uchar
	if len(data) > 0 {
		in = (*C.uchar)(unsafe.Pointer(&data[0]))
	}
	got := C.opus_decode(o.dec, in, C.opus_int32(len(data)),
		(*C.opus_int16)(unsafe.Pointer(&dst[start])), C.int(n), 0)
	if got < 0 {
		return dst[:start]
	}
	return dst[:start+int(got)]
}

// Samples implements Codec, from the packet's table of contents.
func (o *Opus) Samples(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := C.opus_packet_get_nb_samples((*C.uchar)(unsafe.Pointer(&data[0])), C.opus_int32(len(data)), OpusRate)
	if n < 0 {
		return 0
	}
	return int(n)
}

// Conceal implements Concealer: the decoder extrapolates one frame period
// from the audio before the loss.
func (o *Opus) Conceal(dst []int16) []int16 {
	if o.dec == nil {
		return append(dst, make([]int16, FrameSamples(OpusRate))...)
	}
	return o.decode(dst, nil, FrameSamples(OpusRate))
}

// Rate implements Codec: OpusRate.
func (o *Opus) Rate() int { return OpusRate }

// Close frees the encoder and the decoder; the codec is unusable after.
func (o *Opus) Close() error {
	if o.enc != nil {
		C.opus_encoder_destroy(o.enc)
		o.enc = nil
	}
	if o.dec != nil {
		C.opus_decoder_destroy(o.dec)
		o.dec = nil
	}
	return nil
}
