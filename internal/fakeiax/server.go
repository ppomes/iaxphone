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

// Package fakeiax is the minimum of an IAX2 server for the tests of the
// packages above iax2: no authentication, REGREQ and REGREL get a REGACK,
// NEW gets ACCEPT, RINGING, ANSWER and one voice frame, HANGUP, CONTROL and DTMF
// frames get an ACK, QUELCH and UNQUELCH put the call on and off hold, and
// TRANSFER is followed by a HANGUP, as Asterisk hangs up on the party that
// transferred a call. It can also originate a call, press keys, hold, ask
// for a transfer and hang up.
//
// Like chan_iax2 it keeps sequence numbers, acknowledges again a frame it
// has already processed (the client retransmitting because the ACK was
// lost) and, in Lossy mode, drops the first copy of every sequenced frame
// so that only retransmissions get through.
package fakeiax

import (
	"bytes"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ppomes/iaxphone/audio"
	"github.com/ppomes/iaxphone/iax2"
)

// Server is one fake server on a loopback UDP socket.
type Server struct {
	tb   testing.TB
	conn *net.UDPConn

	mu        sync.Mutex
	regs      int // REGREQ seen
	hangups   []string
	digits    string   // DTMF_END digits received
	xfers     []string // CALLED_NUMBER of every TRANSFER received
	client    *net.UDPAddr
	callNo    uint16               // last call number handed out
	calls     map[uint16]*fakeCall // by the client's call number
	cur       *fakeCall            // the call the client last spoke on (but to hang up)
	offered   *fakeCall            // Originate sent a NEW: its peer number comes with the reply
	audioTS   uint32               // timestamp of the next SendAudio frame
	lossy     bool                 // drop the first copy of every sequenced frame
	deaf      bool                 // ignore everything
	unbridged bool                 // acknowledge TRANSFER but keep the call
	dropped   map[string]bool      // frames dropped once (Lossy)
	seen      map[string]bool      // sequenced frames processed, re-acked when repeated
	retrans   int                  // retransmitted frames received
}

// fakeCall is one call leg at the server.
type fakeCall struct {
	no   uint16 // our call number
	peer uint16 // the client's
	oseq uint8  // our next outbound sequence number
	iseq uint8  // the client's next sequence number
	held bool   // between the client's QUELCH and UNQUELCH
}

// New starts a server; it is closed when the test ends.
func New(tb testing.TB) *Server {
	tb.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		tb.Fatal(err)
	}
	s := &Server{tb: tb, conn: conn, callNo: 0x1000, calls: make(map[uint16]*fakeCall),
		dropped: make(map[string]bool), seen: make(map[string]bool)}
	tb.Cleanup(func() { conn.Close() }) //nolint:errcheck
	go s.loop()
	return s
}

// Addr is the host:port to dial.
func (s *Server) Addr() string { return s.conn.LocalAddr().String() }

func (s *Server) send(to *net.UDPAddr, f *iax2.FullFrame) {
	b, err := iax2.Encode(f)
	if err != nil {
		s.tb.Error(err)
		return
	}
	_, _ = s.conn.WriteToUDP(b, to)
}

// sequenced reports whether a full frame consumes a sequence number and
// is acknowledged (RFC 5456 §8.1.1).
func sequenced(f *iax2.FullFrame) bool {
	if f.Type != iax2.FrameIAX {
		return true
	}
	switch iax2.IAXSubclass(f.Subclass) {
	case iax2.IAXAck, iax2.IAXInval, iax2.IAXVnak, iax2.IAXTxCnt, iax2.IAXTxAcc, iax2.IAXTxRel:
		return false
	}
	return true
}

func (s *Server) loop() {
	buf := make([]byte, 1500)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		f, err := iax2.Parse(append([]byte(nil), buf[:n]...))
		if err != nil {
			continue
		}
		full, ok := f.(*iax2.FullFrame)
		if !ok {
			continue
		}
		s.mu.Lock()
		if s.deaf {
			s.mu.Unlock()
			continue
		}
		if full.Retransmit {
			s.retrans++
		}
		// A frame is told from its repeats by call, sequence number and
		// payload (a NEW resent with its call token is a new frame).
		key := fmt.Sprintf("%d/%d/%x", full.SrcCall, full.OSeqno, full.Data)
		repeat := false
		if sequenced(full) {
			if s.lossy && !s.dropped[key] {
				s.dropped[key] = true // lost on the way, this once
				s.mu.Unlock()
				continue
			}
			repeat = s.seen[key]
			s.seen[key] = true
		}
		s.client = from
		isReg := full.Type == iax2.FrameIAX &&
			(iax2.IAXSubclass(full.Subclass) == iax2.IAXRegReq || iax2.IAXSubclass(full.Subclass) == iax2.IAXRegRel)
		fc := &fakeCall{no: s.callNo} // registrations: a throwaway leg
		if !isReg {
			fc = s.calls[full.SrcCall]
			switch {
			case fc != nil:
			case s.offered != nil:
				// The client's answer to our NEW: now we know its number.
				fc, s.offered = s.offered, nil
				fc.peer = full.SrcCall
				s.calls[fc.peer] = fc
			default:
				s.callNo++
				fc = &fakeCall{no: s.callNo, peer: full.SrcCall}
				s.calls[fc.peer] = fc
			}
			if sequenced(full) && !repeat {
				fc.iseq = full.OSeqno + 1
			}
			if full.Type != iax2.FrameIAX || iax2.IAXSubclass(full.Subclass) != iax2.IAXHangup {
				s.cur = fc
			}
		}
		s.mu.Unlock()
		// ack answers with an ACK; next sends a sequenced frame of ours.
		ack := func() {
			s.send(from, &iax2.FullFrame{
				SrcCall: fc.no, DstCall: full.SrcCall, Timestamp: full.Timestamp,
				OSeqno: s.seq(fc, false), ISeqno: full.OSeqno + 1, Type: iax2.FrameIAX, Subclass: uint64(iax2.IAXAck),
			})
		}
		next := func(typ iax2.FrameType, sub uint64, ts uint32, ies iax2.IEs) {
			data, _ := ies.Encode()
			s.send(from, &iax2.FullFrame{
				SrcCall: fc.no, DstCall: full.SrcCall, Timestamp: ts,
				OSeqno: s.seq(fc, true), ISeqno: full.OSeqno + 1, Type: typ, Subclass: sub, Data: data,
			})
		}
		if repeat {
			ack() // our ACK or reply was lost: acknowledge again
			continue
		}
		if full.Type == iax2.FrameControl || full.Type == iax2.FrameDTMFBegin || full.Type == iax2.FrameDTMFEnd {
			if full.Type == iax2.FrameDTMFEnd {
				s.mu.Lock()
				s.digits += string(rune(full.Subclass))
				s.mu.Unlock()
			}
			ack()
			continue
		}
		if full.Type != iax2.FrameIAX {
			continue
		}
		switch iax2.IAXSubclass(full.Subclass) {
		case iax2.IAXRegReq:
			s.mu.Lock()
			s.regs++
			s.mu.Unlock()
			next(iax2.FrameIAX, uint64(iax2.IAXRegAck), full.Timestamp, iax2.IEs{iax2.Uint16IE(iax2.IERefresh, 60)})
		case iax2.IAXRegRel:
			next(iax2.FrameIAX, uint64(iax2.IAXRegAck), full.Timestamp, nil)
		case iax2.IAXNew:
			ies, _ := full.IEs()
			if ies.String(iax2.IECalledNumber) == "404" {
				next(iax2.FrameIAX, uint64(iax2.IAXReject), full.Timestamp, iax2.IEs{iax2.StringIE(iax2.IECause, "No such extension")})
				continue
			}
			next(iax2.FrameIAX, uint64(iax2.IAXAccept), full.Timestamp, iax2.IEs{iax2.Uint32IE(iax2.IEFormat, uint32(iax2.FormatULAW))})
			next(iax2.FrameControl, uint64(iax2.ControlRinging), full.Timestamp, nil)
			next(iax2.FrameControl, uint64(iax2.ControlAnswer), full.Timestamp, nil)
			s.send(from, &iax2.FullFrame{SrcCall: fc.no, DstCall: full.SrcCall, Timestamp: 40, OSeqno: s.seq(fc, true), ISeqno: full.OSeqno + 1,
				Type: iax2.FrameVoice, Subclass: uint64(iax2.FormatULAW), Data: make([]byte, 160)})
		case iax2.IAXAccept, iax2.IAXReject:
			ack()
		case iax2.IAXQuelch, iax2.IAXUnquelch:
			s.mu.Lock()
			fc.held = iax2.IAXSubclass(full.Subclass) == iax2.IAXQuelch
			s.mu.Unlock()
			ack()
		case iax2.IAXTransfer:
			ies, _ := full.IEs()
			s.mu.Lock()
			s.xfers = append(s.xfers, ies.String(iax2.IECalledNumber))
			unbridged := s.unbridged
			s.mu.Unlock()
			ack()
			if unbridged {
				break
			}
			next(iax2.FrameIAX, uint64(iax2.IAXHangup), full.Timestamp+10,
				iax2.IEs{iax2.StringIE(iax2.IECause, "Normal Clearing"), iax2.Uint8IE(iax2.IECauseCode, iax2.CauseNormalClearing)})
		case iax2.IAXHangup:
			ies, _ := full.IEs()
			s.mu.Lock()
			s.hangups = append(s.hangups, ies.String(iax2.IECause))
			s.mu.Unlock()
			ack()
		}
	}
}

// seq returns our outbound sequence number for the next frame of fc,
// consuming it when the frame is sequenced.
func (s *Server) seq(fc *fakeCall, consume bool) uint8 {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq := fc.oseq
	if consume {
		fc.oseq++
	}
	return seq
}

// Originate sends a NEW from "Asterisk" <1001> to the client at addr.
func (s *Server) Originate(addr string) {
	to, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		s.tb.Fatal(err)
	}
	s.mu.Lock()
	s.callNo++
	no := s.callNo
	s.offered = &fakeCall{no: no, oseq: 1} // the NEW is our frame 0
	s.mu.Unlock()
	data, _ := (iax2.IEs{
		iax2.StringIE(iax2.IECalledNumber, "s"),
		iax2.StringIE(iax2.IECallingNumber, "1001"),
		iax2.StringIE(iax2.IECallingName, "Asterisk"),
		iax2.Uint32IE(iax2.IEFormat, uint32(iax2.FormatULAW)),
		iax2.Uint32IE(iax2.IECapability, uint32(iax2.FormatULAW)),
	}).Encode()
	s.send(to, &iax2.FullFrame{SrcCall: no, Timestamp: 1, Type: iax2.FrameIAX, Subclass: uint64(iax2.IAXNew), Data: data})
}

// sendSeq sends one sequenced full frame of ours on the current call.
func (s *Server) sendSeq(typ iax2.FrameType, sub uint64, ts uint32, data []byte) {
	s.mu.Lock()
	to, fc := s.client, s.cur
	if fc == nil {
		s.mu.Unlock()
		s.tb.Fatal("fakeiax: no current call")
		return
	}
	f := &iax2.FullFrame{SrcCall: fc.no, DstCall: fc.peer, Timestamp: ts, OSeqno: fc.oseq, ISeqno: fc.iseq,
		Type: typ, Subclass: sub, Data: data}
	fc.oseq++
	s.mu.Unlock()
	s.send(to, f)
}

// Hangup ends the current call from the server side with "Normal Clearing".
func (s *Server) Hangup() {
	data, _ := (iax2.IEs{iax2.StringIE(iax2.IECause, "Normal Clearing"), iax2.Uint8IE(iax2.IECauseCode, iax2.CauseNormalClearing)}).Encode()
	s.sendSeq(iax2.FrameIAX, uint64(iax2.IAXHangup), 500, data)
}

// PressKey sends DTMF_BEGIN and DTMF_END for digit on the current call.
func (s *Server) PressKey(digit byte) {
	s.sendSeq(iax2.FrameDTMFBegin, uint64(digit), 300, nil)
	s.sendSeq(iax2.FrameDTMFEnd, uint64(digit), 400, nil)
}

// Hold puts the client on hold (QUELCH) or takes it back (UNQUELCH).
func (s *Server) Hold(on bool) {
	if on {
		s.sendSeq(iax2.FrameIAX, uint64(iax2.IAXQuelch), 500, nil)
		return
	}
	s.sendSeq(iax2.FrameIAX, uint64(iax2.IAXUnquelch), 600, nil)
}

// Transfer asks the client to call number instead (TRANSFER), as the
// Transfer() dialplan application does.
func (s *Server) Transfer(number string) {
	data, _ := (iax2.IEs{iax2.StringIE(iax2.IECalledNumber, number)}).Encode()
	s.sendSeq(iax2.FrameIAX, uint64(iax2.IAXTransfer), 700, data)
}

// Lossy makes the server drop the first copy of every sequenced frame the
// client sends, as a bad link would: only the retransmissions get through.
func (s *Server) Lossy(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lossy = on
}

// Unbridged makes the server acknowledge TRANSFER without acting on it,
// as Asterisk does when the call is not bridged to anyone.
func (s *Server) Unbridged(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unbridged = on
}

// Deaf makes the server ignore everything: a server that went away.
func (s *Server) Deaf(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deaf = on
}

// Retransmitted counts the frames received with the retransmission bit.
func (s *Server) Retransmitted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retrans
}

// SendAudio sends count voice mini frames of frameMs each on the current
// call, paced in real time, timestamps following on from the previous
// audio (the first voice frame is at 40), loud enough to meter: what a
// peer with its own packetisation sends.
func (s *Server) SendAudio(frameMs, count int) {
	s.mu.Lock()
	to, fc := s.client, s.cur
	if fc == nil {
		s.mu.Unlock()
		s.tb.Fatal("fakeiax: no current call")
		return
	}
	no := fc.no
	if s.audioTS == 0 {
		s.audioTS = 60
	}
	ts := s.audioTS
	s.audioTS += uint32(frameMs * count) //nolint:gosec // small
	s.mu.Unlock()
	data := bytes.Repeat([]byte{audio.LinearToUlaw(12000)}, frameMs*audio.SampleRate/1000)
	for range count {
		f := &iax2.MiniFrame{SrcCall: no, Timestamp: uint16(ts), Data: data} //nolint:gosec // low 16 bits
		b, err := f.AppendTo(nil)
		if err != nil {
			s.tb.Fatal(err)
		}
		if _, err := s.conn.WriteToUDP(b, to); err != nil {
			s.tb.Error(err)
		}
		ts += uint32(frameMs) //nolint:gosec // small
		time.Sleep(time.Duration(frameMs) * time.Millisecond)
	}
}

// Registrations counts the REGREQ received.
func (s *Server) Registrations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.regs
}

// Held reports whether the client put the current call on hold.
func (s *Server) Held() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur != nil && s.cur.held
}

// Transfers returns the number of every TRANSFER received.
func (s *Server) Transfers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.xfers...)
}

// Digits returns the DTMF keys received so far.
func (s *Server) Digits() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.digits
}

// HangupCauses returns the CAUSE of every HANGUP received.
func (s *Server) HangupCauses() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.hangups...)
}
