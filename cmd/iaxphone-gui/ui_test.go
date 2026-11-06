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
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/ppomes/iaxphone/audio"
	"github.com/ppomes/iaxphone/history"
	"github.com/ppomes/iaxphone/internal/fakeiax"
	"github.com/ppomes/iaxphone/phone"
)

// pump applies the controller's events to the window on the test goroutine
// until cond holds, so that nothing touches the widgets concurrently.
func pump(t *testing.T, g *gui, p *phone.Phone, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !cond() {
		select {
		case ev, ok := <-p.Events():
			if !ok {
				t.Fatalf("events closed while waiting for %s", what)
			}
			g.handle(ev)
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timeout waiting for %s: status %q, keys %q, details %q", what,
				g.callStatus.Text, g.keys.Text, g.details.Text)
		}
	}
}

func hasText(l *widget.Label, s string) func() bool {
	return func() bool { return strings.Contains(l.Text, s) }
}

func TestWindowDrivesCalls(t *testing.T) {
	srv := fakeiax.New(t)
	a := test.NewApp()
	g := newGUI(a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	g.settings = settings{Server: srv.Addr(), Username: "phone", Register: true, JitterMs: audio.DefaultJitterMs, Audio: "none"}
	var err error
	if g.history, err = history.Open(filepath.Join(t.TempDir(), "history.jsonl")); err != nil {
		t.Fatal(err)
	}
	if !g.callBtn.Disabled() || g.regStatus.Text != "Not connected" {
		t.Errorf("before connect: call enabled=%v status %q", !g.callBtn.Disabled(), g.regStatus.Text)
	}
	if g.tray != nil || g.trayStateNow() != trayOffline {
		t.Errorf("before connect: tray %v state %v", g.tray, g.trayStateNow())
	}
	p, err := g.connect()
	if err != nil {
		t.Fatal(err)
	}
	defer g.stop()
	pump(t, g, p, "registration", hasText(g.regStatus, "Registered at "+srv.Addr()))
	// The socket listens on every address; the fake server must reach it
	// on loopback.
	_, port, err := net.SplitHostPort(p.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	us := net.JoinHostPort("127.0.0.1", port)
	if g.callBtn.Disabled() || !g.hangupBtn.Disabled() || !g.answerBtn.Disabled() || !g.mute.Disabled() ||
		!g.hold.Disabled() || !g.transfer.Disabled() {
		t.Error("idle: wrong buttons")
	}
	if g.trayStateNow() != trayIdle {
		t.Errorf("idle: tray state %v", g.trayStateNow())
	}

	// Outgoing call: keypad fills the number, Call dials, keypad then
	// sends DTMF, the peer's keys are shown, Hold holds, Transfer sends
	// the peer elsewhere and the server ends the call.
	test.Tap(g.keypad[5]) // "6"
	test.Type(g.number, "00")
	if g.number.Text != "600" {
		t.Fatalf("number %q", g.number.Text)
	}
	test.Tap(g.callBtn)
	pump(t, g, p, "call up", hasText(g.callStatus, "In call with 600 · ulaw · 0:0"))
	if !g.callBtn.Disabled() || g.hangupBtn.Disabled() || !g.answerBtn.Disabled() || g.mute.Disabled() ||
		g.hold.Disabled() || g.transfer.Disabled() {
		t.Error("in call: wrong buttons")
	}
	if g.trayStateNow() != trayInCall {
		t.Errorf("in call: tray state %v", g.trayStateNow())
	}
	// A key held with the mouse: DTMF_END waits for the release.
	g.keypad[4].MouseDown(&desktop.MouseEvent{}) // "5"
	pump(t, g, p, "key down", func() bool { return g.keys.Text == "Keys sent: 5" })
	time.Sleep(2 * phone.MinKeyDuration)
	if got := srv.Digits(); got != "" {
		t.Errorf("digit ended while held: %q", got)
	}
	g.keypad[4].MouseUp(&desktop.MouseEvent{})
	g.keypad[4].Tapped(nil) // the tap that follows a click is not a second key
	pump(t, g, p, "digit at the server", func() bool { return srv.Digits() == "5" })
	if g.keys.Text != "Keys sent: 5" {
		t.Errorf("keys %q", g.keys.Text)
	}
	srv.PressKey('7')
	pump(t, g, p, "received key", hasText(g.keys, "Keys received: 7"))
	if !strings.HasPrefix(g.keys.Text, "Keys sent: 5\n") {
		t.Errorf("keys %q", g.keys.Text)
	}
	test.Tap(g.mute)
	if !g.phone.Muted() {
		t.Error("not muted")
	}
	test.Tap(g.hold)
	pump(t, g, p, "on hold", hasText(g.callStatus, " · on hold"))
	if !srv.Held() {
		t.Error("server not held")
	}
	test.Tap(g.hold)
	pump(t, g, p, "off hold", func() bool { return !strings.Contains(g.callStatus.Text, "on hold") })
	if srv.Held() {
		t.Error("server still held")
	}
	g.number.SetText("601")
	test.Tap(g.transfer)
	pump(t, g, p, "transferred", hasText(g.details, "Call to 600 transferred to 601, ulaw"))
	if g.callStatus.Text != "Idle" || g.keys.Text != "" || g.callBtn.Disabled() || !g.hangupBtn.Disabled() || g.hold.Checked {
		t.Errorf("after transfer: status %q keys %q hold %v", g.callStatus.Text, g.keys.Text, g.hold.Checked)
	}
	if got := srv.Transfers(); len(got) != 1 || got[0] != "601" {
		t.Errorf("server saw transfers %q", got)
	}

	// Incoming call: Answer, then the peer hangs up.
	srv.Originate(us)
	pump(t, g, p, "incoming", hasText(g.callStatus, "Incoming call from Asterisk <1001>"))
	if g.answerBtn.Disabled() || g.hangupBtn.Disabled() || !g.callBtn.Disabled() || !g.keypad[0].Disabled() {
		t.Error("ringing: wrong buttons")
	}
	if g.trayStateNow() != trayRinging {
		t.Errorf("ringing: tray state %v", g.trayStateNow())
	}
	test.Tap(g.answerBtn)
	pump(t, g, p, "answered", hasText(g.callStatus, "In call with Asterisk <1001>"))
	if g.mute.Checked {
		t.Error("mute not reset for the new call")
	}
	// The server asks us to call 777 instead: shown, and put in the entry
	// only when nothing is being typed there.
	srv.Transfer("777")
	pump(t, g, p, "transfer request", hasText(g.callStatus, "Asked to call 777 instead"))
	if g.number.Text != "601" {
		t.Errorf("number being typed replaced by %q", g.number.Text)
	}
	g.number.SetText("")
	srv.Transfer("778")
	pump(t, g, p, "second transfer request", func() bool { return g.number.Text == "778" })
	srv.Hangup()
	pump(t, g, p, "peer hangup", hasText(g.details, "Call from Asterisk <1001> ended by the peer (Normal Clearing (16))"))

	// Decline an incoming call.
	srv.Originate(us)
	pump(t, g, p, "incoming again", hasText(g.callStatus, "Incoming call"))
	test.Tap(g.hangupBtn)
	pump(t, g, p, "declined", hasText(g.details, "ended by us"))
	if causes := srv.HangupCauses(); len(causes) != 1 || causes[0] != "Call Rejected" {
		t.Errorf("server saw %q", causes)
	}

	// The three calls are in the history, newest first; picking one from
	// the list puts its number back in the entry.
	h := g.history.Entries()
	if len(h) != 3 || !h[0].Missed() || h[0].Name != "Asterisk" || h[1].Outgoing || !h[1].Answered ||
		!h[2].Outgoing || !h[2].Answered || h[2].Number != "600" || h[2].Cause != "transferred to 601" || h[0].Cause != "hung up" {
		t.Fatalf("history %+v", h)
	}
	g.number.SetText("")
	g.showHistory()
	if g.historyList == nil {
		t.Fatal("history list not open")
	}
	g.historyList.Select(2)
	if g.number.Text != "600" {
		t.Errorf("number after pick %q", g.number.Text)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	a := test.NewApp()
	s := settings{Server: "pbx:4569", Username: "u", Secret: "s", CallerNumber: "12", CallerName: "n",
		Register: false, JitterMs: 80, Audio: "none"}
	s.save(a.Preferences())
	if got := loadSettings(a.Preferences()); got != s {
		t.Errorf("got %+v", got)
	}
	cfg := s.phoneConfig(slog.Default())
	if cfg.Server != "pbx:4569" || cfg.Register || cfg.JitterMs != 80 {
		t.Errorf("config %+v", cfg)
	}
	if d, err := cfg.OpenDevice(); err != nil {
		t.Error(err)
	} else if _, ok := d.(*audio.NullDevice); !ok {
		t.Errorf("device %T", d)
	}
}

// TestSettingsPickDevice opens the settings, drops the Microphone list down,
// taps an entry and saves, the way a user does it.
func TestSettingsPickDevice(t *testing.T) {
	srv := fakeiax.New(t)
	a := test.NewApp()
	g := newGUI(a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	g.settings = settings{Server: srv.Addr(), Username: "phone", JitterMs: audio.DefaultJitterMs, Audio: "none"}
	g.devices = func() ([]audio.DeviceInfo, error) {
		return []audio.DeviceInfo{
			{Name: "Built-in Mic", Capture: true, Default: true},
			{Name: "USB Mic", Capture: true},
			{Name: "Speakers", Default: true},
		}, nil
	}
	defer g.stop()
	g.win.Resize(fyne.NewSize(340, 560))
	g.showSettings()
	if g.form == nil {
		t.Fatal("no settings form")
	}
	if got := g.form.mic.Options; !slices.Equal(got, []string{"(default)", "Built-in Mic", "USB Mic"}) {
		t.Errorf("microphones %q", got)
	}
	if got := g.form.speaker.Options; !slices.Equal(got, []string{"(default)", "Speakers"}) {
		t.Errorf("speakers %q", got)
	}
	c := g.win.Canvas()
	test.Tap(g.form.mic) // drops the list down as a pop-up
	if c.Overlays().Top() == nil {
		t.Fatal("no pop-up after tapping the select")
	}
	tapText(t, c, "USB Mic")
	if g.form.mic.Selected != "USB Mic" {
		t.Fatalf("selected %q", g.form.mic.Selected)
	}
	tapText(t, c, "Save")
	if g.form != nil {
		t.Error("form still open after Save")
	}
	if g.settings.Mic != "USB Mic" || g.settings.Speaker != "" {
		t.Errorf("settings %+v", g.settings)
	}
	if got := loadSettings(a.Preferences()); got.Mic != "USB Mic" {
		t.Errorf("saved %+v", got)
	}
	// The saved choice is offered even when the device is gone.
	g.devices = func() ([]audio.DeviceInfo, error) { return nil, nil }
	if mics, _ := g.deviceNames(); !slices.Equal(mics, []string{"(default)", "USB Mic"}) {
		t.Errorf("unplugged: %q", mics)
	}
}

// tapText taps the canvas where the text s is drawn, in the top overlay.
func tapText(t *testing.T, c fyne.Canvas, s string) {
	t.Helper()
	obj := findText(c.Overlays().Top(), s)
	if obj == nil {
		t.Fatalf("no text %q on the canvas", s)
	}
	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(obj)
	test.TapCanvas(c, pos.Add(fyne.NewPos(2, 2)))
}

// findText walks the rendered tree looking for the text s.
func findText(o fyne.CanvasObject, s string) fyne.CanvasObject {
	switch v := o.(type) {
	case nil:
		return nil
	case *canvas.Text:
		if v.Text == s {
			return v
		}
	case *widget.RichText:
		if strings.TrimSpace(v.String()) == s {
			return v
		}
	case *fyne.Container:
		for _, child := range v.Objects {
			if hit := findText(child, s); hit != nil {
				return hit
			}
		}
	case fyne.Widget:
		for _, child := range test.WidgetRenderer(v).Objects() {
			if hit := findText(child, s); hit != nil {
				return hit
			}
		}
	}
	return nil
}

func TestMeter(t *testing.T) {
	for db, want := range map[float64]float64{-60: 0, -30: 0.5, 0: 1, 6: 1, -90: 0} {
		if got := meter(db); got != want {
			t.Errorf("meter(%v) = %v, want %v", db, got, want)
		}
	}
}
