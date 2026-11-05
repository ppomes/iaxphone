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
	"sort"
	"sync"
)

// JitterBuffer reorders incoming voice frames by timestamp and hands them
// out at a steady pace to the playback goroutine.
//
// It is a fixed-depth buffer: playout starts once depth milliseconds of
// audio are queued, then Get is expected every frame period. A missing frame
// is reported as lost (the caller plays silence); an empty buffer is an
// underrun after which playout pauses until depth milliseconds are queued
// again; a buffer that grows past the maximum (sender clock running faster
// than ours, or a burst after a network stall) is trimmed from the front.
//
// An adaptive depth and packet loss concealment are left for later.
type JitterBuffer struct {
	mu sync.Mutex

	depth, max, frameMs uint32
	frames              []jbFrame // sorted by timestamp
	next                uint32    // timestamp of the next frame to play
	started, playing    bool
	stats               JitterStats
}

type jbFrame struct {
	ts   uint32
	data []byte
}

// JitterStats counts what happened to the frames.
type JitterStats struct {
	Received  int // frames given to Put
	Played    int // frames returned by Get
	Lost      int // frames expected by Get but never received
	Late      int // frames received after their playout time (or duplicates)
	Dropped   int // frames discarded because the buffer overflowed
	Underruns int // times Get found the buffer empty while playing
}

// DefaultJitterMs is the default playout delay.
const DefaultJitterMs = 60

// NewJitterBuffer returns a buffer that delays playout by depthMs and never
// holds more than depthMs+120 ms of audio. frameMs is the frame period.
func NewJitterBuffer(depthMs, frameMs int) *JitterBuffer {
	if frameMs <= 0 {
		frameMs = 20
	}
	if depthMs < frameMs {
		depthMs = frameMs
	}
	return &JitterBuffer{
		depth:   uint32(depthMs),       //nolint:gosec // checked positive
		max:     uint32(depthMs + 120), //nolint:gosec // checked positive
		frameMs: uint32(frameMs),       //nolint:gosec // checked positive
	}
}

// Put queues a frame received from the network. data is copied.
func (b *JitterBuffer) Put(ts uint32, data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stats.Received++
	if !b.started {
		b.started = true
		b.next = ts
	}
	if b.playing && int32(ts-b.next) < 0 {
		b.stats.Late++
		return
	}
	i := sort.Search(len(b.frames), func(i int) bool { return int32(b.frames[i].ts-ts) >= 0 })
	if i < len(b.frames) && b.frames[i].ts == ts {
		b.stats.Late++ // duplicate
		return
	}
	f := jbFrame{ts: ts, data: append([]byte(nil), data...)}
	b.frames = append(b.frames, jbFrame{})
	copy(b.frames[i+1:], b.frames[i:])
	b.frames[i] = f

	// Trim an overfull buffer, skipping playout ahead to what is left.
	trimmed := false
	for len(b.frames) > 1 && b.span() > b.max {
		b.frames = b.frames[1:]
		b.stats.Dropped++
		trimmed = true
	}
	if trimmed && b.playing {
		b.next = b.frames[0].ts
	}
}

// span is the amount of audio queued, in ms, from the playout point.
func (b *JitterBuffer) span() uint32 {
	if len(b.frames) == 0 {
		return 0
	}
	from := b.next
	if !b.playing {
		from = b.frames[0].ts
	}
	return b.frames[len(b.frames)-1].ts + b.frameMs - from
}

// Get returns the next frame to play, or nil when there is nothing to play
// (not started, prebuffering, underrun) or the frame was lost; lost is true
// only in that last case, when the caller may want to conceal the gap.
func (b *JitterBuffer) Get() (data []byte, lost bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.started {
		return nil, false
	}
	if !b.playing {
		if b.span() < b.depth {
			return nil, false
		}
		b.playing = true
		b.next = b.frames[0].ts
	}
	if len(b.frames) == 0 {
		b.stats.Underruns++
		b.playing = false
		return nil, false
	}
	f := b.frames[0]
	if int32(f.ts-b.next) <= int32(b.frameMs/2) { //nolint:gosec // small values
		b.frames = b.frames[1:]
		b.next = f.ts + b.frameMs
		b.stats.Played++
		return f.data, false
	}
	b.stats.Lost++
	b.next += b.frameMs
	return nil, true
}

// Stats returns a snapshot of the counters.
func (b *JitterBuffer) Stats() JitterStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

// Queued returns the amount of audio waiting, in ms.
func (b *JitterBuffer) Queued() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int(b.span())
}
