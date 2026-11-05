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

// Command iaxphone-gui is the IAX2 softphone with a Fyne window: one
// account, one call at a time, a keypad that dials or sends DTMF, mute and
// level meters. Settings live in Fyne's preferences for the app id.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"

	"github.com/ppomes/iaxphone/audio"
	"github.com/ppomes/iaxphone/phone"
)

const appID = "com.github.ppomes.iaxphone"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	a := app.NewWithID(appID)
	g := newGUI(a, log)
	g.win.Resize(fyne.NewSize(340, 560))
	lock, err := claimInstance(instanceSocket(), func() { fyne.Do(g.showWindow) })
	if errors.Is(err, errAlreadyRunning) {
		fmt.Fprintln(os.Stderr, "iaxphone-gui is already running, showing its window")
		return
	}
	if err != nil {
		log.Warn("single instance", "err", err) // run anyway
	} else {
		defer func() { _ = lock.Close() }()
	}
	g.setupTray()
	g.start()
	go g.tick()
	if g.tray != nil && g.settings.configured() {
		// In the status area the phone starts behind its icon, with no
		// window to take a place in the dock; the icon, its menu, an
		// incoming call or a second iaxphone-gui bring the window up.
		a.Run()
	} else {
		g.win.ShowAndRun()
	}
	g.stop()
}

// settings is what the user configures, persisted in the app preferences.
type settings struct {
	Server       string
	Username     string
	Secret       string
	CallerNumber string
	CallerName   string
	Register     bool
	JitterMs     int
	Audio        string // "default" or "none"
	Mic          string // capture device name, "" for the default
	Speaker      string // playback device name, "" for the default
}

// configured reports whether there is an account to connect with.
func (s settings) configured() bool {
	return s.Server != "" && s.Username != ""
}

func loadSettings(p fyne.Preferences) settings {
	return settings{
		Server:       p.StringWithFallback("server", "127.0.0.1"),
		Username:     p.String("username"),
		Secret:       p.String("secret"),
		CallerNumber: p.String("caller_number"),
		CallerName:   p.StringWithFallback("caller_name", "iaxphone"),
		Register:     p.BoolWithFallback("register", true),
		JitterMs:     p.IntWithFallback("jitter_ms", audio.DefaultJitterMs),
		Audio:        p.StringWithFallback("audio", "default"),
		Mic:          p.String("mic"),
		Speaker:      p.String("speaker"),
	}
}

func (s settings) save(p fyne.Preferences) {
	p.SetString("server", s.Server)
	p.SetString("username", s.Username)
	p.SetString("secret", s.Secret)
	p.SetString("caller_number", s.CallerNumber)
	p.SetString("caller_name", s.CallerName)
	p.SetBool("register", s.Register)
	p.SetInt("jitter_ms", s.JitterMs)
	p.SetString("audio", s.Audio)
	p.SetString("mic", s.Mic)
	p.SetString("speaker", s.Speaker)
}

// phoneConfig builds the controller configuration for the settings.
func (s settings) phoneConfig(log *slog.Logger) phone.Config {
	open := func() (audio.Device, error) {
		m, err := audio.OpenMalgo(func(msg string) { log.Debug("miniaudio", "msg", strings.TrimSpace(msg)) })
		if err != nil {
			return nil, err
		}
		if err := m.Select(s.Mic, s.Speaker); err != nil {
			_ = m.Close()
			return nil, err
		}
		return m, nil
	}
	if s.Audio == "none" {
		open = func() (audio.Device, error) { return &audio.NullDevice{}, nil }
	}
	return phone.Config{
		Server:       s.Server,
		Username:     s.Username,
		Secret:       s.Secret,
		CallerNumber: s.CallerNumber,
		CallerName:   s.CallerName,
		Register:     s.Register,
		OpenDevice:   open,
		JitterMs:     s.JitterMs,
		Logger:       log,
	}
}

// start creates the controller for the current settings and forwards its
// events to the window. It runs on the UI goroutine.
func (g *gui) start() {
	if !g.settings.configured() {
		g.showSettings()
		return
	}
	p, err := g.connect()
	if err != nil {
		g.showError(err)
		return
	}
	go func() {
		for ev := range p.Events() {
			fyne.Do(func() { g.handle(ev) })
		}
	}()
}

// connect starts the controller for the current settings. The caller
// forwards its events to handle.
func (g *gui) connect() (*phone.Phone, error) {
	p := phone.New(g.settings.phoneConfig(g.log))
	if err := p.Start(context.Background()); err != nil {
		return nil, err
	}
	g.phone = p
	g.setRegistration(phone.RegInfo{})
	g.updateButtons()
	return p, nil
}

// stop closes the controller; the events goroutine ends with its channel.
func (g *gui) stop() {
	if g.phone == nil {
		return
	}
	_ = g.phone.Close()
	g.phone = nil
}

// restart applies new settings.
func (g *gui) restart() {
	g.stop()
	g.reset()
	g.start()
}

// tick refreshes the meters and the talk time ten times a second.
func (g *gui) tick() {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		fyne.Do(g.refresh)
	}
}
