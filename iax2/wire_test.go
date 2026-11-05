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
	"net/netip"
	"testing"
	"time"
)

func TestDateTimeRoundTrip(t *testing.T) {
	want := time.Date(2026, time.November, 4, 13, 7, 42, 0, time.Local)
	got := DecodeDateTime(EncodeDateTime(want))
	if !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
	// Known packing: 2026-11-04 13:07:42 -> year 26<<25 | 11<<21 | 4<<16 | 13<<11 | 7<<5 | 21
	if v := EncodeDateTime(want); v != 26<<25|11<<21|4<<16|13<<11|7<<5|21 {
		t.Errorf("packed = %#x", v)
	}
}

func TestSockaddr(t *testing.T) {
	for _, ap := range []netip.AddrPort{
		netip.MustParseAddrPort("192.0.2.10:51014"),
		netip.MustParseAddrPort("[2001:db8::1]:4569"),
	} {
		got, ok := parseSockaddr(sockaddrIE(ap).Data)
		if !ok || got != ap {
			t.Errorf("round trip %s -> %s (%v)", ap, got, ok)
		}
	}
	if _, ok := parseSockaddr([]byte{1, 2, 3}); ok {
		t.Error("garbage accepted")
	}
}
