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

import "testing"

func TestExpandTimestamp(t *testing.T) {
	cases := []struct {
		ref  uint32
		low  uint16
		want uint32
	}{
		{0, 0, 0},
		{1000, 1020, 1020},
		{1000, 980, 980},                 // slightly late mini frame
		{0xFFF0, 0x0010, 0x10010},        // forward rollover
		{0x10010, 0xFFF0, 0xFFF0},        // late frame from before the rollover
		{0x2FFFF, 0x0000, 0x30000},       // exact boundary
		{0xABCD1234, 0x1235, 0xABCD1235}, // high bits preserved
	}
	for _, tc := range cases {
		if got := ExpandTimestamp(tc.ref, tc.low); got != tc.want {
			t.Errorf("ExpandTimestamp(%#x, %#x) = %#x, want %#x", tc.ref, tc.low, got, tc.want)
		}
	}
}
