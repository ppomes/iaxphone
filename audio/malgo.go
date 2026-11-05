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

//go:build cgo

package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/gen2brain/malgo"
)

// Malgo is a Device on the default sound devices of the system, through
// miniaudio (PulseAudio, PipeWire, ALSA, CoreAudio, WASAPI...). miniaudio
// resamples between the device's native rate and SampleRate.
type Malgo struct {
	ctx *malgo.AllocatedContext
	dev *malgo.Device

	captureID  *malgo.DeviceID // nil: default device
	playbackID *malgo.DeviceID

	mu       sync.Mutex
	capture  func([]int16)
	playback func([]int16)
	inAcc    []int16 // captured samples not yet forming a whole frame
	outRest  []int16 // decoded samples not yet handed to the device
	frame    []int16 // scratch frame for playback
}

// OpenMalgo initialises the audio backend. logf, if not nil, receives
// miniaudio's log messages.
func OpenMalgo(logf func(string)) (*Malgo, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, logf)
	if err != nil {
		return nil, fmt.Errorf("audio: init context: %w", err)
	}
	return &Malgo{ctx: ctx, frame: make([]int16, FrameSamples)}, nil
}

// Devices lists the capture and playback devices.
func (m *Malgo) Devices() ([]DeviceInfo, error) {
	var out []DeviceInfo
	for _, kind := range []malgo.DeviceType{malgo.Capture, malgo.Playback} {
		infos, err := m.ctx.Devices(kind)
		if err != nil {
			return nil, fmt.Errorf("audio: list devices: %w", err)
		}
		for i := range infos {
			out = append(out, DeviceInfo{
				Name:    infos[i].Name(),
				Capture: kind == malgo.Capture,
				Default: infos[i].IsDefault != 0,
			})
		}
	}
	return out, nil
}

// ErrNoDevice is returned by Select for a name that is not listed.
var ErrNoDevice = errors.New("audio: no such device")

// Select chooses the capture and playback devices by name, as listed by
// Devices; an empty name keeps the system default. It must be called
// before Start.
func (m *Malgo) Select(capture, playback string) error {
	m.captureID, m.playbackID = nil, nil
	for _, want := range []struct {
		name string
		kind malgo.DeviceType
		id   **malgo.DeviceID
	}{{capture, malgo.Capture, &m.captureID}, {playback, malgo.Playback, &m.playbackID}} {
		if want.name == "" {
			continue
		}
		infos, err := m.ctx.Devices(want.kind)
		if err != nil {
			return fmt.Errorf("audio: list devices: %w", err)
		}
		found := false
		for i := range infos {
			if infos[i].Name() == want.name {
				id := infos[i].ID
				*want.id = &id
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: %q", ErrNoDevice, want.name)
		}
	}
	return nil
}

// Start implements Device.
func (m *Malgo) Start(capture, playback func([]int16)) error {
	m.mu.Lock()
	m.capture, m.playback = capture, playback
	m.mu.Unlock()

	cfg := malgo.DefaultDeviceConfig(malgo.Duplex)
	cfg.SampleRate = SampleRate
	cfg.PeriodSizeInFrames = FrameSamples
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = 1
	cfg.Playback.Format = malgo.FormatS16
	cfg.Playback.Channels = 1
	cfg.Alsa.NoMMap = 1
	if m.captureID != nil {
		cfg.Capture.DeviceID = m.captureID.Pointer()
	}
	if m.playbackID != nil {
		cfg.Playback.DeviceID = m.playbackID.Pointer()
	}

	dev, err := malgo.InitDevice(m.ctx.Context, cfg, malgo.DeviceCallbacks{Data: m.onData})
	if err != nil {
		return fmt.Errorf("audio: open device: %w", err)
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return fmt.Errorf("audio: start device: %w", err)
	}
	m.dev = dev
	return nil
}

// onData runs on miniaudio's thread with n frames of input and room for n
// frames of output. The device is configured for n == FrameSamples but the
// code copes with any n.
func (m *Malgo) onData(out, in []byte, n uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i < int(n) && 2*i+1 < len(in); i++ {
		m.inAcc = append(m.inAcc, int16(binary.NativeEndian.Uint16(in[2*i:]))) //nolint:gosec // sample bits
	}
	for len(m.inAcc) >= FrameSamples {
		m.capture(m.inAcc[:FrameSamples])
		m.inAcc = append(m.inAcc[:0], m.inAcc[FrameSamples:]...)
	}

	for i := 0; i < int(n) && 2*i+1 < len(out); i++ {
		if len(m.outRest) == 0 {
			clear(m.frame)
			m.playback(m.frame)
			m.outRest = m.frame
		}
		binary.NativeEndian.PutUint16(out[2*i:], uint16(m.outRest[0])) //nolint:gosec // sample bits
		m.outRest = m.outRest[1:]
	}
}

// Close implements Device. It may be called without Start.
func (m *Malgo) Close() error {
	if m.dev != nil {
		m.dev.Uninit()
		m.dev = nil
	}
	if m.ctx != nil {
		err := m.ctx.Uninit()
		m.ctx.Free()
		m.ctx = nil
		return err
	}
	return nil
}
