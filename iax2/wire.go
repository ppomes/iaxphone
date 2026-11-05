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
	"net/netip"
	"time"
)

// DecodeDateTime unpacks the DATETIME IE, which chan_iax2 fills with the
// server's local time: seconds/2 in 5 bits, minutes 6, hours 5, day 5,
// month 4 and years since 2000 in the top 7 bits. There is no zone
// information, so the result is in time.Local.
func DecodeDateTime(v uint32) time.Time {
	sec := int(v&0x1F) << 1
	min := int(v>>5) & 0x3F
	hour := int(v>>11) & 0x1F
	day := int(v>>16) & 0x1F
	month := time.Month(int(v>>21) & 0x0F)
	year := 2000 + int(v>>25)
	return time.Date(year, month, day, hour, min, sec, 0, time.Local)
}

// EncodeDateTime is the inverse of DecodeDateTime.
func EncodeDateTime(t time.Time) uint32 {
	return uint32(t.Second()>>1)&0x1F |
		uint32(t.Minute()&0x3F)<<5 |
		uint32(t.Hour()&0x1F)<<11 |
		uint32(t.Day()&0x1F)<<16 |
		uint32(int(t.Month())&0x0F)<<21 |
		uint32((t.Year()-2000)&0x7F)<<25
}

// parseSockaddr decodes the APPARENT_ADDR IE, which is a raw struct
// sockaddr_in (16 bytes) or sockaddr_in6 (28 bytes) as laid out by the
// server's libc: the family is in host byte order, port and address in
// network order.
func parseSockaddr(b []byte) (netip.AddrPort, bool) {
	switch len(b) {
	case 16:
		port := binary.BigEndian.Uint16(b[2:4])
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[4:8])), port), true
	case 28:
		port := binary.BigEndian.Uint16(b[2:4])
		return netip.AddrPortFrom(netip.AddrFrom16([16]byte(b[8:24])), port), true
	}
	return netip.AddrPort{}, false
}

// sockaddrIE builds an APPARENT_ADDR IE in Linux x86 layout (little-endian
// family), used by the test server.
func sockaddrIE(ap netip.AddrPort) IE {
	if ap.Addr().Is4() {
		b := make([]byte, 16)
		binary.LittleEndian.PutUint16(b[0:2], 2) // AF_INET
		binary.BigEndian.PutUint16(b[2:4], ap.Port())
		a := ap.Addr().As4()
		copy(b[4:8], a[:])
		return IE{ID: IEApparentAddr, Data: b}
	}
	b := make([]byte, 28)
	binary.LittleEndian.PutUint16(b[0:2], 10) // AF_INET6
	binary.BigEndian.PutUint16(b[2:4], ap.Port())
	a := ap.Addr().As16()
	copy(b[8:24], a[:])
	return IE{ID: IEApparentAddr, Data: b}
}
