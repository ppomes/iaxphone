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
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// hurry shortens the retransmission timers for the test.
func hurry(t *testing.T) {
	t.Helper()
	interval, attempts := retransmitInterval, retransmitAttempts
	retransmitInterval, retransmitAttempts = 30*time.Millisecond, 4
	t.Cleanup(func() { retransmitInterval, retransmitAttempts = interval, attempts })
}

// loseFirstCopies makes the fake server drop the first copy of every
// sequenced frame the client sends: only retransmissions get through.
func loseFirstCopies(s *fakeServer) {
	dropped := make(map[string]bool)
	s.lose = func(f *FullFrame) bool {
		if !sequenced(f) {
			return false
		}
		key := fmt.Sprintf("%d/%d/%x", f.SrcCall, f.OSeqno, f.Data)
		if dropped[key] {
			return false
		}
		dropped[key] = true
		return true
	}
}

// eventually polls cond for up to 2 s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Every sequenced frame lost on its way to the server is sent again until
// it gets there: the call goes through on a link that loses the first copy
// of everything, and so do the digits and the HANGUP.
func TestRetransmitLostFrames(t *testing.T) {
	hurry(t)
	srv := newFakeServer(t, loseFirstCopies)
	c := dialFake(t, srv, "phone", "s3cret")

	start := time.Now()
	call, err := c.Call(context.Background(), "600", make(chan MediaFrame, 16))
	if err != nil {
		t.Fatal(err)
	}
	if ev := <-call.Events; ev.Kind != EventAnswered {
		t.Fatalf("event %v", ev)
	}
	if took := time.Since(start); took < 3*retransmitInterval {
		t.Errorf("call set up in %s, less than three retransmissions", took)
	}
	frames := collectUntil(t, srv, IAXPong)
	// NEW (R), NEW+token (R), AUTHREP (R), then the ACKs and the PONG (R
	// too: the first one was lost like the rest); the first copies never
	// reached the server.
	var resent []string
	for _, f := range frames {
		if f.Retransmit {
			resent = append(resent, frameName(f))
		}
		if sequenced(f) && !f.Retransmit {
			t.Errorf("first copy got through: %s", f)
		}
	}
	if got := strings.Join(resent, " "); got != "NEW NEW AUTHREP PONG" {
		t.Errorf("retransmitted frames: %s", got)
	}

	if err := call.SendDTMF('1'); err != nil {
		t.Fatal(err)
	}
	begin, end := srv.next(t), srv.next(t)
	if begin.Type != FrameDTMFBegin || !begin.Retransmit || end.Type != FrameDTMFEnd || !end.Retransmit {
		t.Errorf("DTMF: %s, %s", begin, end)
	}

	// HANGUP lost too: Hangup returns once the retransmission is acked and
	// the call number is then released.
	if err := call.Hangup(CauseNormalClearing); err != nil {
		t.Fatal(err)
	}
	if hup := srv.next(t); IAXSubclass(hup.Subclass) != IAXHangup || !hup.Retransmit {
		t.Errorf("HANGUP: %s", hup)
	}
	eventually(t, "call release", func() bool { return c.lookup(call.LocalNumber()) == nil })
}

// An incoming call on the same lossy link: ACCEPT, RINGING and ANSWER are
// ours to retransmit.
func TestRetransmitLostIncoming(t *testing.T) {
	hurry(t)
	srv := newFakeServer(t, loseFirstCopies)
	c := dialFake(t, srv, "phone", "")
	srv.originate(t, c.LocalAddr(), "s")
	call := waitIncoming(t, c)
	// Their timers fire together: either order.
	acc, ring := srv.next(t), srv.next(t)
	if acc.Type == FrameControl {
		acc, ring = ring, acc
	}
	if IAXSubclass(acc.Subclass) != IAXAccept || !acc.Retransmit || ControlSubclass(ring.Subclass) != ControlRinging || !ring.Retransmit {
		t.Errorf("ACCEPT %s, RINGING %s", acc, ring)
	}
	if err := call.Answer(make(chan MediaFrame, 4)); err != nil {
		t.Fatal(err)
	}
	if ans := srv.next(t); ControlSubclass(ans.Subclass) != ControlAnswer || !ans.Retransmit {
		t.Errorf("ANSWER: %s", ans)
	}
	if err := call.Hangup(CauseNormalClearing); err != nil {
		t.Fatal(err)
	}
}

// When the server's ACK is lost, the client sends the frame again and the
// server acknowledges it again rather than acting on it twice.
func TestLostAckIsAnsweredAgain(t *testing.T) {
	hurry(t)
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	call, err := c.Call(context.Background(), "600", make(chan MediaFrame, 16))
	if err != nil {
		t.Fatal(err)
	}
	<-call.Events // answered
	collectUntil(t, srv, IAXPong)

	srv.loseAcks.Store(1)
	if err := call.SendDTMF('2'); err != nil {
		t.Fatal(err)
	}
	first, again, end := srv.next(t), srv.next(t), srv.next(t)
	if first.Type != FrameDTMFBegin || first.Retransmit || again.Type != FrameDTMFBegin || !again.Retransmit ||
		again.OSeqno != first.OSeqno || end.Type != FrameDTMFEnd || end.Retransmit {
		t.Errorf("frames: %s, %s, %s", first, again, end)
	}
}

// A frame the peer sends twice (its retransmission of something we did
// get) is acknowledged again but acted on once.
func TestRepeatedFrameActedOnOnce(t *testing.T) {
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	call, err := c.Call(context.Background(), "600", make(chan MediaFrame, 16))
	if err != nil {
		t.Fatal(err)
	}
	<-call.Events
	collectUntil(t, srv, IAXPong) // the server's frames 0..4 are in

	end := &FullFrame{DstCall: call.LocalNumber(), Timestamp: 300, OSeqno: 5, ISeqno: 3, Type: FrameDTMFEnd, Subclass: '9'}
	srv.sendTo(t, c.LocalAddr(), end)
	repeat := *end
	repeat.Retransmit = true
	srv.sendTo(t, c.LocalAddr(), &repeat)
	if ev := <-call.Events; ev.Kind != EventDTMF || ev.Digit != '9' {
		t.Errorf("event %+v", ev)
	}
	for i := 0; i < 2; i++ {
		if ack := srv.next(t); IAXSubclass(ack.Subclass) != IAXAck || ack.ISeqno != 6 || ack.Timestamp != 300 {
			t.Errorf("ACK %d: %s", i, ack)
		}
	}
	select {
	case ev := <-call.Events:
		t.Errorf("second event %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// A frame arriving ahead of one we have not received is held back with a
// VNAK naming the missing one; the peer then sends both in order.
func TestGapAsksForVNAK(t *testing.T) {
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	call, err := c.Call(context.Background(), "600", make(chan MediaFrame, 16))
	if err != nil {
		t.Fatal(err)
	}
	<-call.Events
	collectUntil(t, srv, IAXPong)

	key := func(typ FrameType, oseq uint8, ts uint32) {
		srv.sendTo(t, c.LocalAddr(), &FullFrame{DstCall: call.LocalNumber(), Timestamp: ts, OSeqno: oseq, ISeqno: 3, Type: typ, Subclass: '8'})
	}
	key(FrameDTMFEnd, 6, 400) // frame 5 is missing
	if vnak := srv.next(t); IAXSubclass(vnak.Subclass) != IAXVnak || vnak.ISeqno != 5 {
		t.Fatalf("expected VNAK for 5, got %s", vnak)
	}
	select {
	case ev := <-call.Events:
		t.Errorf("event before the gap was filled: %+v", ev)
	case <-time.After(30 * time.Millisecond):
	}
	key(FrameDTMFBegin, 5, 300)
	key(FrameDTMFEnd, 6, 400)
	if ev := <-call.Events; ev.Kind != EventDTMF || ev.Digit != '8' {
		t.Errorf("event %+v", ev)
	}
	for _, want := range []uint8{6, 7} {
		if ack := srv.next(t); IAXSubclass(ack.Subclass) != IAXAck || ack.ISeqno != want {
			t.Errorf("ACK: %s, want ISeqno %d", ack, want)
		}
	}
	select {
	case ev := <-call.Events:
		t.Errorf("extra event %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// A VNAK from the peer makes us send the frames it names again at once,
// without waiting for the retransmission timer.
func TestPeerVNAK(t *testing.T) {
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	call, err := c.Call(context.Background(), "600", make(chan MediaFrame, 16))
	if err != nil {
		t.Fatal(err)
	}
	<-call.Events
	collectUntil(t, srv, IAXPong)

	srv.loseAcks.Store(1)
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- call.DTMFBegin('3') }()
	begin := srv.next(t)
	if begin.Type != FrameDTMFBegin || begin.Retransmit {
		t.Fatalf("DTMF_BEGIN: %s", begin)
	}
	srv.sendTo(t, c.LocalAddr(), &FullFrame{DstCall: call.LocalNumber(), Timestamp: 500, OSeqno: 5, ISeqno: begin.OSeqno,
		Type: FrameIAX, Subclass: uint64(IAXVnak)})
	again := srv.next(t)
	if again.Type != FrameDTMFBegin || !again.Retransmit || again.OSeqno != begin.OSeqno {
		t.Fatalf("after VNAK: %s", again)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > retransmitInterval/2 {
		t.Errorf("DTMF_BEGIN acknowledged after %s: the timer did it, not the VNAK", took)
	}
	if err := call.DTMFEnd('3'); err != nil {
		t.Fatal(err)
	}
}

// A server that stops answering ends the call after the retransmissions:
// the caller's frame comes back with the error, the call reports a hangup
// with "no reply" and a final HANGUP goes out in case the server is only
// deaf, not gone.
func TestDeafServerEndsCall(t *testing.T) {
	hurry(t)
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	call, err := c.Call(context.Background(), "600", make(chan MediaFrame, 16))
	if err != nil {
		t.Fatal(err)
	}
	<-call.Events
	collectUntil(t, srv, IAXPong)

	srv.deaf.Store(true)
	start := time.Now()
	err = call.SendDTMF('4')
	if err == nil || !strings.Contains(err.Error(), "no reply to DTMF_BEGIN") || !strings.Contains(err.Error(), "after 4 attempts") {
		t.Errorf("SendDTMF: %v", err)
	}
	if took := time.Since(start); took < 3*retransmitInterval || took > 10*retransmitInterval {
		t.Errorf("gave up after %s", took)
	}
	var last Event
	for ev := range call.Events {
		last = ev
	}
	if last.Kind != EventHangup || last.Cause != "no reply" || last.CauseCode != CauseLocal {
		t.Errorf("last event %+v", last)
	}
	<-call.Done()
	if call.Err() == nil || call.State() != StateDown {
		t.Errorf("err=%v state=%s", call.Err(), call.State())
	}
	var sent []string
	for len(sent) < 5 {
		f := srv.next(t)
		sent = append(sent, fmt.Sprintf("%s/%v", frameName(f), f.Retransmit))
	}
	hup := sent[4]
	if got := strings.Join(sent, " "); got != "DTMF_BEGIN/false DTMF_BEGIN/true DTMF_BEGIN/true DTMF_BEGIN/true HANGUP/false" {
		t.Errorf("frames: %s", got)
	}
	_ = hup
	eventually(t, "call release", func() bool { return c.lookup(call.LocalNumber()) == nil })
}

// The final HANGUP of a deaf server carries the cause chan_iax2 uses.
func TestDeafServerHangupCause(t *testing.T) {
	hurry(t)
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	call, err := c.Call(context.Background(), "600", make(chan MediaFrame, 16))
	if err != nil {
		t.Fatal(err)
	}
	<-call.Events
	collectUntil(t, srv, IAXPong)
	srv.deaf.Store(true)
	_ = call.SendDTMF('4')
	frames := collectUntil(t, srv, IAXHangup)
	ies, _ := frames[len(frames)-1].IEs()
	if code, _ := ies.Uint8(IECauseCode); code != CauseOutOfOrder || ies.String(IECause) != "Destination out of order" {
		t.Errorf("HANGUP IEs: %v", ies)
	}
}

// A POKE nobody answers gives up after the retransmissions.
func TestPingGivesUp(t *testing.T) {
	hurry(t)
	dead, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer dead.Close() //nolint:errcheck
	c, err := Dial(context.Background(), Config{Server: dead.LocalAddr().String(), LocalAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck
	_, err = c.Ping(context.Background())
	want := "iax2: no reply to POKE from " + dead.LocalAddr().String() + " after 4 attempts"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}
	c.mu.Lock()
	n := len(c.calls)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("%d calls still allocated", n)
	}
}

// A full frame for a call we do not have is answered with INVAL, so the
// peer stops retransmitting; a stray ACK is just dropped.
func TestUnknownCallGetsINVAL(t *testing.T) {
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	srv.sendTo(t, c.LocalAddr(), &FullFrame{DstCall: 0x7000, Timestamp: 10, OSeqno: 2, ISeqno: 1, Type: FrameIAX, Subclass: uint64(IAXPing)})
	inval := srv.next(t)
	if IAXSubclass(inval.Subclass) != IAXInval || inval.SrcCall != 0x7000 || inval.DstCall != 0x1234 {
		t.Errorf("reply: %s", inval)
	}
	srv.sendTo(t, c.LocalAddr(), &FullFrame{DstCall: 0x7000, Timestamp: 10, OSeqno: 2, ISeqno: 1, Type: FrameIAX, Subclass: uint64(IAXAck)})
	select {
	case f := <-srv.frames:
		t.Errorf("reply to a stray ACK: %s", f)
	case <-time.After(50 * time.Millisecond):
	}
}

// INVAL from the peer during a call ends it; the frames we had in flight
// are not retransmitted to a call that no longer exists.
func TestINVALEndsCall(t *testing.T) {
	hurry(t)
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	call, err := c.Call(context.Background(), "600", make(chan MediaFrame, 16))
	if err != nil {
		t.Fatal(err)
	}
	<-call.Events
	collectUntil(t, srv, IAXPong)
	srv.deaf.Store(true)
	go func() { _ = call.SendDTMF('5') }()
	srv.next(t) // DTMF_BEGIN, unanswered
	srv.sendTo(t, c.LocalAddr(), &FullFrame{DstCall: call.LocalNumber(), Type: FrameIAX, Subclass: uint64(IAXInval)})
	last := Event{}
	for ev := range call.Events {
		last = ev
	}
	if last.Kind != EventHangup || last.Cause != "INVAL" {
		t.Errorf("last event %+v", last)
	}
	if errors.Is(call.Err(), errNoAck) {
		t.Error("call failed rather than ended")
	}
	select {
	case f := <-srv.frames:
		if f.Retransmit {
			t.Errorf("retransmitted after INVAL: %s", f)
		}
	case <-time.After(4 * retransmitInterval):
	}
}
