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
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/ppomes/iaxphone/history"
	"github.com/ppomes/iaxphone/phone"
)

// historyEntry is the history line for a call that just ended.
func historyEntry(c phone.CallInfo, now time.Time) history.Entry {
	e := history.Entry{
		At:       c.Started,
		Outgoing: c.Outgoing,
		Answered: !c.Answered.IsZero(),
		Number:   c.Number,
		Name:     c.Name,
		Seconds:  int(c.TalkTime(now).Round(time.Second) / time.Second),
		Cause:    c.Cause,
	}
	if c.Err != nil {
		e.Cause = c.Err.Error()
	}
	return e
}

// recordCall adds the call to the history, if there is one.
func (g *gui) recordCall(c phone.CallInfo) {
	if g.history == nil {
		return
	}
	if err := g.history.Add(historyEntry(c, time.Now())); err != nil {
		g.log.Error("history", "err", err)
	}
}

// historyLine is how an entry reads in the list: who, when, how long.
func historyLine(e history.Entry, now time.Time) (who, when string) {
	switch {
	case e.Outgoing:
		who = "→ " + e.Number
	case e.Missed():
		who = "✕ " + e.Number
	default:
		who = "← " + e.Number
	}
	if e.Name != "" && !e.Outgoing {
		who += " " + e.Name
	}
	at := e.At.In(now.Location())
	if y, m, d := at.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		when = at.Format("15:04")
	} else {
		when = at.Format("Jan 2 15:04")
	}
	switch {
	case e.Answered:
		when += fmt.Sprintf("  %d:%02d", e.Seconds/60, e.Seconds%60)
	case e.Missed():
		when += "  missed"
	default:
		when += "  failed" // the cause is in the file
	}
	return who, when
}

// showHistory opens the list of past calls; tapping one puts its number
// in the entry, ready to call.
func (g *gui) showHistory() {
	if g.history == nil {
		return
	}
	entries := g.history.Entries()
	list := widget.NewList(
		func() int { return len(entries) },
		func() fyne.CanvasObject {
			who := widget.NewLabel("")
			who.Truncation = fyne.TextTruncateEllipsis
			when := widget.NewLabel("")
			when.Alignment = fyne.TextAlignTrailing
			return container.NewBorder(nil, nil, nil, when, who)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			who, when := historyLine(entries[i], time.Now())
			b := o.(*fyne.Container)
			b.Objects[0].(*widget.Label).SetText(who)
			b.Objects[1].(*widget.Label).SetText(when)
			b.Refresh() // lay the row out again for the new widths
		})
	var d dialog.Dialog
	list.OnSelected = func(i widget.ListItemID) {
		g.number.SetText(entries[i].Number)
		d.Hide()
	}
	clearBtn := widget.NewButton("Clear", func() {
		if err := g.history.Clear(); err != nil {
			g.showError(err)
		}
		entries = nil
		list.Refresh()
	})
	empty := widget.NewLabel("No calls yet")
	empty.Hidden = len(entries) > 0
	content := container.NewBorder(empty, clearBtn, nil, nil, list)
	d = dialog.NewCustom("Call history", "Close", content, g.win)
	g.historyList = list
	d.SetOnClosed(func() { g.historyList = nil })
	d.Resize(fyne.NewSize(320, 420))
	d.Show()
}
