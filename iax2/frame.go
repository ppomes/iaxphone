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

package iax2

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
)

// Header sizes.
const (
	FullHeaderLen = 12
	MiniHeaderLen = 4

	// MaxCallNumber is the largest 15-bit call number.
	MaxCallNumber = 0x7FFF

	flagFullFrame   = 0x8000 // F bit on the source call number word
	flagRetransmit  = 0x8000 // R bit on the destination call number word
	flagSubclassLog = 0x80   // C bit: subclass is a power of two exponent
)

// Framing errors.
var (
	ErrShortFrame  = errors.New("iax2: frame too short")
	ErrMetaFrame   = errors.New("iax2: meta frames are not supported")
	ErrBadCallNo   = errors.New("iax2: call number out of range")
	ErrBadSubclass = errors.New("iax2: subclass cannot be encoded")
)

// Frame is either a *FullFrame or a *MiniFrame.
type Frame interface {
	// AppendTo appends the wire encoding of the frame to dst.
	AppendTo(dst []byte) ([]byte, error)
	// SourceCall returns the sender's call number.
	SourceCall() uint16
}

// FullFrame is a reliable frame (RFC 5456 §8.1.1).
type FullFrame struct {
	SrcCall    uint16 // 15 bits, sender's call number
	DstCall    uint16 // 15 bits, receiver's call number (0 when unknown)
	Retransmit bool   // R bit
	Timestamp  uint32 // milliseconds since the start of the call
	OSeqno     uint8  // outbound sequence number
	ISeqno     uint8  // inbound sequence number (next expected)
	Type       FrameType
	// Subclass is the decoded subclass. For VOICE and VIDEO frames it is a
	// single Format bit; for IAX and CONTROL frames it is the small integer
	// (convert with IAXSubclass(f.Subclass) or ControlSubclass(f.Subclass)).
	Subclass uint64
	Data     []byte
}

// MiniFrame is an unreliable voice frame (RFC 5456 §8.1.2).
type MiniFrame struct {
	SrcCall   uint16 // 15 bits
	Timestamp uint16 // low 16 bits of the full timestamp
	Data      []byte
}

// SourceCall implements Frame.
func (f *FullFrame) SourceCall() uint16 { return f.SrcCall }

// SourceCall implements Frame.
func (f *MiniFrame) SourceCall() uint16 { return f.SrcCall }

// IEs parses the frame payload as a list of information elements. Only IAX
// frames carry IEs.
func (f *FullFrame) IEs() (IEs, error) { return ParseIEs(f.Data) }

// AppendTo implements Frame.
func (f *FullFrame) AppendTo(dst []byte) ([]byte, error) {
	if f.SrcCall > MaxCallNumber || f.DstCall > MaxCallNumber {
		return nil, ErrBadCallNo
	}
	csub, err := compressSubclass(f.Subclass)
	if err != nil {
		return nil, err
	}
	dst16 := f.DstCall
	if f.Retransmit {
		dst16 |= flagRetransmit
	}
	dst = binary.BigEndian.AppendUint16(dst, f.SrcCall|flagFullFrame)
	dst = binary.BigEndian.AppendUint16(dst, dst16)
	dst = binary.BigEndian.AppendUint32(dst, f.Timestamp)
	dst = append(dst, f.OSeqno, f.ISeqno, uint8(f.Type), csub)
	return append(dst, f.Data...), nil
}

// AppendTo implements Frame.
func (f *MiniFrame) AppendTo(dst []byte) ([]byte, error) {
	if f.SrcCall > MaxCallNumber {
		return nil, ErrBadCallNo
	}
	dst = binary.BigEndian.AppendUint16(dst, f.SrcCall)
	dst = binary.BigEndian.AppendUint16(dst, f.Timestamp)
	return append(dst, f.Data...), nil
}

// Encode returns the wire encoding of a frame.
func Encode(f Frame) ([]byte, error) { return f.AppendTo(nil) }

// Parse decodes one UDP datagram. The returned frame's Data aliases pkt.
func Parse(pkt []byte) (Frame, error) {
	if len(pkt) < MiniHeaderLen {
		return nil, ErrShortFrame
	}
	w0 := binary.BigEndian.Uint16(pkt[0:2])
	if w0&flagFullFrame == 0 {
		src := w0 & MaxCallNumber
		if src == 0 {
			return nil, ErrMetaFrame
		}
		return &MiniFrame{
			SrcCall:   src,
			Timestamp: binary.BigEndian.Uint16(pkt[2:4]),
			Data:      pkt[MiniHeaderLen:],
		}, nil
	}
	if len(pkt) < FullHeaderLen {
		return nil, ErrShortFrame
	}
	w1 := binary.BigEndian.Uint16(pkt[2:4])
	return &FullFrame{
		SrcCall:    w0 & MaxCallNumber,
		DstCall:    w1 & MaxCallNumber,
		Retransmit: w1&flagRetransmit != 0,
		Timestamp:  binary.BigEndian.Uint32(pkt[4:8]),
		OSeqno:     pkt[8],
		ISeqno:     pkt[9],
		Type:       FrameType(pkt[10]),
		Subclass:   uncompressSubclass(pkt[11]),
		Data:       pkt[FullHeaderLen:],
	}, nil
}

// compressSubclass mirrors Asterisk's compress_subclass(): values below 128
// are sent as is, larger power-of-two values are sent as their exponent with
// the C bit set.
func compressSubclass(sc uint64) (uint8, error) {
	if sc < flagSubclassLog || sc == 0xFF {
		return uint8(sc), nil
	}
	if bits.OnesCount64(sc) != 1 {
		return 0, ErrBadSubclass
	}
	return uint8(bits.TrailingZeros64(sc)) | flagSubclassLog, nil
}

// uncompressSubclass is the inverse. A C-bit exponent of 64 or more cannot be
// a format; 0xFF in particular is what Asterisk sends for CONTROL subclass
// -1 (stop sounds), so such bytes are returned as they are.
func uncompressSubclass(b uint8) uint64 {
	if b&flagSubclassLog != 0 {
		if n := b &^ flagSubclassLog; n < 64 {
			return 1 << n
		}
	}
	return uint64(b)
}

// String gives a one-line summary useful in logs and packet dumps.
func (f *FullFrame) String() string {
	var sub string
	switch f.Type {
	case FrameIAX:
		sub = IAXSubclass(f.Subclass).String()
	case FrameControl:
		sub = ControlSubclass(f.Subclass).String()
	case FrameVoice, FrameVideo:
		sub = Format(f.Subclass).String()
	default:
		sub = fmt.Sprint(f.Subclass)
	}
	r := ""
	if f.Retransmit {
		r = " R"
	}
	return fmt.Sprintf("FULL %s/%s src=%d dst=%d ts=%d oseq=%d iseq=%d len=%d%s",
		f.Type, sub, f.SrcCall, f.DstCall, f.Timestamp, f.OSeqno, f.ISeqno, len(f.Data), r)
}

// String gives a one-line summary useful in logs and packet dumps.
func (f *MiniFrame) String() string {
	return fmt.Sprintf("MINI src=%d ts=%d len=%d", f.SrcCall, f.Timestamp, len(f.Data))
}
