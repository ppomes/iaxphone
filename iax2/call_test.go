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

import (
	"testing"
	"time"
)

func TestVoiceTimestampPredictsFromSamples(t *testing.T) {
	c := newCallState(nil, 1)
	c.format = FormatULAW
	first := c.voiceTimestamp(160)
	c.voiceStarted, c.lastVoiceTS = true, first
	// Frames captured in a burst still get 20 ms spaced timestamps.
	for i := 1; i <= 3; i++ {
		ts := c.voiceTimestamp(160)
		if want := first + uint32(20*i); ts != want { //nolint:gosec // small
			t.Fatalf("frame %d: ts %d, want %d", i, ts, want)
		}
		c.lastVoiceTS = ts
	}
	// A 10 ms A-law frame advances by 10.
	c.format = FormatALAW
	if ts := c.voiceTimestamp(80); ts != c.lastVoiceTS+10 {
		t.Fatalf("10 ms frame: ts %d, want %d", ts, c.lastVoiceTS+10)
	}
}

func TestVoiceTimestampResyncsAfterGap(t *testing.T) {
	c := newCallState(nil, 1)
	c.format = FormatULAW
	c.start = time.Now().Add(-2 * time.Second)
	c.voiceStarted, c.lastVoiceTS = true, 100 // the device paused for ~1.9 s
	ts := c.voiceTimestamp(160)
	if ts < 1900 || ts > 2200 {
		t.Fatalf("ts %d, want the wall clock (~2000)", ts)
	}
	// The wall clock lagging behind never moves timestamps backwards.
	c.lastVoiceTS = 5000
	if ts := c.voiceTimestamp(160); ts != 5020 {
		t.Fatalf("ts %d, want 5020", ts)
	}
}
