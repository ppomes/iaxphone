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

// Package iax2 implements the Inter-Asterisk eXchange protocol version 2
// (RFC 5456).
//
// IAX2 carries signalling and media over a single UDP flow. Two frame kinds
// matter for a client:
//
//   - Full frames (12-byte header) are reliable: they carry sequence numbers
//     and must be acknowledged. They are used for signalling (IAX, CONTROL)
//     and for the first voice frame of a call, which fixes the codec.
//   - Mini frames (4-byte header) are unreliable and carry voice with only the
//     low 16 bits of the timestamp. See ExpandTimestamp.
//
// Meta frames (trunking, video) are not supported.
//
// The package is split in layers: frame.go and ie.go are pure encoding with
// no I/O, call.go holds the per-call state machine, sequence numbers and
// retransmissions, and client.go owns the UDP socket and the call table.
package iax2
