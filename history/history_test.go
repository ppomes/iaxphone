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

package history

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "history.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries()) != 0 {
		t.Fatal("new log not empty")
	}
	t0 := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	calls := []Entry{
		{At: t0, Outgoing: true, Answered: true, Number: "600", Seconds: 42, Cause: "Normal Clearing"},
		{At: t0.Add(time.Minute), Number: "1001", Name: "Asterisk", Cause: "Call Rejected"},
		{At: t0.Add(2 * time.Minute), Outgoing: true, Number: "999", Cause: "No such context/extension"},
	}
	for _, e := range calls {
		if err := l.Add(e); err != nil {
			t.Fatal(err)
		}
	}

	// Reloaded, newest first, with a damaged line in the middle ignored.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	_, _ = f.WriteString("{not json\n")
	_ = f.Close()
	l, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := l.Entries()
	if len(got) != 3 || got[0].Number != "999" || got[2].Number != "600" || got[2].Seconds != 42 {
		t.Fatalf("reloaded %+v", got)
	}
	if !got[1].Missed() || got[0].Missed() || got[2].Missed() {
		t.Errorf("missed flags wrong: %+v", got)
	}

	// Growth is bounded: past 2*Max the file is rewritten with Max entries.
	for i := range 2 * Max {
		if err := l.Add(Entry{At: t0.Add(time.Duration(i) * time.Hour), Number: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	mem := l.Entries()
	if n := len(mem); n < Max || n > 2*Max || mem[0].Number != fmt.Sprint(2*Max-1) {
		t.Errorf("%d entries in memory, newest %q", n, mem[0].Number)
	}
	// Reopened, only the newest Max are kept.
	l, _ = Open(path)
	if disk := l.Entries(); len(disk) != Max || disk[0] != mem[0] {
		t.Errorf("%d entries reloaded, newest %q", len(disk), disk[0].Number)
	}

	if err := l.Clear(); err != nil {
		t.Fatal(err)
	}
	l, _ = Open(path)
	if len(l.Entries()) != 0 {
		t.Error("not cleared")
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	if p, _ := DefaultPath(); p != "/data/iaxphone/history.jsonl" {
		t.Errorf("path %q", p)
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/x")
	if p, _ := DefaultPath(); p != "/home/x/.local/share/iaxphone/history.jsonl" {
		t.Errorf("path %q", p)
	}
}
