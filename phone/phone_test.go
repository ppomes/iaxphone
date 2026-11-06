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

package phone

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ppomes/iaxphone/audio"
	"github.com/ppomes/iaxphone/iax2"
	"github.com/ppomes/iaxphone/internal/fakeiax"
)

func startPhone(t *testing.T, srv *fakeiax.Server, register bool) *Phone {
	t.Helper()
	p := New(Config{
		Server: srv.Addr(), LocalAddr: "127.0.0.1:0",
		Username: "phone", Register: register,
		OpenDevice: func() (audio.Device, error) { return &audio.NullDevice{}, nil },
	})
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() }) //nolint:errcheck
	return p
}

func next(t *testing.T, p *Phone, want Kind) Event {
	t.Helper()
	for {
		select {
		case ev, ok := <-p.Events():
			if !ok {
				t.Fatalf("Events closed while waiting for %s", want)
			}
			if ev.Kind == want {
				return ev
			}
			t.Logf("skipping event %s", ev.Kind)
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for %s", want)
		}
	}
}

func TestRegisterReportsState(t *testing.T) {
	srv := fakeiax.New(t)
	p := startPhone(t, srv, true)
	if st := p.Registration().State; st != RegPending {
		t.Errorf("before: %s", st)
	}
	ev := next(t, p, EventRegistration)
	if ev.Registration.State != RegOK || ev.Registration.Refresh != 60*time.Second || ev.Registration.Err != nil {
		t.Errorf("registration %+v", ev.Registration)
	}
	if st := p.Registration().State; st != RegOK {
		t.Errorf("after: %s", st)
	}
}

func TestNoRegistrationByDefault(t *testing.T) {
	srv := fakeiax.New(t)
	p := startPhone(t, srv, false)
	if st := p.Registration().State; st != RegNone {
		t.Errorf("state %s", st)
	}
	time.Sleep(100 * time.Millisecond)
	if n := srv.Registrations(); n != 0 {
		t.Errorf("%d REGREQ sent", n)
	}
}

func TestOutgoingCallLifecycle(t *testing.T) {
	srv := fakeiax.New(t)
	p := startPhone(t, srv, false)
	if err := p.Dial("600"); err != nil {
		t.Fatal(err)
	}
	if err := p.Dial("601"); !errors.Is(err, ErrBusy) {
		t.Errorf("second Dial: %v", err)
	}
	ev := next(t, p, EventDialing)
	if !ev.Call.Outgoing || ev.Call.Number != "600" || ev.Call.State != CallDialing {
		t.Errorf("dialing %+v", ev.Call)
	}
	ev = next(t, p, EventRemoteRinging)
	if ev.Call.State != CallDialing {
		t.Errorf("remote ringing %+v", ev.Call)
	}
	ev = next(t, p, EventAnswered)
	if ev.Call.State != CallActive || ev.Call.Format != iax2.FormatULAW || ev.Call.Answered.IsZero() {
		t.Errorf("answered %+v", ev.Call)
	}
	// The null device ticks every 20 ms: audio must flow both ways.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := p.Stats()
		if st.Sent >= 3 && st.Jitter.Received >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := p.Stats(); st.Sent < 3 || st.Jitter.Received < 1 {
		t.Errorf("stats %+v", st)
	}
	p.SetMuted(true)
	if !p.Muted() {
		t.Error("not muted")
	}
	if err := p.Hangup(); err != nil {
		t.Fatal(err)
	}
	ev = next(t, p, EventEnded)
	if !ev.Call.HungUpByUs || ev.Call.Err != nil || ev.Call.State != CallEnded || ev.Call.Stats.Sent < 3 {
		t.Errorf("ended %+v", ev.Call)
	}
	if causes := srv.HangupCauses(); len(causes) != 1 || causes[0] != "Normal Clearing" {
		t.Errorf("server saw hangups %q", causes)
	}
	if _, ok := p.Call(); ok {
		t.Error("call still current")
	}
	if err := p.Hangup(); err != nil {
		t.Errorf("Hangup without a call: %v", err)
	}
}

func TestOutgoingCallRejected(t *testing.T) {
	srv := fakeiax.New(t)
	p := startPhone(t, srv, false)
	if err := p.Dial("404"); err != nil {
		t.Fatal(err)
	}
	ev := next(t, p, EventEnded)
	if ev.Call.Err == nil || ev.Call.HungUpByUs || ev.Call.Cause == "" {
		t.Errorf("ended %+v", ev.Call)
	}
	if _, ok := p.Call(); ok {
		t.Error("call still current")
	}
}

func TestIncomingCallAnswerAndPeerHangup(t *testing.T) {
	srv := fakeiax.New(t)
	p := startPhone(t, srv, true)
	next(t, p, EventRegistration)
	if err := p.Answer(); !errors.Is(err, ErrNotRinging) {
		t.Errorf("Answer without a call: %v", err)
	}
	srv.Originate(p.LocalAddr())
	ev := next(t, p, EventIncoming)
	if ev.Call.Outgoing || ev.Call.Number != "1001" || ev.Call.Name != "Asterisk" || ev.Call.State != CallRinging {
		t.Errorf("incoming %+v", ev.Call)
	}
	// The ring tone plays on the device while the call waits.
	deadline := time.Now().Add(2 * time.Second)
	for p.Stats().Tone < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if st := p.Stats(); st.Tone < 3 || st.Played != 0 {
		t.Errorf("while ringing: %+v", st)
	}
	// A second caller is answered busy.
	srv.Originate(p.LocalAddr())
	deadline = time.Now().Add(2 * time.Second)
	for len(srv.HangupCauses()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if causes := srv.HangupCauses(); len(causes) != 1 || causes[0] != "User busy" {
		t.Errorf("second caller: %q", causes)
	}
	if err := p.Answer(); err != nil {
		t.Fatal(err)
	}
	ev = next(t, p, EventAnswered)
	if ev.Call.State != CallActive {
		t.Errorf("answered %+v", ev.Call)
	}
	info, ok := p.Call()
	if !ok || info.State != CallActive {
		t.Errorf("current %+v %v", info, ok)
	}
	srv.Hangup()
	ev = next(t, p, EventEnded)
	if ev.Call.HungUpByUs || ev.Call.Cause != "Normal Clearing (16)" || ev.Call.Err != nil {
		t.Errorf("ended %+v", ev.Call)
	}
}

func TestDeclineIncoming(t *testing.T) {
	srv := fakeiax.New(t)
	p := startPhone(t, srv, true)
	next(t, p, EventRegistration)
	srv.Originate(p.LocalAddr())
	next(t, p, EventIncoming)
	if err := p.Hangup(); err != nil {
		t.Fatal(err)
	}
	ev := next(t, p, EventEnded)
	if !ev.Call.HungUpByUs {
		t.Errorf("ended %+v", ev.Call)
	}
	if causes := srv.HangupCauses(); len(causes) != 1 || causes[0] != "Call Rejected" {
		t.Errorf("server saw %q", causes)
	}
}

func TestCloseEndsCallAndEvents(t *testing.T) {
	srv := fakeiax.New(t)
	p := startPhone(t, srv, true)
	next(t, p, EventRegistration)
	if err := p.Dial("600"); err != nil {
		t.Fatal(err)
	}
	next(t, p, EventAnswered)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	var ended bool
	for ev := range p.Events() {
		if ev.Kind == EventEnded {
			ended = true
		}
	}
	if !ended {
		t.Error("no EventEnded before Events closed")
	}
	if err := p.Dial("600"); !errors.Is(err, ErrNotStarted) && err == nil {
		t.Errorf("Dial after Close: %v", err)
	}
}

// countingDevice counts how often the phone opens and closes the device.
type countingDevice struct {
	audio.NullDevice
	opens, closes *atomic.Int32
}

func (d *countingDevice) Close() error {
	d.closes.Add(1)
	return d.NullDevice.Close()
}

func TestKeys(t *testing.T) {
	old := KeyIdleClose
	KeyIdleClose = 100 * time.Millisecond
	defer func() { KeyIdleClose = old }()
	srv := fakeiax.New(t)
	var opens, closes atomic.Int32
	p := New(Config{
		Server: srv.Addr(), LocalAddr: "127.0.0.1:0", Username: "phone", Register: true,
		OpenDevice: func() (audio.Device, error) {
			opens.Add(1)
			return &countingDevice{opens: &opens, closes: &closes}, nil
		},
	})
	if err := p.PressKey('1'); !errors.Is(err, ErrNotStarted) {
		t.Errorf("PressKey before Start: %v", err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() }) //nolint:errcheck
	next(t, p, EventRegistration)
	if err := p.PressKey('x'); !errors.Is(err, iax2.ErrBadDigit) {
		t.Errorf("PressKey('x'): %v", err)
	}

	// Idle: the key sounds on a device opened for it, which closes
	// KeyIdleClose after the release; the level meter sees the tone.
	t0 := time.Now()
	if err := p.PressKey('1'); err != nil {
		t.Fatal(err)
	}
	p.ReleaseKey()
	p.ReleaseKey() // harmless twice
	time.Sleep(50 * time.Millisecond)
	if _, out := p.Levels(); math.IsInf(out, -1) {
		t.Error("no tone level while the key sounds")
	}
	for opens.Load() != 1 || closes.Load() != 1 {
		if time.Since(t0) > 2*time.Second {
			t.Fatalf("opens %d closes %d", opens.Load(), closes.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if held := time.Since(t0); held < MinKeyDuration+KeyIdleClose {
		t.Errorf("device closed after %s", held)
	}
	// Two keys in a row share the device.
	if err := p.PressKey('2'); err != nil {
		t.Fatal(err)
	}
	if err := p.PressKey('3'); err != nil { // releases 2
		t.Fatal(err)
	}
	p.ReleaseKey()
	if opens.Load() != 2 {
		t.Errorf("opens %d", opens.Load())
	}

	// A ringing call takes no keys.
	srv.Originate(p.LocalAddr())
	next(t, p, EventIncoming)
	if err := p.PressKey('1'); !errors.Is(err, ErrNotActive) {
		t.Errorf("PressKey while ringing: %v", err)
	}
	if err := p.Hangup(); err != nil {
		t.Fatal(err)
	}
	next(t, p, EventEnded)

	// In a call: DTMF_BEGIN on the press, DTMF_END no sooner than
	// MinKeyDuration, on the release.
	if err := p.Dial("600"); err != nil {
		t.Fatal(err)
	}
	next(t, p, EventAnswered)
	t0 = time.Now()
	if err := p.PressKey('5'); err != nil {
		t.Fatal(err)
	}
	p.ReleaseKey()
	if got := srv.Digits(); got != "" {
		t.Errorf("digit ended at once: %q", got)
	}
	for srv.Digits() != "5" {
		if time.Since(t0) > 2*time.Second {
			t.Fatalf("server received %q", srv.Digits())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if held := time.Since(t0); held < MinKeyDuration {
		t.Errorf("key ended after %s", held)
	}
	// A key still held when the call ends is dropped quietly.
	if err := p.PressKey('6'); err != nil {
		t.Fatal(err)
	}
	if err := p.Hangup(); err != nil {
		t.Fatal(err)
	}
	next(t, p, EventEnded)
	p.ReleaseKey()
	if err := p.PressKey('7'); err != nil { // idle again: the player
		t.Fatal(err)
	}
	p.ReleaseKey()
	if opens.Load() != 5 { // player, player, ring, call, player
		t.Errorf("opens %d", opens.Load())
	}
}

func TestDTMF(t *testing.T) {
	srv := fakeiax.New(t)
	p := startPhone(t, srv, false)
	if err := p.SendDTMF('1'); !errors.Is(err, ErrNoCall) {
		t.Errorf("SendDTMF without a call: %v", err)
	}
	if err := p.Dial("600"); err != nil {
		t.Fatal(err)
	}
	next(t, p, EventAnswered)
	for _, d := range []byte("12#") {
		if err := p.SendDTMF(d); err != nil {
			t.Fatalf("SendDTMF(%c): %v", d, err)
		}
	}
	if got := srv.Digits(); got != "12#" {
		t.Errorf("server received %q", got)
	}
	srv.PressKey('5')
	ev := next(t, p, EventDTMF)
	if ev.Digit != '5' || ev.Call.State != CallActive {
		t.Errorf("dtmf event %+v", ev)
	}
	if err := p.Hangup(); err != nil {
		t.Fatal(err)
	}
	next(t, p, EventEnded)
	if err := p.SendDTMF('1'); !errors.Is(err, ErrNoCall) {
		t.Errorf("SendDTMF after hangup: %v", err)
	}
}

// The peer packetises as it likes: 40 ms and 30 ms frames must play
// through, nothing lost or dropped, the way a 20 ms stream would.
func TestPeerFrameSizes(t *testing.T) {
	srv := fakeiax.New(t)
	p := startPhone(t, srv, false)
	if err := p.Dial("600"); err != nil {
		t.Fatal(err)
	}
	next(t, p, EventDialing)
	next(t, p, EventRemoteRinging)
	next(t, p, EventAnswered)
	srv.SendAudio(40, 10) // 400 ms
	srv.SendAudio(30, 10) // 300 ms
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && p.Stats().Jitter.Received < 21 { // plus the server's first frame
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // let the queue play out
	st := p.Stats()
	if st.Jitter.Received != 21 || st.Jitter.Lost != 0 || st.Jitter.Dropped != 0 || st.Jitter.Late != 0 {
		t.Errorf("jitter stats %+v", st.Jitter)
	}
	// 720 ms of audio at 20 ms a callback.
	if st.Played < 30 || st.Played > 40 {
		t.Errorf("played %d callbacks, want 36 or so (stats %+v)", st.Played, st)
	}
	if err := p.Hangup(); err != nil {
		t.Fatal(err)
	}
	next(t, p, EventEnded)
}

// A link that loses the first copy of every frame: the call, the digits
// and the hangup all get through on their retransmissions, which the
// server sees with the retransmission bit set.
func TestLossyLink(t *testing.T) {
	srv := fakeiax.New(t)
	srv.Lossy(true)
	p := startPhone(t, srv, false)
	start := time.Now()
	if err := p.Dial("600"); err != nil {
		t.Fatal(err)
	}
	next(t, p, EventAnswered)
	if took := time.Since(start); took < 500*time.Millisecond {
		t.Errorf("answered after %s: the NEW got through first time", took)
	}
	if err := p.SendDTMF('1'); err != nil {
		t.Fatal(err)
	}
	if got := srv.Digits(); got != "1" {
		t.Errorf("server received %q", got)
	}
	if err := p.Hangup(); err != nil {
		t.Fatal(err)
	}
	ev := next(t, p, EventEnded)
	if !ev.Call.HungUpByUs || ev.Call.Err != nil || ev.Call.Cause != "hung up" {
		t.Errorf("ended %+v", ev.Call)
	}
	if causes := srv.HangupCauses(); len(causes) != 1 || causes[0] != "Normal Clearing" {
		t.Errorf("server saw hangups %q", causes)
	}
	// NEW, DTMF_BEGIN, DTMF_END, HANGUP: each got there on its second copy.
	if n := srv.Retransmitted(); n < 4 {
		t.Errorf("server saw %d retransmitted frames", n)
	}
}

// A server that stops answering mid-call: the digit comes back with an
// error and the call ends on its own with the cause "no reply".
func TestServerGoesDeaf(t *testing.T) {
	srv := fakeiax.New(t)
	p := startPhone(t, srv, false)
	if err := p.Dial("600"); err != nil {
		t.Fatal(err)
	}
	next(t, p, EventAnswered)
	srv.Deaf(true)
	start := time.Now()
	if err := p.SendDTMF('1'); err == nil || !strings.Contains(err.Error(), "no reply to DTMF_BEGIN") {
		t.Errorf("SendDTMF: %v", err)
	}
	deadline := time.Now().Add(6 * time.Second)
	var ev Event
	for {
		select {
		case e := <-p.Events():
			if e.Kind == EventEnded {
				ev = e
			}
		case <-time.After(time.Until(deadline)):
			t.Fatal("timeout waiting for ended")
		}
		if ev.Kind == EventEnded {
			break
		}
	}
	if took := time.Since(start); took < 3*time.Second || took > 6*time.Second {
		t.Errorf("call ended after %s", took)
	}
	if ev.Call.HungUpByUs || ev.Call.Cause != "no reply" || ev.Call.Err != nil || ev.Call.State != CallEnded {
		t.Errorf("ended %+v", ev.Call)
	}
	if _, ok := p.Call(); ok {
		t.Error("call still current")
	}
	srv.Deaf(false)
	if err := p.Dial("600"); err != nil {
		t.Fatalf("Dial after the failure: %v", err)
	}
	next(t, p, EventAnswered)
}

// Hold stops our audio and tells the server, Unhold resumes; the peer's
// hold and transfer request are reported; our transfer is followed by the
// server's hangup.
func TestHoldAndTransfer(t *testing.T) {
	srv := fakeiax.New(t)
	p := startPhone(t, srv, false)
	if err := p.Hold(); !errors.Is(err, ErrNoCall) {
		t.Errorf("Hold without a call: %v", err)
	}
	if err := p.Dial("600"); err != nil {
		t.Fatal(err)
	}
	next(t, p, EventAnswered)
	deadline := time.Now().Add(2 * time.Second)
	for p.Stats().Sent < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if err := p.Hold(); err != nil {
		t.Fatal(err)
	}
	ev := next(t, p, EventHold)
	if !ev.Call.OnHold || ev.Call.Held || ev.Call.State != CallActive {
		t.Errorf("hold %+v", ev.Call)
	}
	if !srv.Held() {
		t.Error("server not held")
	}
	time.Sleep(50 * time.Millisecond)
	sent := p.Stats().Sent
	time.Sleep(100 * time.Millisecond)
	if got := p.Stats().Sent; got != sent {
		t.Errorf("sent %d frames while on hold", got-sent)
	}
	if ci, _ := p.Call(); !ci.OnHold {
		t.Errorf("Call() %+v", ci)
	}
	if err := p.Unhold(); err != nil {
		t.Fatal(err)
	}
	ev = next(t, p, EventUnhold)
	if ev.Call.OnHold || srv.Held() {
		t.Errorf("unhold %+v, server held %v", ev.Call, srv.Held())
	}
	deadline = time.Now().Add(2 * time.Second)
	for p.Stats().Sent == sent && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if p.Stats().Sent == sent {
		t.Error("nothing sent after Unhold")
	}

	srv.Hold(true)
	ev = next(t, p, EventHold)
	if !ev.Call.Held || ev.Call.OnHold {
		t.Errorf("held by the peer %+v", ev.Call)
	}
	srv.Hold(false)
	if ev = next(t, p, EventUnhold); ev.Call.Held {
		t.Errorf("unheld by the peer %+v", ev.Call)
	}
	srv.Transfer("777")
	if ev = next(t, p, EventTransfer); ev.Number != "777" {
		t.Errorf("transfer request %+v", ev)
	}

	if err := p.Transfer("601"); err != nil {
		t.Fatal(err)
	}
	ev = next(t, p, EventEnded)
	if ev.Call.TransferredTo != "601" || ev.Call.HungUpByUs || ev.Call.Err != nil || ev.Call.Cause != "Normal Clearing (16)" {
		t.Errorf("ended %+v", ev.Call)
	}
	if got := srv.Transfers(); len(got) != 1 || got[0] != "601" {
		t.Errorf("server saw transfers %q", got)
	}
	if err := p.Transfer("601"); !errors.Is(err, ErrNoCall) {
		t.Errorf("Transfer without a call: %v", err)
	}
}
