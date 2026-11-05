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

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"github.com/godbus/dbus/v5"

	"github.com/ppomes/iaxphone/phone"
)

// trayState is what the tray icon shows: the colour of the handset.
type trayState int

const (
	trayOffline trayState = iota // not connected, or the registration failed
	trayIdle                     // ready for a call
	trayRinging                  // a call is ringing, either way
	trayInCall                   // a call is up
)

var trayColours = map[trayState]string{
	trayOffline: "#7a7a7a",
	trayIdle:    "#1e5aa8",
	trayRinging: "#e08a00",
	trayInCall:  "#2e8b57",
}

// trayIcon is the handset on a rounded square of the state's colour, the
// same drawing as the desktop icon. Fyne rasterises it for the tray.
func trayIcon(s trayState) fyne.Resource {
	svg := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64">
  <rect x="4" y="4" width="56" height="56" rx="12" fill="%s"/>
  <path d="M20 14c-2 0-5 2-6 5-2 6 4 18 12 26s20 14 26 12c3-1 5-4 5-6l-2-6c-1-2-3-2-5-1l-5 2c-2 1-3 0-5-2l-8-8c-2-2-3-3-2-5l2-5c1-2 1-4-1-5l-6-2c-1-1-3-1-5 1z" fill="#fff"/>
</svg>
`, trayColours[s])
	return fyne.NewStaticResource(fmt.Sprintf("iaxphone-%d.svg", s), []byte(svg))
}

// trayStateNow derives the icon state from the window state.
func (g *gui) trayStateNow() trayState {
	switch {
	case g.phone == nil, g.settings.Register && g.reg.State != phone.RegOK:
		return trayOffline
	case !g.inCall:
		return trayIdle
	case g.call.State == phone.CallActive:
		return trayInCall
	default:
		return trayRinging
	}
}

// tray is the status icon and its menu, nil when the desktop has none.
type tray struct {
	app    desktop.App
	menu   *fyne.Menu
	show   *fyne.MenuItem
	answer *fyne.MenuItem
	hangup *fyne.MenuItem
	state  trayState
	set    bool // an icon has been set
}

// setupTray adds the status icon when the app and the desktop support it.
// Closing the window then hides it behind the icon instead of quitting;
// tapping the icon or its first entry brings it back.
func (g *gui) setupTray() {
	desk, ok := g.app.(desktop.App)
	if !ok || !hasStatusNotifierHost() {
		return
	}
	t := &tray{app: desk}
	t.show = fyne.NewMenuItem("Show iaxphone", g.showWindow)
	t.answer = fyne.NewMenuItem("Answer", g.answer)
	t.hangup = fyne.NewMenuItem("Hang up", g.hangup)
	quit := fyne.NewMenuItem("Quit", g.app.Quit)
	quit.IsQuit = true
	t.menu = fyne.NewMenu("iaxphone", t.show, fyne.NewMenuItemSeparator(), t.answer, t.hangup,
		fyne.NewMenuItemSeparator(), quit)
	desk.SetSystemTrayMenu(t.menu)
	desk.SetSystemTrayWindow(g.win)
	g.tray = t
	g.updateTray()
}

// updateTray keeps the icon and the menu in step with the window.
func (g *gui) updateTray() {
	t := g.tray
	if t == nil {
		return
	}
	if s := g.trayStateNow(); !t.set || s != t.state {
		t.state, t.set = s, true
		t.app.SetSystemTrayIcon(trayIcon(s))
	}
	answer := !g.answerBtn.Disabled()
	hangup := !g.hangupBtn.Disabled()
	if t.answer.Disabled != !answer || t.hangup.Disabled != !hangup {
		t.answer.Disabled = !answer
		t.hangup.Disabled = !hangup
		t.menu.Refresh()
	}
}

// showWindow brings the window back from the tray, or to the front.
func (g *gui) showWindow() {
	g.win.Show()
	g.win.RequestFocus()
}

// hasStatusNotifierHost reports whether something on the session bus
// takes status icons (the StatusNotifierItem protocol every tray on Linux
// speaks today). Without it a hidden window could not be brought back, so
// the window keeps its normal close.
func hasStatusNotifierHost() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	var has bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has)
	return err == nil && has
}
