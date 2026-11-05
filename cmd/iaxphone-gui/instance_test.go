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

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClaimInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "i.sock")
	shown := make(chan struct{}, 1)
	l, err := claimInstance(path, func() { shown <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}

	// A second instance is turned away and the first is asked to show.
	if _, err := claimInstance(path, func() { t.Error("second instance shown") }); !errors.Is(err, errAlreadyRunning) {
		t.Fatalf("second claim: %v, want errAlreadyRunning", err)
	}
	select {
	case <-shown:
	case <-time.After(2 * time.Second):
		t.Fatal("first instance not asked to show")
	}

	// Once the first is gone a new one takes over.
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l, err = claimInstance(path, func() {})
	if err != nil {
		t.Fatalf("claim after close: %v", err)
	}
	_ = l.Close()

	// A dead socket file from a crash does not block.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l, err = claimInstance(path, func() {})
	if err != nil {
		t.Fatalf("claim over stale file: %v", err)
	}
	_ = l.Close()
}
