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
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/ppomes/iaxphone/audio"
	"github.com/ppomes/iaxphone/history"
	"github.com/ppomes/iaxphone/phone"
)

// gui owns the window and the controller. Everything in it is touched on
// the UI goroutine only: controller events and the ticker come through
// fyne.Do.
type gui struct {
	app      fyne.App
	win      fyne.Window
	log      *slog.Logger
	settings settings
	phone    *phone.Phone
	devices  func() ([]audio.DeviceInfo, error) // for the settings; listDevices
	form     *settingsForm                      // the open settings form, if any
	tray     *tray                              // the status icon, nil without one
	history  *history.Log                       // past calls, nil without a file

	historyList *widget.List // the open history list, if any

	reg      phone.RegInfo
	call     phone.CallInfo
	inCall   bool
	keysIn   string // DTMF received during the call
	keysOut  string // DTMF sent during the call
	lastCall string // summary of the previous call

	regStatus  *widget.Label
	callStatus *widget.Label
	number     *widget.Entry
	keypad     []*keyButton
	callBtn    *widget.Button
	answerBtn  *widget.Button
	hangupBtn  *widget.Button
	mute       *widget.Check
	micMeter   *widget.ProgressBar
	spkMeter   *widget.ProgressBar
	keys       *widget.Label
	details    *widget.Label
}

func newGUI(a fyne.App, log *slog.Logger) *gui {
	g := &gui{app: a, win: a.NewWindow("iaxphone"), log: log, settings: loadSettings(a.Preferences()), devices: listDevices}
	g.build()
	return g
}

func (g *gui) build() {
	g.regStatus = widget.NewLabel("")
	g.regStatus.Wrapping = fyne.TextWrapWord
	g.callStatus = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	g.callStatus.Wrapping = fyne.TextWrapWord

	g.number = widget.NewEntry()
	g.number.SetPlaceHolder("Number")
	g.number.OnSubmitted = func(string) { g.dial() }

	var pad []fyne.CanvasObject
	for _, k := range "123456789*0#" {
		key := byte(k)
		b := newKeyButton(string(k), func() { g.keyDown(key) }, g.keyUp)
		g.keypad = append(g.keypad, b)
		pad = append(pad, b)
	}

	g.callBtn = widget.NewButtonWithIcon("Call", theme.MediaPlayIcon(), g.dial)
	g.callBtn.Importance = widget.HighImportance
	g.answerBtn = widget.NewButtonWithIcon("Answer", theme.ConfirmIcon(), g.answer)
	g.answerBtn.Importance = widget.SuccessImportance
	g.hangupBtn = widget.NewButtonWithIcon("Hang up", theme.CancelIcon(), g.hangup)
	g.hangupBtn.Importance = widget.DangerImportance
	g.mute = widget.NewCheck("Mute", func(on bool) {
		if g.phone != nil {
			g.phone.SetMuted(on)
		}
	})

	g.micMeter = widget.NewProgressBar()
	g.micMeter.TextFormatter = func() string { return "" }
	g.spkMeter = widget.NewProgressBar()
	g.spkMeter.TextFormatter = func() string { return "" }

	g.keys = widget.NewLabel("")
	g.keys.Wrapping = fyne.TextWrapWord
	g.details = widget.NewLabel("")
	g.details.Wrapping = fyne.TextWrapWord

	settingsBtn := widget.NewButtonWithIcon("", theme.SettingsIcon(), g.showSettings)
	historyBtn := widget.NewButtonWithIcon("", theme.HistoryIcon(), g.showHistory)
	top := container.NewBorder(nil, nil, nil, container.NewHBox(historyBtn, settingsBtn), g.regStatus)
	meters := container.New(&twoColumns{},
		widget.NewLabel("Mic level"), g.micMeter,
		widget.NewLabel("Speaker level"), g.spkMeter,
	)
	content := container.NewVBox(
		top,
		widget.NewSeparator(),
		g.callStatus,
		g.number,
		container.NewGridWithColumns(3, pad...),
		container.NewGridWithColumns(3, g.callBtn, g.answerBtn, g.hangupBtn),
		container.NewHBox(g.mute),
		meters,
		g.keys,
		widget.NewSeparator(),
		g.details,
	)
	g.win.SetContent(container.NewPadded(content))
	g.reset()
	g.setRegistration(phone.RegInfo{})
}

// reset puts the window in the idle state.
func (g *gui) reset() {
	g.inCall = false
	g.call = phone.CallInfo{}
	g.keysIn, g.keysOut = "", ""
	g.callStatus.SetText("Idle")
	g.keys.SetText("")
	g.details.SetText(g.lastCall)
	g.micMeter.SetValue(0)
	g.spkMeter.SetValue(0)
	g.updateButtons()
}

// updateButtons enables what makes sense in the current state.
func (g *gui) updateButtons() {
	ready := g.phone != nil
	setEnabled(g.callBtn, ready && !g.inCall)
	setEnabled(g.answerBtn, ready && g.inCall && g.call.State == phone.CallRinging && !g.call.Outgoing)
	setEnabled(g.hangupBtn, ready && g.inCall)
	setEnabled(g.mute, ready && g.inCall && g.call.State == phone.CallActive)
	for _, b := range g.keypad {
		setEnabled(b, ready && (!g.inCall || g.call.State == phone.CallActive))
	}
	g.updateTray()
}

func setEnabled(w fyne.Disableable, on bool) {
	if on {
		w.Enable()
	} else {
		w.Disable()
	}
}

// handle applies one controller event to the window.
func (g *gui) handle(ev phone.Event) {
	switch ev.Kind {
	case phone.EventRegistration:
		g.setRegistration(ev.Registration)
	case phone.EventIncoming:
		g.inCall, g.call = true, ev.Call
		g.keysIn, g.keysOut = "", ""
		g.keys.SetText("")
		g.callStatus.SetText(fmt.Sprintf("Incoming call from %s", callerText(ev.Call)))
		g.app.SendNotification(fyne.NewNotification("iaxphone", "Incoming call from "+callerText(ev.Call)))
		if g.tray != nil {
			g.showWindow() // it may be hidden behind the icon
		}
	case phone.EventDialing:
		g.inCall, g.call = true, ev.Call
		g.keysIn, g.keysOut = "", ""
		g.keys.SetText("")
		g.callStatus.SetText("Calling " + ev.Call.Number + "…")
	case phone.EventRemoteRinging:
		g.call = ev.Call
		g.callStatus.SetText("Ringing " + ev.Call.Number + "…")
	case phone.EventAnswered:
		g.call = ev.Call
		g.mute.SetChecked(false)
		g.refresh()
	case phone.EventDTMF:
		g.keysIn += string(ev.Digit)
		g.showKeys()
	case phone.EventEnded:
		g.lastCall = endedText(ev.Call)
		g.recordCall(ev.Call)
		g.reset()
	}
	g.updateButtons()
}

func (g *gui) setRegistration(r phone.RegInfo) {
	g.reg = r
	switch {
	case g.phone == nil:
		g.regStatus.SetText("Not connected")
	case !g.settings.Register:
		g.regStatus.SetText("Not registering at " + g.settings.Server)
	case r.State == phone.RegOK:
		g.regStatus.SetText(fmt.Sprintf("Registered at %s (refresh %s)", g.settings.Server, r.Refresh))
	case r.State == phone.RegFailed:
		g.regStatus.SetText("Registration failed: " + r.Err.Error())
	default:
		g.regStatus.SetText("Registering at " + g.settings.Server + "…")
	}
	g.updateTray()
}

// refresh updates the live parts: meters and talk time.
func (g *gui) refresh() {
	if g.phone == nil || !g.inCall {
		return
	}
	if g.call.State != phone.CallActive {
		return
	}
	in, out := g.phone.Levels()
	g.micMeter.SetValue(meter(in))
	g.spkMeter.SetValue(meter(out))
	g.callStatus.SetText(fmt.Sprintf("In call with %s · %s · %s", callerText(g.call), g.call.Format,
		talkTime(g.call.TalkTime(time.Now()))))
}

// meter maps a peak level in dBFS onto 0..1 over a 60 dB range.
func meter(dbfs float64) float64 {
	if math.IsInf(dbfs, -1) || math.IsNaN(dbfs) {
		return 0
	}
	return math.Max(0, math.Min(1, (dbfs+60)/60))
}

func talkTime(d time.Duration) string {
	s := int(d.Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func callerText(c phone.CallInfo) string {
	if c.Name != "" && !c.Outgoing {
		return fmt.Sprintf("%s <%s>", c.Name, c.Number)
	}
	return c.Number
}

func endedText(c phone.CallInfo) string {
	direction := "Call to " + c.Number
	if !c.Outgoing {
		direction = "Call from " + callerText(c)
	}
	if c.Err != nil {
		return direction + " failed: " + c.Err.Error()
	}
	by := "the peer"
	if c.HungUpByUs {
		by = "us"
	}
	st := c.Stats
	return fmt.Sprintf("%s ended by %s (%s), %s, talk time %s.\nSent %d, played %d, lost %d, late %d, underruns %d.",
		direction, by, c.Cause, c.Format, talkTime(c.TalkTime(time.Now())),
		st.Sent, st.Played, st.Jitter.Lost, st.Jitter.Late, st.Jitter.Underruns)
}

func (g *gui) showKeys() {
	text := ""
	if g.keysOut != "" {
		text = "Keys sent: " + g.keysOut
	}
	if g.keysIn != "" {
		if text != "" {
			text += "\n"
		}
		text += "Keys received: " + g.keysIn
	}
	g.keys.SetText(text)
}

// keyDown handles a keypad button going down: a digit to dial when idle,
// a DTMF key during a call. Either way the key sounds until keyUp.
func (g *gui) keyDown(key byte) {
	if g.phone == nil {
		return
	}
	switch {
	case !g.inCall:
		g.number.SetText(g.number.Text + string(key))
		g.number.CursorColumn = len(g.number.Text)
	case g.call.State == phone.CallActive:
		g.keysOut += string(key)
		g.showKeys()
	default:
		return
	}
	if err := g.phone.PressKey(key); err != nil {
		g.log.Warn("key", "key", string(key), "err", err)
	}
}

func (g *gui) keyUp() {
	if g.phone != nil {
		g.phone.ReleaseKey()
	}
}

func (g *gui) dial() {
	if g.phone == nil || g.inCall || g.number.Text == "" {
		return
	}
	if err := g.phone.Dial(g.number.Text); err != nil {
		g.showError(err)
	}
}

func (g *gui) answer() {
	if g.phone == nil {
		return
	}
	if err := g.phone.Answer(); err != nil {
		g.showError(err)
	}
}

func (g *gui) hangup() {
	if g.phone == nil {
		return
	}
	if err := g.phone.Hangup(); err != nil {
		g.showError(err)
	}
}

func (g *gui) showError(err error) {
	g.log.Error("ui", "err", err)
	g.win.Show() // it may be behind the status icon
	dialog.ShowError(err, g.win)
}

// settingsForm is the content of the settings dialog.
type settingsForm struct {
	items        []*widget.FormItem
	mic, speaker *widget.Select
	read         func() settings // the settings as edited
}

// showSettings edits the settings and restarts the controller on save.
func (g *gui) showSettings() {
	g.win.Show() // it may be behind the status icon
	f := g.newSettingsForm()
	g.form = f
	d := dialog.NewForm("Settings", "Save", "Cancel", f.items, func(ok bool) {
		g.form = nil
		if !ok {
			return
		}
		g.settings = f.read()
		g.settings.save(g.app.Preferences())
		g.restart()
	}, g.win)
	d.Resize(fyne.NewSize(320, 0))
	d.Show()
}

func (g *gui) newSettingsForm() *settingsForm {
	s := g.settings
	server := widget.NewEntry()
	server.SetText(s.Server)
	user := widget.NewEntry()
	user.SetText(s.Username)
	secret := widget.NewPasswordEntry()
	secret.SetText(s.Secret)
	cid := widget.NewEntry()
	cid.SetText(s.CallerNumber)
	name := widget.NewEntry()
	name.SetText(s.CallerName)
	register := widget.NewCheck("", nil)
	register.SetChecked(s.Register)
	jitter := widget.NewEntry()
	jitter.SetText(strconv.Itoa(s.JitterMs))
	jitter.Validator = func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n < 20 || n > 500 {
			return fmt.Errorf("20 to 500 ms")
		}
		return nil
	}
	sound := widget.NewSelect([]string{"default", "none"}, nil)
	sound.SetSelected(s.Audio)
	mics, speakers := g.deviceNames()
	mic := widget.NewSelect(mics, nil)
	mic.SetSelected(orDefault(s.Mic))
	speaker := widget.NewSelect(speakers, nil)
	speaker.SetSelected(orDefault(s.Speaker))

	items := []*widget.FormItem{
		widget.NewFormItem("Server", server),
		widget.NewFormItem("Username", user),
		widget.NewFormItem("Secret", secret),
		widget.NewFormItem("Caller number", cid),
		widget.NewFormItem("Caller name", name),
		widget.NewFormItem("Register", register),
		widget.NewFormItem("Jitter (ms)", jitter),
		widget.NewFormItem("Sound", sound),
		widget.NewFormItem("Microphone", mic),
		widget.NewFormItem("Speaker", speaker),
	}
	read := func() settings {
		ms, _ := strconv.Atoi(jitter.Text)
		return settings{
			Server: server.Text, Username: user.Text, Secret: secret.Text,
			CallerNumber: cid.Text, CallerName: name.Text, Register: register.Checked,
			JitterMs: ms, Audio: sound.Selected,
			Mic: fromDefault(mic.Selected), Speaker: fromDefault(speaker.Selected),
		}
	}
	return &settingsForm{items: items, mic: mic, speaker: speaker, read: read}
}

// defaultDevice is the settings entry for the system default device.
const defaultDevice = "(default)"

func orDefault(name string) string {
	if name == "" {
		return defaultDevice
	}
	return name
}

func fromDefault(name string) string {
	if name == defaultDevice {
		return ""
	}
	return name
}

// deviceNames lists the capture and playback devices for the settings,
// the default entry first, then whatever is configured if it is missing
// (unplugged), so that a saved choice survives.
func (g *gui) deviceNames() (mics, speakers []string) {
	mics, speakers = []string{defaultDevice}, []string{defaultDevice}
	devs, err := g.devices()
	if err != nil {
		g.log.Warn("cannot list devices", "err", err)
	}
	for _, d := range devs {
		if d.Capture {
			mics = append(mics, d.Name)
		} else {
			speakers = append(speakers, d.Name)
		}
	}
	if g.settings.Mic != "" && !slices.Contains(mics, g.settings.Mic) {
		mics = append(mics, g.settings.Mic)
	}
	if g.settings.Speaker != "" && !slices.Contains(speakers, g.settings.Speaker) {
		speakers = append(speakers, g.settings.Speaker)
	}
	return mics, speakers
}

// keyButton is a keypad button that reports when it goes down and when it
// is let go, so that a key sounds for as long as it is held. A tap that
// comes without mouse events (touch, keyboard) is a short press.
type keyButton struct {
	widget.Button
	onPress, onRelease func()
	down               bool // between MouseDown and MouseUp
	handled            bool // the next Tapped belongs to a MouseDown
}

func newKeyButton(label string, onPress, onRelease func()) *keyButton {
	k := &keyButton{onPress: onPress, onRelease: onRelease}
	k.Text = label
	k.ExtendBaseWidget(k)
	return k
}

// MouseDown implements desktop.Mouseable.
func (k *keyButton) MouseDown(*desktop.MouseEvent) {
	if k.Disabled() {
		return
	}
	k.down, k.handled = true, true
	k.onPress()
}

// MouseUp implements desktop.Mouseable.
func (k *keyButton) MouseUp(*desktop.MouseEvent) { k.release() }

// MouseOut lets the key go when the pointer leaves the button while it
// is down: the release would go elsewhere.
func (k *keyButton) MouseOut() {
	k.Button.MouseOut()
	k.release()
}

func (k *keyButton) release() {
	if k.down {
		k.down = false
		k.onRelease()
	}
}

// Tapped implements fyne.Tappable.
func (k *keyButton) Tapped(e *fyne.PointEvent) {
	k.Button.Tapped(e)
	if k.handled {
		k.handled = false
		return
	}
	if k.Disabled() {
		return
	}
	k.onPress()
	k.onRelease()
}

// listDevices asks miniaudio for the sound devices.
func listDevices() ([]audio.DeviceInfo, error) {
	m, err := audio.OpenMalgo(nil)
	if err != nil {
		return nil, err
	}
	defer m.Close() //nolint:errcheck
	return m.Devices()
}

// twoColumns lays out label/value pairs: labels take their minimum width,
// values the rest.
type twoColumns struct{}

func (twoColumns) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var w, h float32
	for i := 0; i+1 < len(objects); i += 2 {
		l, v := objects[i].MinSize(), objects[i+1].MinSize()
		w = max(w, l.Width+v.Width+theme.Padding())
		h += max(l.Height, v.Height) + theme.Padding()
	}
	return fyne.NewSize(w, h)
}

func (twoColumns) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var labelW float32
	for i := 0; i < len(objects); i += 2 {
		labelW = max(labelW, objects[i].MinSize().Width)
	}
	var y float32
	for i := 0; i+1 < len(objects); i += 2 {
		l, v := objects[i], objects[i+1]
		h := max(l.MinSize().Height, v.MinSize().Height)
		l.Resize(fyne.NewSize(labelW, h))
		l.Move(fyne.NewPos(0, y))
		v.Resize(fyne.NewSize(size.Width-labelW-theme.Padding(), h))
		v.Move(fyne.NewPos(labelW+theme.Padding(), y))
		y += h + theme.Padding()
	}
}
