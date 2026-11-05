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

// ExpandTimestamp rebuilds a full 32-bit timestamp from the 16 low bits
// carried by a mini frame, using the most recent full timestamp seen on the
// call as a reference. Mini frames may arrive slightly out of order, so the
// candidate closest to the reference is chosen, which handles the 65.536 s
// rollover in both directions.
func ExpandTimestamp(ref uint32, low uint16) uint32 {
	ts := ref&^0xFFFF | uint32(low)
	switch {
	case ts > ref && ts-ref > 0x8000:
		return ts - 0x10000
	case ts < ref && ref-ts > 0x8000:
		return ts + 0x10000
	}
	return ts
}
