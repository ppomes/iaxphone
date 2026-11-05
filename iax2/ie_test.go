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

func TestIEsRoundTrip(t *testing.T) {
	in := IEs{
		Uint16IE(IEVersion, ProtocolVersion),
		StringIE(IECalledNumber, "600"),
		StringIE(IEUsername, "phone"),
		Uint32IE(IECapability, uint32(FormatULAW|FormatALAW)),
		Format64IE(IECapability2, FormatULAW|FormatOpus),
		Uint8IE(IECauseCode, CauseNormalClearing),
		EmptyIE(IEAutoAnswer),
	}
	b, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := []byte{11, 2, 0, 2, 1, 3, '6', '0', '0', 6, 5, 'p', 'h', 'o', 'n', 'e'}
	if !bytes.HasPrefix(b, wantPrefix) {
		t.Fatalf("encode = % x", b)
	}
	out, err := ParseIEs(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("parsed %d IEs, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i].ID != in[i].ID || !bytes.Equal(out[i].Data, in[i].Data) {
			t.Errorf("IE %d: got %v, want %v", i, out[i], in[i])
		}
	}

	if v, err := out.Uint16(IEVersion); err != nil || v != 2 {
		t.Errorf("Uint16(VERSION) = %d, %v", v, err)
	}
	if s := out.String(IECalledNumber); s != "600" {
		t.Errorf("String(CALLED_NUMBER) = %q", s)
	}
	if s := out.String(IEChallenge); s != "" {
		t.Errorf("String(absent) = %q", s)
	}
	if v, err := out.Uint8(IECauseCode); err != nil || v != CauseNormalClearing {
		t.Errorf("Uint8(CAUSECODE) = %d, %v", v, err)
	}
	if !out.Has(IEAutoAnswer) {
		t.Error("AUTOANSWER missing")
	}
	// 64-bit variant wins over the 32-bit one.
	if f, err := out.Format(IECapability, IECapability2); err != nil || f != FormatULAW|FormatOpus {
		t.Errorf("Format() = %s, %v", f, err)
	}
	// Fallback on the 32-bit IE.
	if f, err := out.Format(IECapability, IEFormat2); err != nil || f != FormatULAW|FormatALAW {
		t.Errorf("Format() fallback = %s, %v", f, err)
	}
}

func TestIEsErrors(t *testing.T) {
	if _, err := ParseIEs([]byte{1}); !errors.Is(err, ErrShortIE) {
		t.Errorf("header truncated: %v", err)
	}
	if _, err := ParseIEs([]byte{1, 3, 'a'}); !errors.Is(err, ErrShortIE) {
		t.Errorf("data truncated: %v", err)
	}
	long := IEs{{ID: IEUsername, Data: make([]byte, 256)}}
	if _, err := long.Encode(); !errors.Is(err, ErrLongIE) {
		t.Errorf("long IE: %v", err)
	}
	ies := IEs{Uint8IE(IEVersion, 2)}
	if _, err := ies.Uint16(IEVersion); !errors.Is(err, ErrIESize) {
		t.Errorf("wrong size: %v", err)
	}
	if _, err := ies.Uint32(IECapability); err == nil {
		t.Error("missing IE: expected error")
	}
}
