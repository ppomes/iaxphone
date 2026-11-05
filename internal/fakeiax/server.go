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
// frames get an ACK. It can also originate a call, press keys and hang up.
package fakeiax

import (
	"bytes"
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

	mu       sync.Mutex
	regs     int // REGREQ seen
	hangups  []string
	digits   string // DTMF_END digits received
	client   *net.UDPAddr
	callNo   uint16 // our call number for the current call
	peerCall uint16 // the client's call number for the current call
	audioTS  uint32 // timestamp of the next SendAudio frame
}

// New starts a server; it is closed when the test ends.
func New(tb testing.TB) *Server {
	tb.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		tb.Fatal(err)
	}
	s := &Server{tb: tb, conn: conn, callNo: 0x1000}
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
		if !ok || full.Retransmit {
			continue
		}
		s.mu.Lock()
		s.client = from
		no := s.callNo
		isReg := full.Type == iax2.FrameIAX &&
			(iax2.IAXSubclass(full.Subclass) == iax2.IAXRegReq || iax2.IAXSubclass(full.Subclass) == iax2.IAXRegRel)
		if !isReg {
			s.peerCall = full.SrcCall
		}
		s.mu.Unlock()
		reply := func(typ iax2.FrameType, sub uint64, oseq uint8, ies iax2.IEs) {
			data, _ := ies.Encode()
			s.send(from, &iax2.FullFrame{
				SrcCall: no, DstCall: full.SrcCall, Timestamp: full.Timestamp,
				OSeqno: oseq, ISeqno: full.OSeqno + 1, Type: typ, Subclass: sub, Data: data,
			})
		}
		if full.Type == iax2.FrameControl || full.Type == iax2.FrameDTMFBegin || full.Type == iax2.FrameDTMFEnd {
			if full.Type == iax2.FrameDTMFEnd {
				s.mu.Lock()
				s.digits += string(rune(full.Subclass))
				s.mu.Unlock()
			}
			reply(iax2.FrameIAX, uint64(iax2.IAXAck), full.ISeqno, nil)
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
			reply(iax2.FrameIAX, uint64(iax2.IAXRegAck), 0, iax2.IEs{iax2.Uint16IE(iax2.IERefresh, 60)})
		case iax2.IAXRegRel:
			reply(iax2.FrameIAX, uint64(iax2.IAXRegAck), 0, nil)
		case iax2.IAXNew:
			ies, _ := full.IEs()
			if ies.String(iax2.IECalledNumber) == "404" {
				reply(iax2.FrameIAX, uint64(iax2.IAXReject), 0, iax2.IEs{iax2.StringIE(iax2.IECause, "No such extension")})
				continue
			}
			reply(iax2.FrameIAX, uint64(iax2.IAXAccept), 0, iax2.IEs{iax2.Uint32IE(iax2.IEFormat, uint32(iax2.FormatULAW))})
			reply(iax2.FrameControl, uint64(iax2.ControlRinging), 1, nil)
			reply(iax2.FrameControl, uint64(iax2.ControlAnswer), 2, nil)
			s.send(from, &iax2.FullFrame{SrcCall: no, DstCall: full.SrcCall, Timestamp: 40, OSeqno: 3, ISeqno: full.OSeqno + 1,
				Type: iax2.FrameVoice, Subclass: uint64(iax2.FormatULAW), Data: make([]byte, 160)})
		case iax2.IAXAccept:
			reply(iax2.FrameIAX, uint64(iax2.IAXAck), full.ISeqno, nil)
		case iax2.IAXHangup:
			ies, _ := full.IEs()
			s.mu.Lock()
			s.hangups = append(s.hangups, ies.String(iax2.IECause))
			s.mu.Unlock()
			reply(iax2.FrameIAX, uint64(iax2.IAXAck), full.ISeqno, nil)
		}
	}
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

// Hangup ends the current call from the server side with "Normal Clearing".
func (s *Server) Hangup() {
	s.mu.Lock()
	to, no, peer := s.client, s.callNo, s.peerCall
	s.mu.Unlock()
	data, _ := (iax2.IEs{iax2.StringIE(iax2.IECause, "Normal Clearing"), iax2.Uint8IE(iax2.IECauseCode, iax2.CauseNormalClearing)}).Encode()
	s.send(to, &iax2.FullFrame{SrcCall: no, DstCall: peer, Timestamp: 500, OSeqno: 4, ISeqno: 9,
		Type: iax2.FrameIAX, Subclass: uint64(iax2.IAXHangup), Data: data})
}

// PressKey sends DTMF_BEGIN and DTMF_END for digit on the current call,
// with outbound sequence numbers oseq and oseq+1.
func (s *Server) PressKey(digit byte, oseq uint8) {
	s.mu.Lock()
	to, no, peer := s.client, s.callNo, s.peerCall
	s.mu.Unlock()
	for i, typ := range []iax2.FrameType{iax2.FrameDTMFBegin, iax2.FrameDTMFEnd} {
		s.send(to, &iax2.FullFrame{SrcCall: no, DstCall: peer, Timestamp: 300 + 100*uint32(i), //nolint:gosec // 0 or 1
			OSeqno: oseq + uint8(i), ISeqno: 9, Type: typ, Subclass: uint64(digit)}) //nolint:gosec // 0 or 1
	}
}

// SendAudio sends count voice mini frames of frameMs each on the current
// call, paced in real time, timestamps following on from the previous
// audio (the first voice frame is at 40), loud enough to meter: what a
// peer with its own packetisation sends.
func (s *Server) SendAudio(frameMs, count int) {
	s.mu.Lock()
	to, no := s.client, s.callNo
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
