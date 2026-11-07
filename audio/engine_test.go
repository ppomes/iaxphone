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
	"errors"
	"math"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeDevice lets the test drive the callbacks by hand.
type fakeDevice struct {
	rate              int
	capture, playback func([]int16)
}

func (d *fakeDevice) Start(rate int, c, p func([]int16)) error {
	d.rate, d.capture, d.playback = rate, c, p
	return nil
}
func (d *fakeDevice) Close() error { return nil }

// rawCodec is a 16 kHz codec that sends the samples as they are, little
// endian, and conceals a lost frame with a constant.
type rawCodec struct{ closed bool }

func (c *rawCodec) Encode(dst []byte, pcm []int16) []byte {
	for _, s := range pcm {
		dst = append(dst, byte(s), byte(uint16(s)>>8)) //nolint:gosec // sample bits
	}
	return dst
}

func (c *rawCodec) Decode(dst []int16, data []byte) []int16 {
	for i := 0; i+1 < len(data); i += 2 {
		dst = append(dst, int16(uint16(data[i])|uint16(data[i+1])<<8)) //nolint:gosec // sample bits
	}
	return dst
}

func (c *rawCodec) Samples(data []byte) int { return len(data) / 2 }
func (c *rawCodec) Rate() int               { return 16000 }
func (c *rawCodec) Close() error            { c.closed = true; return nil }

func (c *rawCodec) Conceal(dst []int16) []int16 {
	for range FrameSamples(c.Rate()) {
		dst = append(dst, 777)
	}
	return dst
}

func TestEngineCaptureToSend(t *testing.T) {
	dev := &fakeDevice{}
	var mu sync.Mutex
	var sent [][]byte
	eng := NewEngine(dev, Ulaw{}, 40, func(p []byte) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, p)
		return nil
	})
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, FrameSamples(SampleRate))
	for i := range pcm {
		pcm[i] = int16(8000 * math.Sin(2*math.Pi*1004*float64(i)/SampleRate))
	}
	dev.capture(pcm)
	dev.capture(make([]int16, FrameSamples(SampleRate)))
	if err := eng.Stop(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 || len(sent[0]) != FrameSamples(SampleRate) {
		t.Fatalf("sent %d frames", len(sent))
	}
	st := eng.Stats()
	if st.Captured != 2 || st.Sent != 2 {
		t.Fatalf("stats %+v", st)
	}
	in, _ := eng.Levels()
	if in > -11 || in < -13 { // 8000/32768 ≈ -12.2 dBFS
		t.Fatalf("input peak %.1f dBFS", in)
	}
}

func TestEngineReceiveToPlayback(t *testing.T) {
	dev := &fakeDevice{}
	eng := NewEngine(dev, Alaw{}, 40, func([]byte) error { return errors.New("down") })
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop() //nolint:errcheck
	tone := make([]int16, FrameSamples(SampleRate))
	for i := range tone {
		tone[i] = 12000
	}
	payload := Alaw{}.Encode(nil, tone)
	eng.Receive(0, payload)
	eng.Receive(20, payload)

	out := make([]int16, FrameSamples(SampleRate))
	dev.playback(out) // prebuffered (40 ms queued, depth 40) → plays
	if out[0] < 11000 || out[0] > 13000 {
		t.Fatalf("playback sample %d, want ≈12000", out[0])
	}
	dev.playback(out)
	dev.playback(out) // underrun → silence
	if out[0] != 0 {
		t.Fatalf("expected silence on underrun, got %d", out[0])
	}
	st := eng.Stats()
	if st.Played != 2 || st.Silent != 1 || st.Jitter.Underruns != 1 {
		t.Fatalf("stats %+v", st)
	}
	dev.capture(make([]int16, FrameSamples(SampleRate)))
	deadline := time.Now().Add(time.Second)
	for eng.Stats().SendErrors == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if eng.Stats().SendErrors != 1 {
		t.Fatalf("send error not counted: %+v", eng.Stats())
	}
}

func TestNullDeviceTicks(t *testing.T) {
	dev := &NullDevice{}
	eng := NewEngine(dev, Ulaw{}, 40, func([]byte) error { return nil })
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(110 * time.Millisecond)
	if err := eng.Stop(); err != nil {
		t.Fatal(err)
	}
	if st := eng.Stats(); st.Captured < 3 || st.Silent < 3 {
		t.Fatalf("stats %+v", st)
	}
	if err := eng.Stop(); err != nil {
		t.Fatal("second Stop must be a no-op")
	}
}

func TestEngineTone(t *testing.T) {
	dev := &fakeDevice{}
	eng := NewEngine(dev, Ulaw{}, 40, func([]byte) error { return nil })
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop() //nolint:errcheck
	eng.SetTone(NewTone([]float64{1000}, time.Second, 0, -6))
	out := make([]int16, FrameSamples(SampleRate))
	dev.playback(out)
	var peak int16
	for _, v := range out {
		if v > peak {
			peak = v
		}
	}
	if peak < 15000 || peak > 16500 {
		t.Errorf("tone peak %d, want ≈16384", peak)
	}
	if _, o := eng.Levels(); o < -6.5 || o > -5.5 {
		t.Errorf("out level %.1f dBFS", o)
	}
	eng.SetTone(nil)
	dev.playback(out)
	if out[0] != 0 {
		t.Errorf("sample after tone %d", out[0])
	}
	if st := eng.Stats(); st.Tone != 1 || st.Silent != 1 || st.Played != 0 {
		t.Errorf("stats %+v", st)
	}
}

// A peer sending 40 ms frames: the playback callbacks, 20 ms each, must get
// the whole signal in order, with nothing counted lost or dropped.
func TestEngineLongFrames(t *testing.T) {
	dev := &fakeDevice{}
	eng := NewEngine(dev, Ulaw{}, 40, func([]byte) error { return nil })
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop() //nolint:errcheck
	// A ramp over 4 frames of 40 ms, u-law exact at these values.
	const frames = 4
	var want []int16
	for ts := uint32(0); ts < frames*40; ts += 40 {
		pcm := make([]int16, 2*FrameSamples(SampleRate))
		for i := range pcm {
			pcm[i] = int16(ts) * 50 //nolint:gosec // small
		}
		want = append(want, Ulaw{}.Decode(nil, Ulaw{}.Encode(nil, pcm))...)
		eng.Receive(ts, Ulaw{}.Encode(nil, pcm))
	}
	var got []int16
	out := make([]int16, FrameSamples(SampleRate))
	for range 2 * frames {
		dev.playback(out)
		got = append(got, out...)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("playback differs from the signal (first %v..., want %v...)", got[:4], want[:4])
	}
	st := eng.Stats()
	if st.Played != 2*frames || st.Silent != 0 || st.Jitter.Lost != 0 || st.Jitter.Dropped != 0 || st.Jitter.Underruns != 0 {
		t.Errorf("stats %+v", st)
	}
}

// deadDevice cannot start.
type deadDevice struct{}

func (deadDevice) Start(int, func([]int16), func([]int16)) error { return errors.New("no sound card") }
func (deadDevice) Close() error                                  { return nil }

// A device that fails to start: Start reports it, the codec is closed all
// the same and a Stop afterwards is a harmless no-op.
func TestEngineStartFails(t *testing.T) {
	codec := &rawCodec{}
	eng := NewEngine(deadDevice{}, codec, 40, func([]byte) error { return nil })
	if err := eng.Start(); err == nil || err.Error() != "no sound card" {
		t.Fatalf("Start: %v", err)
	}
	if !codec.closed {
		t.Error("codec left open after a failed Start")
	}
	if err := eng.Stop(); err != nil {
		t.Errorf("Stop after a failed Start: %v", err)
	}
}

// A codec at another rate: the device is started at it, frames are sized
// for it, the jitter buffer times them right, a lost frame is concealed by
// the codec, a tone is generated at that rate and Stop closes the codec.
func TestEngineCodecRate(t *testing.T) {
	dev := &fakeDevice{}
	codec := &rawCodec{}
	eng := NewEngine(dev, codec, 40, func([]byte) error { return nil })
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	if dev.rate != 16000 {
		t.Fatalf("device started at %d Hz", dev.rate)
	}
	frame := FrameSamples(16000)
	if frame != 320 {
		t.Fatalf("FrameSamples(16000) = %d", frame)
	}
	pcm := make([]int16, frame)
	for i := range pcm {
		pcm[i] = 1000
	}
	payload := codec.Encode(nil, pcm)
	eng.Receive(0, payload)
	eng.Receive(20, payload) // 40 ms queued: playout starts
	eng.Receive(60, payload) // 40 is missing
	out := make([]int16, frame)
	for i, want := range []int16{1000, 1000, 777, 1000} {
		dev.playback(out)
		if out[0] != want || out[frame-1] != want {
			t.Errorf("frame %d plays %d..%d, want %d", i, out[0], out[frame-1], want)
		}
	}
	if st := eng.Stats(); st.Played != 4 || st.Jitter.Lost != 1 {
		t.Errorf("stats %+v", st)
	}

	// A 1 kHz tone at 16 kHz: 20 cycles per frame, so the sample a quarter
	// cycle in (4 samples) is at the peak and the half cycle one is 0.
	tone := NewTone([]float64{1000}, time.Second, 0, 0)
	eng.SetTone(tone)
	dev.playback(out)
	if out[4] < 32000 || out[8] < -400 || out[8] > 400 {
		t.Errorf("tone at 16 kHz: out[4] = %d, out[8] = %d", out[4], out[8])
	}
	// The caller's tone is untouched (still at SampleRate, never played) and
	// may be set again while the copy plays.
	eng.SetTone(tone)
	dev.playback(out)
	if tone.rate != SampleRate || tone.pos != 0 {
		t.Errorf("caller's tone changed: rate %d, pos %d", tone.rate, tone.pos)
	}
	if err := eng.Stop(); err != nil {
		t.Fatal(err)
	}
	if !codec.closed {
		t.Error("Stop did not close the codec")
	}
}
