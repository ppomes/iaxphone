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
	"bytes"
	"errors"
	"testing"
)

func TestFullFrameRoundTrip(t *testing.T) {
	in := &FullFrame{
		SrcCall: 0x1234, DstCall: 0x0042, Retransmit: true, Timestamp: 0xDEADBEEF,
		OSeqno: 7, ISeqno: 9, Type: FrameIAX, Subclass: uint64(IAXNew),
		Data: []byte{1, 2, 3},
	}
	b, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x92, 0x34, 0x80, 0x42, 0xDE, 0xAD, 0xBE, 0xEF, 7, 9, 6, 1, 1, 2, 3}
	if !bytes.Equal(b, want) {
		t.Fatalf("encode = % x, want % x", b, want)
	}
	f, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := f.(*FullFrame)
	if !ok {
		t.Fatalf("parsed %T, want *FullFrame", f)
	}
	if out.SrcCall != in.SrcCall || out.DstCall != in.DstCall || !out.Retransmit ||
		out.Timestamp != in.Timestamp || out.OSeqno != in.OSeqno || out.ISeqno != in.ISeqno ||
		out.Type != in.Type || out.Subclass != in.Subclass || !bytes.Equal(out.Data, in.Data) {
		t.Fatalf("round trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
}

func TestMiniFrameRoundTrip(t *testing.T) {
	in := &MiniFrame{SrcCall: 5, Timestamp: 0xBEEF, Data: []byte{0xFF, 0x7F}}
	b, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x00, 0x05, 0xBE, 0xEF, 0xFF, 0x7F}
	if !bytes.Equal(b, want) {
		t.Fatalf("encode = % x, want % x", b, want)
	}
	f, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := f.(*MiniFrame)
	if !ok {
		t.Fatalf("parsed %T, want *MiniFrame", f)
	}
	if out.SrcCall != 5 || out.Timestamp != 0xBEEF || !bytes.Equal(out.Data, in.Data) {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestSubclassCompression(t *testing.T) {
	cases := []struct {
		sc   uint64
		wire uint8
	}{
		{0, 0},
		{uint64(IAXCallToken), 40},
		{uint64(FormatULAW), 4},           // small formats are sent verbatim
		{uint64(FormatSLIN16), 0x80 | 15}, // >= 128: exponent with C bit
		{uint64(FormatOpus), 0x80 | 34},
		{127, 127},
		{128, 0x80 | 7},
		{0xFF, 0xFF}, // CONTROL "stop sounds" as sent by Asterisk
	}
	for _, tc := range cases {
		got, err := compressSubclass(tc.sc)
		if err != nil {
			t.Fatalf("compress(%d): %v", tc.sc, err)
		}
		if got != tc.wire {
			t.Errorf("compress(%d) = %#x, want %#x", tc.sc, got, tc.wire)
		}
		if back := uncompressSubclass(got); back != tc.sc {
			t.Errorf("uncompress(%#x) = %d, want %d", got, back, tc.sc)
		}
	}
	if got := uncompressSubclass(0x80 | 100); got != 0x80|100 {
		t.Errorf("uncompress(exponent 100) = %d", got)
	}
	if _, err := compressSubclass(uint64(FormatULAW | FormatSLIN16)); !errors.Is(err, ErrBadSubclass) {
		t.Errorf("multi-bit subclass: err = %v, want ErrBadSubclass", err)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		pkt  []byte
		err  error
	}{
		{"empty", nil, ErrShortFrame},
		{"3 bytes", []byte{0x80, 0, 0}, ErrShortFrame},
		{"full header truncated", []byte{0x80, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0}, ErrShortFrame},
		{"meta", []byte{0, 0, 0, 0}, ErrMetaFrame},
	}
	for _, tc := range cases {
		if _, err := Parse(tc.pkt); !errors.Is(err, tc.err) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.err)
		}
	}
	if _, err := Encode(&FullFrame{SrcCall: 0x8000}); !errors.Is(err, ErrBadCallNo) {
		t.Errorf("call number overflow: err = %v", err)
	}
	if _, err := Encode(&MiniFrame{SrcCall: 0x8000}); !errors.Is(err, ErrBadCallNo) {
		t.Errorf("mini call number overflow: err = %v", err)
	}
}

func TestFullFrameString(t *testing.T) {
	f := &FullFrame{SrcCall: 1, Type: FrameIAX, Subclass: uint64(IAXRegReq)}
	if got, want := f.String(), "FULL IAX/REGREQ src=1 dst=0 ts=0 oseq=0 iseq=0 len=0"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	v := &FullFrame{SrcCall: 1, Type: FrameVoice, Subclass: uint64(FormatOpus)}
	if got := v.String(); got[:15] != "FULL VOICE/opus" {
		t.Errorf("String() = %q", got)
	}
}

func TestFormatString(t *testing.T) {
	if got := (FormatULAW | FormatALAW).String(); got != "ulaw|alaw" {
		t.Errorf("got %q", got)
	}
	if got := Format(0).String(); got != "none" {
		t.Errorf("got %q", got)
	}
}
