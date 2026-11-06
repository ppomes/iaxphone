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

//go:build !cgo

package audio

import "errors"

// ErrNoCgo is returned by OpenMalgo in a binary built without cgo.
var ErrNoCgo = errors.New("audio: miniaudio needs cgo")

// Malgo is unavailable without cgo.
type Malgo struct{}

// OpenMalgo returns ErrNoCgo.
func OpenMalgo(func(string)) (*Malgo, error) { return nil, ErrNoCgo }

// Devices returns ErrNoCgo.
func (*Malgo) Devices() ([]DeviceInfo, error) { return nil, ErrNoCgo }

// Select returns ErrNoCgo.
func (*Malgo) Select(string, string) error { return ErrNoCgo }

// Start returns ErrNoCgo.
func (*Malgo) Start(int, func([]int16), func([]int16)) error { return ErrNoCgo }

// Close does nothing.
func (*Malgo) Close() error { return nil }
