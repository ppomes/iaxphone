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
	"testing"
	"time"
)

// nextEvent returns the next event of call, or fails after 2 s.
func nextEvent(t *testing.T, call *Call) Event {
	t.Helper()
	select {
	case ev := <-call.Events:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
		return Event{}
	}
}

// Hold sends QUELCH with a MUSICONHOLD IE and Unhold UNQUELCH, nothing is
// sent meanwhile; Transfer sends TRANSFER with the number; the peer's
// QUELCH, UNQUELCH, CONTROL/HOLD, CONTROL/UNHOLD and TRANSFER are reported
// as events and acknowledged.
func TestHoldAndTransfer(t *testing.T) {
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	call, err := c.Call(context.Background(), "600", make(chan MediaFrame, 16))
	if err != nil {
		t.Fatal(err)
	}
	if err := call.Hold(); !errors.Is(err, ErrCallDown) && call.State() != StateUp {
		t.Errorf("Hold before answer: %v", err)
	}
	if ev := nextEvent(t, call); ev.Kind != EventAnswered {
		t.Fatalf("event %v", ev)
	}
	collectUntil(t, srv, IAXPong) // NEW ... PONG: the call is up
	if err := call.SendVoice([]byte{0xFF}); err != nil {
		t.Fatal(err)
	}
	if v := srv.next(t); v.Type != FrameVoice || v.OSeqno != 3 {
		t.Errorf("first voice frame: %s", v)
	}

	// Our hold: QUELCH asking for music, sequenced; no voice goes out
	// until UNQUELCH.
	if err := call.Hold(); err != nil {
		t.Fatal(err)
	}
	q := srv.next(t)
	if q.Type != FrameIAX || IAXSubclass(q.Subclass) != IAXQuelch || q.OSeqno != 4 {
		t.Errorf("QUELCH: %s", q)
	}
	if ies, err := q.IEs(); err != nil || !ies.Has(IEMusicOnHold) {
		t.Errorf("QUELCH IEs %v: %v", ies, err)
	}
	if !call.OnHold() || call.Held() {
		t.Errorf("OnHold %v Held %v after Hold", call.OnHold(), call.Held())
	}
	if err := call.SendVoice([]byte{0xFF}); !errors.Is(err, ErrOnHold) {
		t.Errorf("SendVoice on hold: %v", err)
	}
	if err := call.Hold(); err != nil {
		t.Errorf("Hold again: %v", err)
	}
	if err := call.Unhold(); err != nil {
		t.Fatal(err)
	}
	if u := srv.next(t); u.Type != FrameIAX || IAXSubclass(u.Subclass) != IAXUnquelch || u.OSeqno != 5 || len(u.Data) != 0 {
		t.Errorf("UNQUELCH: %s", u)
	}
	if call.OnHold() {
		t.Error("still on hold after Unhold")
	}
	if err := call.Unhold(); err != nil {
		t.Errorf("Unhold again: %v", err)
	}
	if err := call.SendVoice([]byte{0xFF}); err != nil {
		t.Errorf("SendVoice after Unhold: %v", err)
	}

	// Our transfer: TRANSFER with the number, and the context when given.
	if err := call.Transfer(""); err == nil {
		t.Error("Transfer(\"\") succeeded")
	}
	if err := call.Transfer("601@sales"); err != nil {
		t.Fatal(err)
	}
	x := srv.next(t)
	if x.Type != FrameIAX || IAXSubclass(x.Subclass) != IAXTransfer || x.OSeqno != 6 {
		t.Errorf("TRANSFER: %s", x)
	}
	if ies, err := x.IEs(); err != nil || ies.String(IECalledNumber) != "601" || ies.String(IECalledContext) != "sales" {
		t.Errorf("TRANSFER IEs %v: %v", ies, err)
	}

	// The peer's: each is acknowledged and reported; its QUELCH stops our
	// voice until its UNQUELCH.
	peer := func(typ FrameType, sub uint64, oseq uint8, ts uint32, ies IEs) {
		data, _ := ies.Encode()
		srv.sendTo(t, c.LocalAddr(), &FullFrame{SrcCall: 0x1234, DstCall: call.LocalNumber(), Timestamp: ts,
			OSeqno: oseq, ISeqno: 7, Type: typ, Subclass: sub, Data: data})
	}
	peer(FrameIAX, uint64(IAXQuelch), 5, 300, nil)
	if ev := nextEvent(t, call); ev.Kind != EventHold {
		t.Errorf("after QUELCH: %+v", ev)
	}
	eventually(t, "held", call.Held)
	if err := call.SendVoice([]byte{0xFF}); !errors.Is(err, ErrOnHold) {
		t.Errorf("SendVoice held by the peer: %v", err)
	}
	peer(FrameIAX, uint64(IAXUnquelch), 6, 400, nil)
	if ev := nextEvent(t, call); ev.Kind != EventUnhold {
		t.Errorf("after UNQUELCH: %+v", ev)
	}
	eventually(t, "unheld", func() bool { return !call.Held() })
	if err := call.SendVoice([]byte{0xFF}); err != nil {
		t.Errorf("SendVoice after the peer's UNQUELCH: %v", err)
	}
	peer(FrameControl, uint64(ControlHold), 7, 500, nil)
	if ev := nextEvent(t, call); ev.Kind != EventHold {
		t.Errorf("after CONTROL/HOLD: %+v", ev)
	}
	peer(FrameControl, uint64(ControlUnhold), 8, 600, nil)
	if ev := nextEvent(t, call); ev.Kind != EventUnhold {
		t.Errorf("after CONTROL/UNHOLD: %+v", ev)
	}
	peer(FrameIAX, uint64(IAXTransfer), 9, 700, IEs{StringIE(IECalledNumber, "777")})
	if ev := nextEvent(t, call); ev.Kind != EventTransfer || ev.Number != "777" {
		t.Errorf("after TRANSFER: %+v", ev)
	}
	for i, ts := range []uint32{300, 400, 500, 600, 700} {
		if ack := srv.next(t); IAXSubclass(ack.Subclass) != IAXAck || ack.Timestamp != ts || ack.ISeqno != uint8(6+i) {
			t.Errorf("ACK of peer frame %d: %s", i, ack)
		}
	}

	if err := call.Hangup(CauseNormalClearing); err != nil {
		t.Fatal(err)
	}
	<-call.Done()
	if err := call.Hold(); !errors.Is(err, ErrCallDown) {
		t.Errorf("Hold after hangup: %v", err)
	}
	if err := call.Transfer("601"); !errors.Is(err, ErrCallDown) {
		t.Errorf("Transfer after hangup: %v", err)
	}
}

// A lost QUELCH is retransmitted like any sequenced frame: Hold returns
// once the copy that got through is acknowledged.
func TestHoldRetransmitted(t *testing.T) {
	hurry(t)
	srv := newFakeServer(t, loseFirstCopies)
	c := dialFake(t, srv, "phone", "s3cret")
	call, err := c.Call(context.Background(), "600", make(chan MediaFrame, 16))
	if err != nil {
		t.Fatal(err)
	}
	if ev := nextEvent(t, call); ev.Kind != EventAnswered {
		t.Fatalf("event %v", ev)
	}
	collectUntil(t, srv, IAXPong)
	if err := call.Hold(); err != nil {
		t.Fatal(err)
	}
	if q := srv.next(t); IAXSubclass(q.Subclass) != IAXQuelch || !q.Retransmit {
		t.Errorf("QUELCH that got through: %s", q)
	}
	if err := call.Hangup(CauseNormalClearing); err != nil {
		t.Fatal(err)
	}
}
