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
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// errAlreadyRunning is returned by claimInstance when another iaxphone-gui
// holds the socket; it has been asked to show its window.
var errAlreadyRunning = errors.New("iaxphone-gui is already running")

// instanceSocket is where the running instance listens: one per user, in
// the runtime directory when the session has one.
func instanceSocket() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "iaxphone-gui.sock")
}

// claimInstance makes this process the single running instance. It
// listens on path and calls show, from its own goroutine, each time
// another instance starts; that instance gets errAlreadyRunning instead.
// A socket left by a crashed instance is replaced. Close the returned
// listener to let a later instance take over.
func claimInstance(path string, show func()) (io.Closer, error) {
	if c, err := net.Dial("unix", path); err == nil {
		_, _ = io.WriteString(c, "show\n")
		_ = c.Close()
		return nil, errAlreadyRunning
	}
	_ = os.Remove(path) // stale, nobody answers on it
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return // closed
			}
			go func() {
				defer func() { _ = c.Close() }()
				line, _ := bufio.NewReader(c).ReadString('\n')
				if strings.TrimSpace(line) == "show" {
					show()
				}
			}()
		}
	}()
	return l, nil
}
