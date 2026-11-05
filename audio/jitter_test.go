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
	"slices"
	"testing"
)

func frame(ts uint32) []byte { return []byte(fmt.Sprintf("f%d", ts)) }

func TestJitterPrebufferAndSteadyState(t *testing.T) {
	jb := NewJitterBuffer(60, 20)
	if d, lost := jb.Get(); d != nil || lost {
		t.Fatal("Get before any Put should play silence")
	}
	jb.Put(100, 20, frame(100))
	jb.Put(120, 20, frame(120))
	if d, _ := jb.Get(); d != nil {
		t.Fatalf("40 ms queued, depth 60: should still prebuffer, got %q", d)
	}
	jb.Put(140, 20, frame(140))
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
	jb.Put(20, 20, frame(20))
	jb.Put(0, 20, frame(0)) // out of order during prebuffer
	if d, _ := jb.Get(); string(d) != "f0" {
		t.Fatalf("got %q, want f0", d)
	}
	jb.Put(60, 20, frame(60)) // 40 is missing
	jb.Put(80, 20, frame(80))
	if d, _ := jb.Get(); string(d) != "f20" {
		t.Fatalf("got %q, want f20", d)
	}
	d, lost := jb.Get()
	if d != nil || !lost {
		t.Fatalf("expected a lost frame at ts 40, got %q, %v", d, lost)
	}
	jb.Put(40, 20, frame(40)) // arrives too late
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
	jb.Put(0, 20, frame(0))
	jb.Put(20, 20, frame(20))
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
	jb.Put(5000, 20, frame(5000))
	if d, _ := jb.Get(); d != nil {
		t.Fatal("should prebuffer again after an underrun")
	}
	jb.Put(5020, 20, frame(5020))
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
		jb.Put(ts, 20, frame(ts))
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
	jb.Put(0, 20, frame(0))
	jb.Put(0, 20, frame(0))
	if st := jb.Stats(); st.Late != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// The peer's frames may be longer than ours: the playout point moves by
// each frame's own duration, nothing is counted lost.
func TestJitterBufferFrameSizes(t *testing.T) {
	jb := NewJitterBuffer(40, 20) // holds 160 ms at most
	for ts := uint32(0); ts < 160; ts += 40 {
		jb.Put(ts, 40, frame(ts))
	}
	for ts := uint32(0); ts < 160; ts += 40 {
		data, lost := jb.Get()
		if data == nil || lost {
			t.Fatalf("40 ms frame %d: data %v lost %v", ts, data != nil, lost)
		}
	}
	if st := jb.Stats(); st.Played != 4 || st.Lost != 0 || st.Underruns != 0 || st.Dropped != 0 {
		t.Errorf("40 ms stats %+v", st)
	}

	// 30 ms frames with one missing: a frame period of silence, then the
	// stream picks up again within the half-period tolerance.
	jb = NewJitterBuffer(30, 20)
	jb.Put(1000, 30, frame(1000))
	jb.Put(1030, 30, frame(1030))
	jb.Put(1090, 30, frame(1090)) // 1060 is missing
	jb.Put(1120, 30, frame(1120))
	var got []bool
	for range 5 {
		data, _ := jb.Get()
		got = append(got, data != nil)
	}
	if want := []bool{true, true, false, true, true}; !slices.Equal(got, want) {
		t.Errorf("30 ms playout %v, want %v", got, want)
	}
	if st := jb.Stats(); st.Played != 4 || st.Lost != 1 {
		t.Errorf("30 ms stats %+v", st)
	}
}
