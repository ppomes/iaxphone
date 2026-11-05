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

// Package history keeps the list of past calls, one JSON object per line
// in a file under the user's data directory, newest last.
package history

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Max is how many entries are kept; older ones are dropped when the file
// is rewritten.
const Max = 500

// Entry is one past call.
type Entry struct {
	At       time.Time `json:"at"`             // when the call started
	Outgoing bool      `json:"outgoing"`       // we dialled
	Answered bool      `json:"answered"`       // the call was up at some point
	Number   string    `json:"number"`         // dialled, or the caller's
	Name     string    `json:"name,omitempty"` // the caller's name
	Seconds  int       `json:"seconds"`        // talk time
	Cause    string    `json:"cause,omitempty"`
}

// Missed is an incoming call that was not answered.
func (e Entry) Missed() bool { return !e.Outgoing && !e.Answered }

// Log is the history file, loaded in memory.
type Log struct {
	path string

	mu      sync.Mutex
	entries []Entry // oldest first
}

// DefaultPath is $XDG_DATA_HOME/iaxphone/history.jsonl, under
// ~/.local/share without the variable.
func DefaultPath() (string, error) {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "iaxphone", "history.jsonl"), nil
}

// Open loads the history at path; a missing file is an empty history and
// a damaged line is skipped.
func Open(path string) (*Log, error) {
	l := &Log{path: path}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && !e.At.IsZero() {
			l.entries = append(l.entries, e)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	if len(l.entries) > Max {
		l.entries = l.entries[len(l.entries)-Max:]
	}
	return l, nil
}

// Entries returns the calls, newest first.
func (l *Log) Entries() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Entry, len(l.entries))
	for i, e := range l.entries {
		out[len(l.entries)-1-i] = e
	}
	return out
}

// Add records a call: appended to the file, or the file rewritten with
// the last Max entries when it has grown past twice that.
func (l *Log) Add(e Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
	if len(l.entries) > 2*Max {
		l.entries = l.entries[len(l.entries)-Max:]
		return l.rewrite()
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	line, _ := json.Marshal(e)
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Clear forgets every call.
func (l *Log) Clear() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = nil
	return l.rewrite()
}

// rewrite writes the file from the entries, through a temporary file.
func (l *Log) rewrite() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range l.entries {
		line, _ := json.Marshal(e)
		if _, err := w.Write(append(line, '\n')); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}
