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
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestDialAllocatesCallNumbers(t *testing.T) {
	c, err := Dial(context.Background(), Config{Server: "127.0.0.1", LocalAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck

	a, b := c.newCall(), c.newCall()
	if a.localNo == 0 || a.localNo == b.localNo {
		t.Errorf("call numbers %d and %d", a.localNo, b.localNo)
	}
	if c.lookup(a.localNo) != a {
		t.Error("lookup failed")
	}
	if a.State() != StateIdle || a.State().String() != "idle" {
		t.Errorf("state = %v", a.State())
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := (&Config{Server: "pbx.example"}).withDefaults()
	if cfg.Server != "pbx.example:4569" {
		t.Errorf("Server = %q", cfg.Server)
	}
	if cfg.Capability != FormatULAW|FormatALAW || cfg.Preferred != FormatULAW {
		t.Errorf("codecs = %s / %s", cfg.Capability, cfg.Preferred)
	}
	cfg = (&Config{Server: "[::1]:4570"}).withDefaults()
	if cfg.Server != "[::1]:4570" {
		t.Errorf("Server = %q", cfg.Server)
	}
}

// fakeServer answers POKE and REGREQ/REGREL the way chan_iax2 does and
// records the frames it receives.
type fakeServer struct {
	conn   *net.UDPConn
	frames chan *FullFrame

	secret       string
	requireToken bool
	challenge    string
	// answerDelay: time between ACCEPT and ANSWER; hangupAfter: server hangs
	// up this long after ANSWER (0: never).
	answerDelay time.Duration
	hangupAfter time.Duration
	// wrongSecret: answer the client's AUTHREQ with a bad MD5 response.
	wrongSecret bool
}

// originate sends a NEW to the client, as chan_iax2 does for Dial(IAX2/peer).
func (s *fakeServer) originate(t *testing.T, to net.Addr, called string) {
	t.Helper()
	ies := IEs{
		Uint16IE(IEVersion, ProtocolVersion),
		StringIE(IECalledNumber, called),
		StringIE(IECallingNumber, "1001"),
		StringIE(IECallingName, "Asterisk"),
		StringIE(IEUsername, "phone"),
		Uint32IE(IEFormat, uint32(FormatALAW)),
		Uint32IE(IECapability, uint32(FormatULAW|FormatALAW)),
		Format64IE(IEFormat2, FormatALAW),
		Format64IE(IECapability2, FormatULAW|FormatALAW),
		EmptyIE(IECallToken),
	}
	data, err := ies.Encode()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Encode(&FullFrame{SrcCall: 0x1234, Timestamp: 5, Type: FrameIAX, Subclass: uint64(IAXNew), Data: data})
	if _, err := s.conn.WriteToUDP(b, to.(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
}

// sendTo writes a full frame to the client as the call 0x1234.
func (s *fakeServer) sendTo(t *testing.T, to net.Addr, f *FullFrame) {
	t.Helper()
	f.SrcCall = 0x1234
	b, _ := Encode(f)
	if _, err := s.conn.WriteToUDP(b, to.(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
}

func newFakeServer(t *testing.T, opts ...func(*fakeServer)) *fakeServer {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{conn: conn, frames: make(chan *FullFrame, 16), secret: "s3cret", challenge: "424242", requireToken: true}
	for _, o := range opts {
		o(s)
	}
	t.Cleanup(func() { conn.Close() }) //nolint:errcheck
	go s.loop()
	return s
}

func (s *fakeServer) loop() {
	buf := make([]byte, 1500)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		f, err := Parse(append([]byte(nil), buf[:n]...))
		if err != nil {
			continue
		}
		full, ok := f.(*FullFrame)
		if !ok {
			continue
		}
		s.frames <- full
		if full.Retransmit {
			continue
		}
		reply := func(sub IAXSubclass, oseq uint8, ies IEs) {
			data, _ := ies.Encode()
			f := &FullFrame{
				SrcCall: 0x1234, DstCall: full.SrcCall, Timestamp: full.Timestamp,
				OSeqno: oseq, ISeqno: full.OSeqno + 1, Type: FrameIAX, Subclass: uint64(sub), Data: data,
			}
			b, _ := Encode(f)
			_, _ = s.conn.WriteToUDP(b, from)
		}
		if full.Type == FrameControl || full.Type == FrameDTMFBegin || full.Type == FrameDTMFEnd {
			reply(IAXAck, full.ISeqno, nil) // RINGING, ANSWER of a call we originated, DTMF
			continue
		}
		if full.Type != FrameIAX {
			continue
		}
		switch IAXSubclass(full.Subclass) {
		case IAXAuthReq: // the client challenges a call we originated
			ies, _ := full.IEs()
			secret := s.secret
			if s.wrongSecret {
				secret = "wrong"
			}
			reply(IAXAuthRep, 1, IEs{StringIE(IEMD5Result, MD5Response(ies.String(IEChallenge), secret))})
		case IAXAccept:
			reply(IAXAck, full.ISeqno, nil)
		case IAXPoke:
			reply(IAXPong, 0, nil)
		case IAXNew, IAXAuthRep:
			s.handleCall(full, from, reply)
		case IAXHangup:
			reply(IAXAck, full.ISeqno, nil) // ACK: oseqno is whatever we are at
		case IAXRegReq, IAXRegRel:
			ies, _ := full.IEs()
			tok, hasTok := ies.Get(IECallToken)
			// Like chan_iax2, tokens are checked only when the frame would
			// create a call (no destination call number).
			if full.DstCall != 0 {
				hasTok, tok = true, StringIE(IECallToken, "tok")
			}
			if s.requireToken && !hasTok {
				reply(IAXRegRej, 0, nil) // "CallToken Support required"
				continue
			}
			if s.requireToken && hasTok && string(tok.Data) != "tok" && len(tok.Data) != 0 {
				reply(IAXRegRej, 0, IEs{StringIE(IECause, "bad token")})
				continue
			}
			if s.requireToken && len(tok.Data) == 0 {
				// send_apathetic_reply: fake call number 1, oseqno 0.
				data, _ := (IEs{StringIE(IECallToken, "tok")}).Encode()
				f := &FullFrame{SrcCall: 1, DstCall: full.SrcCall, Timestamp: full.Timestamp,
					ISeqno: full.OSeqno + 1, Type: FrameIAX, Subclass: uint64(IAXCallToken), Data: data}
				b, _ := Encode(f)
				_, _ = s.conn.WriteToUDP(b, from)
				continue
			}
			if ies.String(IEUsername) != "phone" {
				reply(IAXRegRej, 0, IEs{StringIE(IECause, "No such user")})
				continue
			}
			if !ies.Has(IEMD5Result) {
				reply(IAXRegAuth, 0, IEs{
					Uint16IE(IEAuthMethods, uint16(AuthMD5|AuthPlaintext)),
					StringIE(IEChallenge, s.challenge),
					StringIE(IEUsername, "phone"),
				})
				continue
			}
			if ies.String(IEMD5Result) != MD5Response(s.challenge, s.secret) {
				reply(IAXRegAuth, 1, IEs{
					Uint16IE(IEAuthMethods, uint16(AuthMD5)),
					StringIE(IEChallenge, s.challenge),
				})
				continue
			}
			reply(IAXRegAck, 1, IEs{
				StringIE(IEUsername, "phone"),
				Uint32IE(IEDateTime, EncodeDateTime(time.Date(2026, 11, 4, 13, 7, 42, 0, time.Local))),
				Uint16IE(IERefresh, 60),
				sockaddrIE(netip.MustParseAddrPort("192.0.2.10:51014")),
				Uint16IE(IEMsgCount, 0xFFFF),
			})
		}
	}
}

// handleCall runs the server side of an outgoing call: NEW -> (CALLTOKEN)
// -> AUTHREQ, AUTHREP -> ACCEPT, ANSWER, one full VOICE frame then two mini
// frames, and optionally HANGUP. Sequence numbers follow what chan_iax2
// would send for this exact exchange.
func (s *fakeServer) handleCall(full *FullFrame, from *net.UDPAddr, reply func(IAXSubclass, uint8, IEs)) {
	ies, _ := full.IEs()
	switch IAXSubclass(full.Subclass) {
	case IAXNew:
		tok, hasTok := ies.Get(IECallToken)
		if s.requireToken && !hasTok {
			reply(IAXReject, 0, IEs{StringIE(IECause, "CallToken Support required")})
			return
		}
		if s.requireToken && len(tok.Data) == 0 {
			data, _ := (IEs{StringIE(IECallToken, "tok")}).Encode()
			f := &FullFrame{SrcCall: 1, DstCall: full.SrcCall, Timestamp: full.Timestamp,
				ISeqno: full.OSeqno + 1, Type: FrameIAX, Subclass: uint64(IAXCallToken), Data: data}
			b, _ := Encode(f)
			_, _ = s.conn.WriteToUDP(b, from)
			return
		}
		if ies.String(IECalledNumber) == "404" {
			reply(IAXReject, 0, IEs{StringIE(IECause, "No such context/extension"), Uint8IE(IECauseCode, CauseUnallocated)})
			return
		}
		reply(IAXAuthReq, 0, IEs{
			Uint16IE(IEAuthMethods, uint16(AuthMD5)),
			StringIE(IEChallenge, s.challenge),
			StringIE(IEUsername, "phone"),
		})
	case IAXAuthRep:
		if ies.String(IEMD5Result) != MD5Response(s.challenge, s.secret) {
			reply(IAXReject, 1, IEs{StringIE(IECause, "No authority found"), Uint8IE(IECauseCode, CauseCallRejected)})
			return
		}
		reply(IAXAccept, 1, IEs{Uint32IE(IEFormat, uint32(FormatULAW)), Format64IE(IEFormat2, FormatULAW)})
		go func() {
			time.Sleep(s.answerDelay)
			send := func(f *FullFrame) {
				b, _ := Encode(f)
				_, _ = s.conn.WriteToUDP(b, from)
			}
			base := &FullFrame{SrcCall: 0x1234, DstCall: full.SrcCall, ISeqno: 2}
			ctl := *base
			ctl.Type, ctl.Subclass, ctl.OSeqno, ctl.Timestamp = FrameControl, uint64(ControlAnswer), 2, 30
			send(&ctl)
			voice := *base
			voice.Type, voice.Subclass, voice.OSeqno, voice.Timestamp = FrameVoice, uint64(FormatULAW), 3, 40
			voice.Data = []byte{0xFF, 0xFF}
			send(&voice)
			for _, ts := range []uint16{60, 80} {
				b, _ := Encode(&MiniFrame{SrcCall: 0x1234, Timestamp: ts, Data: []byte{0xFF}})
				_, _ = s.conn.WriteToUDP(b, from)
			}
			ping := *base
			ping.Type, ping.Subclass, ping.OSeqno, ping.Timestamp = FrameIAX, uint64(IAXPing), 4, 100
			send(&ping)
			if s.hangupAfter > 0 {
				time.Sleep(s.hangupAfter)
				data, _ := (IEs{StringIE(IECause, "Normal Clearing"), Uint8IE(IECauseCode, CauseNormalClearing)}).Encode()
				hup := *base
				hup.Type, hup.Subclass, hup.OSeqno, hup.Timestamp, hup.Data = FrameIAX, uint64(IAXHangup), 5, 200, data
				send(&hup)
			}
		}()
	}
}

func (s *fakeServer) next(t *testing.T) *FullFrame {
	t.Helper()
	select {
	case f := <-s.frames:
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for a frame at the fake server")
		return nil
	}
}

func TestPing(t *testing.T) {
	srv := newFakeServer(t)
	c, err := Dial(context.Background(), Config{Server: srv.conn.LocalAddr().String(), LocalAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck

	rtt, err := c.Ping(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rtt <= 0 || rtt > time.Second {
		t.Errorf("rtt = %s", rtt)
	}

	poke := srv.next(t)
	if poke.Type != FrameIAX || IAXSubclass(poke.Subclass) != IAXPoke || poke.DstCall != 0 ||
		poke.OSeqno != 0 || poke.ISeqno != 0 {
		t.Errorf("unexpected first frame: %s", poke)
	}
	ack := srv.next(t)
	if ack.Type != FrameIAX || IAXSubclass(ack.Subclass) != IAXAck {
		t.Fatalf("expected ACK, got %s", ack)
	}
	if ack.DstCall != 0x1234 || ack.SrcCall != poke.SrcCall || ack.OSeqno != 1 || ack.ISeqno != 1 ||
		ack.Timestamp != poke.Timestamp {
		t.Errorf("bad ACK: %s (poke was %s)", ack, poke)
	}
	if c.lookup(poke.SrcCall) != nil {
		t.Error("call number not released after Ping")
	}
}

func TestPingTimeout(t *testing.T) {
	// A socket nobody answers on.
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

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if _, err := c.Ping(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
}

func dialFake(t *testing.T, srv *fakeServer, user, secret string) *Client {
	t.Helper()
	c, err := Dial(context.Background(), Config{
		Server: srv.conn.LocalAddr().String(), LocalAddr: "127.0.0.1:0",
		Username: user, Secret: secret, RegisterRefresh: 90 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() }) //nolint:errcheck
	return c
}

func TestRegisterOnce(t *testing.T) {
	srv := newFakeServer(t, func(s *fakeServer) { s.requireToken = false })
	c := dialFake(t, srv, "phone", "s3cret")

	reg, err := c.RegisterOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reg.Refresh != 60*time.Second || reg.ApparentAddr != "192.0.2.10:51014" ||
		reg.ServerTime.Hour() != 13 || reg.ServerTime.Minute() != 7 || reg.MsgCount != 0xFFFF {
		t.Errorf("registration = %+v", reg)
	}

	// REGREQ(user, refresh) -> REGAUTH -> REGREQ(md5) -> REGACK -> ACK
	req := srv.next(t)
	ies, _ := req.IEs()
	if IAXSubclass(req.Subclass) != IAXRegReq || ies.String(IEUsername) != "phone" || req.DstCall != 0 {
		t.Errorf("first frame: %s", req)
	}
	if r, _ := ies.Uint16(IERefresh); r != 90 {
		t.Errorf("refresh = %d", r)
	}
	rep := srv.next(t)
	ies, _ = rep.IEs()
	if IAXSubclass(rep.Subclass) != IAXRegReq || rep.DstCall != 0x1234 || rep.OSeqno != 1 || rep.ISeqno != 1 ||
		ies.String(IEMD5Result) != MD5Response("424242", "s3cret") {
		t.Errorf("auth reply: %s %v", rep, ies)
	}
	ack := srv.next(t)
	if IAXSubclass(ack.Subclass) != IAXAck || ack.OSeqno != 2 || ack.ISeqno != 2 || ack.DstCall != 0x1234 {
		t.Errorf("final ack: %s", ack)
	}
}

func TestRegisterBadSecret(t *testing.T) {
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "wrong")
	if _, err := c.RegisterOnce(context.Background()); !errors.Is(err, ErrAuth) {
		t.Errorf("err = %v, want ErrAuth", err)
	}
}

func TestRegisterRejected(t *testing.T) {
	srv := newFakeServer(t)
	c := dialFake(t, srv, "nobody", "s3cret")
	_, err := c.RegisterOnce(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); got != "iax2: "+srv.conn.LocalAddr().String()+" answered REGREQ with REGREJ: No such user" {
		t.Errorf("err = %q", got)
	}
}

func TestRegisterWithCallToken(t *testing.T) {
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	if _, err := c.RegisterOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := srv.next(t)
	if ies, _ := first.IEs(); !ies.Has(IECallToken) {
		t.Errorf("first REGREQ has no empty CALLTOKEN IE: %v", ies)
	}
	second := srv.next(t)
	ies, _ := second.IEs()
	if n := 0; true {
		for _, ie := range ies {
			if ie.ID == IECallToken {
				n++
			}
		}
		if n != 1 {
			t.Errorf("token resend carries %d CALLTOKEN IEs", n)
		}
	}
	if IAXSubclass(second.Subclass) != IAXRegReq || ies.String(IECallToken) != "tok" ||
		second.OSeqno != first.OSeqno || second.ISeqno != 0 || second.SrcCall != first.SrcCall {
		t.Errorf("token resend: %s %v (first %s)", second, ies, first)
	}
	if c.lookup(first.SrcCall) != nil {
		t.Error("call number not released")
	}
}

func TestUnregister(t *testing.T) {
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	if err := c.Unregister(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f := srv.next(t); IAXSubclass(f.Subclass) != IAXRegRel {
		t.Errorf("first frame: %s", f)
	}
}

func TestAnswerPoke(t *testing.T) {
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	poke := &FullFrame{SrcCall: 77, Timestamp: 1234, Type: FrameIAX, Subclass: uint64(IAXPoke)}
	b, _ := Encode(poke)
	if _, err := srv.conn.WriteToUDP(b, c.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	pong := srv.next(t)
	if IAXSubclass(pong.Subclass) != IAXPong || pong.DstCall != 77 || pong.Timestamp != 1234 ||
		pong.OSeqno != 0 || pong.ISeqno != 1 {
		t.Errorf("pong: %s", pong)
	}
}

func collectUntil(t *testing.T, srv *fakeServer, sub IAXSubclass) []*FullFrame {
	t.Helper()
	var out []*FullFrame
	for {
		f := srv.next(t)
		out = append(out, f)
		if f.Type == FrameIAX && IAXSubclass(f.Subclass) == sub {
			return out
		}
	}
}

func TestCallAndHangup(t *testing.T) {
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	media := make(chan MediaFrame, 16)

	call, err := c.Call(context.Background(), "600", media)
	if err != nil {
		t.Fatal(err)
	}
	// The fake server answers right away, so the state may already be Up.
	if st := call.State(); call.Format() != FormatULAW || (st != StateAccepted && st != StateUp) || call.RemoteNumber() != 0x1234 {
		t.Errorf("after accept: format=%s state=%s remote=%d", call.Format(), call.State(), call.RemoteNumber())
	}

	// NEW(empty token) -> NEW(token) -> AUTHREP -> ACK(ACCEPT)
	newf := srv.next(t)
	ies, _ := newf.IEs()
	if IAXSubclass(newf.Subclass) != IAXNew || ies.String(IECalledNumber) != "600" || ies.String(IEUsername) != "phone" {
		t.Errorf("NEW: %s %v", newf, ies)
	}
	if v, _ := ies.Uint16(IEVersion); v != 2 {
		t.Errorf("VERSION = %d", v)
	}
	if f, _ := ies.Format(IECapability, IECapability2); f != FormatULAW|FormatALAW {
		t.Errorf("CAPABILITY = %s", f)
	}
	if f := srv.next(t); IAXSubclass(f.Subclass) != IAXNew || f.OSeqno != 0 {
		t.Errorf("NEW with token: %s", f)
	}
	rep := srv.next(t)
	ies, _ = rep.IEs()
	if IAXSubclass(rep.Subclass) != IAXAuthRep || rep.OSeqno != 1 || rep.ISeqno != 1 || rep.DstCall != 0x1234 ||
		ies.String(IEMD5Result) != MD5Response("424242", "s3cret") {
		t.Errorf("AUTHREP: %s", rep)
	}
	if f := srv.next(t); IAXSubclass(f.Subclass) != IAXAck || f.ISeqno != 2 || f.OSeqno != 2 {
		t.Errorf("ACK of ACCEPT: %s", f)
	}

	// ANSWER event, then media (full + 2 mini), then PING answered by PONG.
	ev := <-call.Events
	if ev.Kind != EventAnswered || call.State() != StateUp {
		t.Errorf("event %v state %s", ev, call.State())
	}
	var got []MediaFrame
	for len(got) < 3 {
		select {
		case m := <-media:
			got = append(got, m)
		case <-time.After(2 * time.Second):
			t.Fatalf("media timeout, got %d frames", len(got))
		}
	}
	if got[0].Timestamp != 40 || got[1].Timestamp != 60 || got[2].Timestamp != 80 ||
		got[0].Format != FormatULAW || got[2].Format != FormatULAW || len(got[0].Data) != 2 {
		t.Errorf("media = %+v", got)
	}
	frames := collectUntil(t, srv, IAXPong)
	// ACK(ANSWER), ACK(VOICE), PONG
	if n := len(frames); n != 3 {
		t.Errorf("frames up to PONG: %d", n)
	}
	pong := frames[len(frames)-1]
	if pong.Timestamp != 100 || pong.ISeqno != 5 || pong.OSeqno != 2 {
		t.Errorf("PONG: %s", pong)
	}

	// Our voice: first frame full, then mini.
	if err := call.SendVoice([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := call.SendVoice([]byte{4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	v := srv.next(t)
	if v.Type != FrameVoice || Format(v.Subclass) != FormatULAW || v.OSeqno != 3 || len(v.Data) != 3 {
		t.Errorf("voice full frame: %s", v)
	}

	// Hangup: HANGUP then the server's ACK.
	if err := call.Hangup(CauseNormalClearing); err != nil {
		t.Fatal(err)
	}
	hup := srv.next(t)
	ies, _ = hup.IEs()
	if IAXSubclass(hup.Subclass) != IAXHangup || ies.String(IECause) != "Normal Clearing" {
		t.Errorf("HANGUP: %s %v", hup, ies)
	}
	if code, _ := ies.Uint8(IECauseCode); code != CauseNormalClearing {
		t.Errorf("CAUSECODE = %d", code)
	}
	select {
	case <-call.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("call not done after hangup")
	}
	if _, ok := <-call.Events; ok {
		t.Error("Events not closed")
	}
	if call.State() != StateDown || c.lookup(call.LocalNumber()) != nil {
		t.Errorf("after hangup: state=%s", call.State())
	}
	if err := call.SendVoice([]byte{1}); !errors.Is(err, ErrCallDown) {
		t.Errorf("SendVoice after hangup: %v", err)
	}
	if err := call.Hangup(CauseNormalClearing); err != nil {
		t.Errorf("second Hangup: %v", err)
	}
}

func TestCallPeerHangsUp(t *testing.T) {
	srv := newFakeServer(t, func(s *fakeServer) { s.hangupAfter = 50 * time.Millisecond })
	c := dialFake(t, srv, "phone", "s3cret")
	call, err := c.Call(context.Background(), "601", nil)
	if err != nil {
		t.Fatal(err)
	}
	var last Event
	for ev := range call.Events {
		last = ev
	}
	if last.Kind != EventHangup || last.Cause != "Normal Clearing" || last.CauseCode != CauseNormalClearing {
		t.Errorf("last event = %+v", last)
	}
	<-call.Done()
	if call.State() != StateDown || call.MediaDropped() != 0 {
		t.Errorf("state=%s dropped=%d", call.State(), call.MediaDropped())
	}
	frames := collectUntil(t, srv, IAXAck)
	_ = frames
}

func TestCallRejected(t *testing.T) {
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	_, err := c.Call(context.Background(), "404", nil)
	if err == nil || err.Error() != "iax2: "+srv.conn.LocalAddr().String()+" answered NEW with REJECT: No such context/extension" {
		t.Errorf("err = %v", err)
	}
	c2 := dialFake(t, srv, "phone", "wrong")
	if _, err := c2.Call(context.Background(), "600", nil); err == nil {
		t.Error("bad secret accepted")
	}
	c.mu.Lock()
	n := len(c.calls)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("%d calls left after rejection", n)
	}
}

func waitIncoming(t *testing.T, c *Client) *Call {
	t.Helper()
	select {
	case call := <-c.Incoming:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("no incoming call")
		return nil
	}
}

func TestIncomingCall(t *testing.T) {
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	srv.originate(t, c.LocalAddr(), "s")

	call := waitIncoming(t, c)
	if call.Outgoing() || call.CalledNumber() != "s" || call.CallerNumber() != "1001" ||
		call.CallerName() != "Asterisk" || call.Format() != FormatALAW || call.State() != StateRinging ||
		call.RemoteNumber() != 0x1234 {
		t.Errorf("incoming: out=%v called=%q caller=%q/%q format=%s state=%s remote=%d", call.Outgoing(),
			call.CalledNumber(), call.CallerNumber(), call.CallerName(), call.Format(), call.State(), call.RemoteNumber())
	}

	// AUTHREQ (answers the NEW, no ACK) -> ACCEPT (answers the AUTHREP) -> RINGING.
	req := srv.next(t)
	ies, _ := req.IEs()
	if IAXSubclass(req.Subclass) != IAXAuthReq || req.OSeqno != 0 || req.ISeqno != 1 || req.DstCall != 0x1234 ||
		ies.String(IEChallenge) == "" || ies.String(IEUsername) != "phone" {
		t.Errorf("AUTHREQ: %s %v", req, ies)
	}
	if m, _ := ies.Uint16(IEAuthMethods); AuthMethod(m) != AuthMD5 {
		t.Errorf("AUTHMETHODS = %d", m)
	}
	acc := srv.next(t)
	ies, _ = acc.IEs()
	f, _ := ies.Format(IEFormat, IEFormat2)
	if IAXSubclass(acc.Subclass) != IAXAccept || acc.OSeqno != 1 || acc.ISeqno != 2 || f != FormatALAW {
		t.Errorf("ACCEPT: %s format %s", acc, f)
	}
	if r := srv.next(t); r.Type != FrameControl || ControlSubclass(r.Subclass) != ControlRinging || r.OSeqno != 2 || r.ISeqno != 2 {
		t.Errorf("RINGING: %s", r)
	}

	// Answer, then the caller's voice reaches media.
	media := make(chan MediaFrame, 16)
	if err := call.Answer(media); err != nil {
		t.Fatal(err)
	}
	if a := srv.next(t); a.Type != FrameControl || ControlSubclass(a.Subclass) != ControlAnswer || a.OSeqno != 3 {
		t.Errorf("ANSWER: %s", a)
	}
	if call.State() != StateUp {
		t.Errorf("state after Answer: %s", call.State())
	}
	if err := call.Answer(media); !errors.Is(err, ErrNotRinging) {
		t.Errorf("second Answer: %v", err)
	}
	srv.sendTo(t, c.LocalAddr(), &FullFrame{DstCall: call.LocalNumber(), Timestamp: 300, OSeqno: 2, ISeqno: 4,
		Type: FrameVoice, Subclass: uint64(FormatALAW), Data: []byte{0xD5, 0xD5}})
	select {
	case m := <-media:
		if m.Timestamp != 300 || m.Format != FormatALAW || len(m.Data) != 2 {
			t.Errorf("media %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no media")
	}
	if ack := srv.next(t); IAXSubclass(ack.Subclass) != IAXAck || ack.Timestamp != 300 || ack.ISeqno != 3 {
		t.Errorf("ACK of voice: %s", ack)
	}

	// Our voice goes out with the negotiated codec.
	if err := call.SendVoice([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if v := srv.next(t); v.Type != FrameVoice || Format(v.Subclass) != FormatALAW || v.OSeqno != 4 {
		t.Errorf("voice: %s", v)
	}

	// The caller hangs up.
	data, _ := (IEs{StringIE(IECause, "Normal Clearing"), Uint8IE(IECauseCode, CauseNormalClearing)}).Encode()
	srv.sendTo(t, c.LocalAddr(), &FullFrame{DstCall: call.LocalNumber(), Timestamp: 400, OSeqno: 3, ISeqno: 5,
		Type: FrameIAX, Subclass: uint64(IAXHangup), Data: data})
	select {
	case ev := <-call.Events:
		if ev.Kind != EventHangup || ev.Cause != "Normal Clearing" || ev.CauseCode != CauseNormalClearing {
			t.Errorf("event %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no hangup event")
	}
	<-call.Done()
	if call.State() != StateDown || c.lookup(call.LocalNumber()) != nil {
		t.Errorf("after hangup: %s", call.State())
	}
}

func TestIncomingCallWithoutSecret(t *testing.T) {
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "")
	srv.originate(t, c.LocalAddr(), "s")
	call := waitIncoming(t, c)
	if acc := srv.next(t); IAXSubclass(acc.Subclass) != IAXAccept || acc.OSeqno != 0 || acc.ISeqno != 1 {
		t.Errorf("ACCEPT: %s", acc)
	}
	srv.next(t) // RINGING
	if err := call.Hangup(CauseUserBusy); err != nil {
		t.Fatal(err)
	}
	hup := srv.next(t)
	ies, _ := hup.IEs()
	if IAXSubclass(hup.Subclass) != IAXHangup || ies.String(IECause) != "User busy" {
		t.Errorf("HANGUP: %s %v", hup, ies)
	}
	<-call.Done()
}

func TestIncomingCallBadSecret(t *testing.T) {
	srv := newFakeServer(t, func(s *fakeServer) { s.wrongSecret = true })
	c := dialFake(t, srv, "phone", "s3cret")
	srv.originate(t, c.LocalAddr(), "s")
	srv.next(t) // AUTHREQ
	rej := srv.next(t)
	ies, _ := rej.IEs()
	if IAXSubclass(rej.Subclass) != IAXReject || ies.String(IECause) != "No authority found" {
		t.Errorf("REJECT: %s %v", rej, ies)
	}
	if code, _ := ies.Uint8(IECauseCode); code != CauseCallRejected {
		t.Errorf("CAUSECODE = %d", code)
	}
	select {
	case call := <-c.Incoming:
		t.Fatalf("unexpected incoming call %d", call.LocalNumber())
	case <-time.After(200 * time.Millisecond):
	}
	c.mu.Lock()
	n := len(c.calls)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("%d calls still allocated", n)
	}
}

func TestIncomingCallNoCommonCodec(t *testing.T) {
	srv := newFakeServer(t)
	c, err := Dial(context.Background(), Config{
		Server: srv.conn.LocalAddr().String(), LocalAddr: "127.0.0.1:0",
		Username: "phone", Secret: "s3cret", Capability: FormatGSM, Preferred: FormatGSM,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() }) //nolint:errcheck
	srv.originate(t, c.LocalAddr(), "s")
	rej := srv.next(t)
	ies, _ := rej.IEs()
	if IAXSubclass(rej.Subclass) != IAXReject || ies.String(IECause) != "Unable to negotiate codec" {
		t.Errorf("REJECT: %s %v", rej, ies)
	}
}

func TestDTMF(t *testing.T) {
	srv := newFakeServer(t)
	c := dialFake(t, srv, "phone", "s3cret")
	call, err := c.Call(context.Background(), "600", make(chan MediaFrame, 16))
	if err != nil {
		t.Fatal(err)
	}
	if err := call.SendDTMF('1'); !errors.Is(err, ErrCallDown) && call.State() != StateUp {
		t.Errorf("SendDTMF before answer: %v", err)
	}
	if ev := <-call.Events; ev.Kind != EventAnswered {
		t.Fatalf("event %v", ev)
	}
	collectUntil(t, srv, IAXPong) // NEW ... PONG: the call is up

	// Our digit: DTMF_BEGIN then, DTMFDuration later, DTMF_END, both
	// sequenced and carrying the digit as subclass.
	if err := call.SendDTMF('x'); !errors.Is(err, ErrBadDigit) {
		t.Errorf("SendDTMF('x'): %v", err)
	}
	t0 := time.Now()
	if err := call.SendDTMF('d'); err != nil {
		t.Fatal(err)
	}
	if held := time.Since(t0); held < DTMFDuration {
		t.Errorf("digit held %s", held)
	}
	begin, end := srv.next(t), srv.next(t)
	if begin.Type != FrameDTMFBegin || begin.Subclass != 'D' || begin.OSeqno != 3 || begin.ISeqno != 5 {
		t.Errorf("DTMF_BEGIN: %s", begin)
	}
	if end.Type != FrameDTMFEnd || end.Subclass != 'D' || end.OSeqno != 4 || end.Timestamp < begin.Timestamp+90 {
		t.Errorf("DTMF_END: %s", end)
	}
	// A key held by the user: BEGIN and END are sent separately, when asked.
	if err := call.DTMFBegin('*'); err != nil {
		t.Fatal(err)
	}
	if err := call.DTMFEnd('*'); err != nil {
		t.Fatal(err)
	}
	if err := call.DTMFEnd('!'); !errors.Is(err, ErrBadDigit) {
		t.Errorf("DTMFEnd('!'): %v", err)
	}
	begin, end = srv.next(t), srv.next(t)
	if begin.Type != FrameDTMFBegin || begin.Subclass != '*' || begin.OSeqno != 5 {
		t.Errorf("held DTMF_BEGIN: %s", begin)
	}
	if end.Type != FrameDTMFEnd || end.Subclass != '*' || end.OSeqno != 6 {
		t.Errorf("held DTMF_END: %s", end)
	}

	// The peer's digits: BEGIN+END is reported once, a lone END too.
	peer := func(typ FrameType, digit byte, oseq uint8, ts uint32) {
		srv.sendTo(t, c.LocalAddr(), &FullFrame{SrcCall: 0x1234, DstCall: call.LocalNumber(), Timestamp: ts,
			OSeqno: oseq, ISeqno: 7, Type: typ, Subclass: uint64(digit)})
	}
	peer(FrameDTMFBegin, '7', 5, 300)
	peer(FrameDTMFEnd, '7', 6, 400)
	peer(FrameDTMFEnd, '#', 7, 600)
	for _, want := range []byte{'7', '#'} {
		select {
		case ev := <-call.Events:
			if ev.Kind != EventDTMF || ev.Digit != want {
				t.Errorf("event %+v, want digit %c", ev, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no DTMF event for %c", want)
		}
	}
	for i, ts := range []uint32{300, 400, 600} {
		if ack := srv.next(t); IAXSubclass(ack.Subclass) != IAXAck || ack.Timestamp != ts || ack.ISeqno != uint8(6+i) {
			t.Errorf("ACK of DTMF frame %d: %s", i, ack)
		}
	}
	select {
	case ev := <-call.Events:
		t.Errorf("unexpected event %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}

	if err := call.Hangup(CauseNormalClearing); err != nil {
		t.Fatal(err)
	}
	<-call.Done()
	if err := call.SendDTMF('1'); !errors.Is(err, ErrCallDown) {
		t.Errorf("SendDTMF after hangup: %v", err)
	}
}
