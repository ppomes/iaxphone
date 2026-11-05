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

package audio

import (
	"fmt"
	"testing"
)

func frame(ts uint32) []byte { return []byte(fmt.Sprintf("f%d", ts)) }

func TestJitterPrebufferAndSteadyState(t *testing.T) {
	jb := NewJitterBuffer(60, 20)
	if d, lost := jb.Get(); d != nil || lost {
		t.Fatal("Get before any Put should play silence")
	}
	jb.Put(100, frame(100))
	jb.Put(120, frame(120))
	if d, _ := jb.Get(); d != nil {
		t.Fatalf("40 ms queued, depth 60: should still prebuffer, got %q", d)
	}
	jb.Put(140, frame(140))
	for _, ts := range []uint32{100, 120, 140} {
		d, lost := jb.Get()
		if lost || string(d) != string(frame(ts)) {
			t.Fatalf("Get = %q, %v; want %q", d, lost, frame(ts))
		}
	}
	st := jb.Stats()
	if st.Received != 3 || st.Played != 3 || st.Lost != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestJitterReorderAndLoss(t *testing.T) {
	jb := NewJitterBuffer(40, 20)
	jb.Put(20, frame(20))
	jb.Put(0, frame(0)) // out of order during prebuffer
	if d, _ := jb.Get(); string(d) != "f0" {
		t.Fatalf("got %q, want f0", d)
	}
	jb.Put(60, frame(60)) // 40 is missing
	jb.Put(80, frame(80))
	if d, _ := jb.Get(); string(d) != "f20" {
		t.Fatalf("got %q, want f20", d)
	}
	d, lost := jb.Get()
	if d != nil || !lost {
		t.Fatalf("expected a lost frame at ts 40, got %q, %v", d, lost)
	}
	jb.Put(40, frame(40)) // arrives too late
	if d, _ := jb.Get(); string(d) != "f60" {
		t.Fatalf("got %q, want f60", d)
	}
	st := jb.Stats()
	if st.Lost != 1 || st.Late != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestJitterUnderrunAndResume(t *testing.T) {
	jb := NewJitterBuffer(40, 20)
	jb.Put(0, frame(0))
	jb.Put(20, frame(20))
	jb.Get()
	jb.Get()
	if d, lost := jb.Get(); d != nil || lost {
		t.Fatal("empty buffer should be a silent underrun, not a loss")
	}
	if jb.Stats().Underruns != 1 {
		t.Fatalf("stats %+v", jb.Stats())
	}
	// The stream resumes much later: playout must jump there, not count
	// every missing 20 ms as a loss.
	jb.Put(5000, frame(5000))
	if d, _ := jb.Get(); d != nil {
		t.Fatal("should prebuffer again after an underrun")
	}
	jb.Put(5020, frame(5020))
	if d, _ := jb.Get(); string(d) != "f5000" {
		t.Fatalf("got %q, want f5000", d)
	}
	if st := jb.Stats(); st.Lost != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestJitterOverflowTrims(t *testing.T) {
	jb := NewJitterBuffer(40, 20) // max 160 ms
	for ts := uint32(0); ts < 400; ts += 20 {
		jb.Put(ts, frame(ts))
	}
	st := jb.Stats()
	if st.Dropped == 0 || jb.Queued() > 160 {
		t.Fatalf("expected trimming, stats %+v queued %d", st, jb.Queued())
	}
	d, _ := jb.Get()
	if string(d) == "f0" {
		t.Fatal("playout should have skipped ahead")
	}
}

func TestJitterDuplicate(t *testing.T) {
	jb := NewJitterBuffer(20, 20)
	jb.Put(0, frame(0))
	jb.Put(0, frame(0))
	if st := jb.Stats(); st.Late != 1 {
		t.Fatalf("stats %+v", st)
	}
}
