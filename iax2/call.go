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
	"sync"
	"time"
)

// ErrCallDown is returned when sending on a call that has ended.
var ErrCallDown = errors.New("iax2: call is down")

// State of a call (RFC 5456 §6.3 and §6.4).
type State int

// Call states.
const (
	StateIdle       State = iota
	StateNewSent          // NEW sent, waiting for AUTHREQ, ACCEPT or REJECT
	StateAuthSent         // AUTHREP sent
	StateAccepted         // ACCEPT received, waiting for ANSWER
	StateIncoming         // incoming: NEW received, authenticating
	StateRinging          // incoming: ACCEPT and RINGING sent, waiting for Answer
	StateUp               // ANSWER exchanged, media flowing
	StateHangupSent       // HANGUP sent, waiting for ACK
	StateDown             // terminal
)

var stateNames = [...]string{
	"idle", "new-sent", "auth-sent", "accepted", "incoming", "ringing", "up", "hangup-sent", "down",
}

func (s State) String() string {
	if int(s) < len(stateNames) {
		return stateNames[s]
	}
	return "State(?)"
}

// EventKind classifies call events.
type EventKind int

// Call events delivered on Call.Events.
const (
	EventRinging    EventKind = iota + 1 // peer is ringing
	EventProceeding                      // call is being routed
	EventAnswered                        // media may flow
	EventHangup                          // peer ended the call; Events is closed after it
	EventDTMF                            // the peer pressed a key (Event.Digit)
)

var eventNames = [...]string{"?", "ringing", "proceeding", "answered", "hangup", "dtmf"}

func (k EventKind) String() string {
	if int(k) < len(eventNames) {
		return eventNames[k]
	}
	return "Event(?)"
}

// Event is a signalling notification.
type Event struct {
	Kind      EventKind
	Cause     string // CAUSE IE for EventHangup
	CauseCode uint8  // CAUSECODE IE for EventHangup
	Digit     byte   // '0'-'9', '*', '#' or 'A'-'D' for EventDTMF
}

// MediaFrame is one encoded voice frame received from the peer.
type MediaFrame struct {
	Timestamp uint32 // ms since the start of the call
	Format    Format
	Data      []byte
}

// Call is one IAX2 call leg. Protocol housekeeping (sequence numbers, ACKs,
// PING/LAGRQ replies, media) happens in handleFull/handleMini on the
// client's read goroutine; signalling is consumed by the transaction in
// progress (await) and then by run.
type Call struct {
	mu sync.Mutex

	client   *Client
	localNo  uint16 // our call number
	remoteNo uint16 // peer's call number, 0 until the first reply
	state    State
	start    time.Time // timestamp origin
	oseqno   uint8     // next outbound sequence number
	iseqno   uint8     // next expected inbound sequence number
	format   Format    // negotiated codec
	lastTS   uint32    // most recent timestamp received, for ExpandTimestamp
	outgoing bool
	called   string // dialled number (outgoing) or CALLED_NUMBER IE (incoming)
	caller   string // CALLING_NUMBER IE of an incoming call
	callerNm string // CALLING_NAME IE of an incoming call

	// media receives the voice frames from the peer. It is never closed;
	// frames are dropped when it is full.
	media chan<- MediaFrame

	voiceStarted bool   // first voice frame (full) sent
	lastVoiceTS  uint32 // for the 16-bit rollover check
	mediaDropped int

	dtmfBegun byte // digit of the DTMF_BEGIN received and not yet ended (run only)
	dtmfMu    sync.Mutex

	// pending holds full frames awaiting an ACK, keyed by OSeqno.
	// TODO: retransmission timer for every full frame, VNAK handling.
	pending map[uint8]*FullFrame

	// inbox receives the signalling full frames addressed to this call.
	inbox chan *FullFrame
	// acks receives the ISeqno of each ACK, for waitAck.
	acks chan uint8
	// stop is closed by Hangup to end run.
	stop     chan struct{}
	stopOnce sync.Once
	// done is closed by run when the call is over.
	done chan struct{}

	// Events receives signalling events; it is closed when the call ends.
	Events chan Event
}

func newCallState(c *Client, no uint16) *Call {
	return &Call{
		client:  c,
		localNo: no,
		start:   time.Now(),
		pending: make(map[uint8]*FullFrame),
		inbox:   make(chan *FullFrame, 32),
		acks:    make(chan uint8, 8),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		Events:  make(chan Event, 16),
	}
}

// LocalNumber returns our call number.
func (c *Call) LocalNumber() uint16 { return c.localNo }

// RemoteNumber returns the peer's call number, 0 until known.
func (c *Call) RemoteNumber() uint16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remoteNo
}

// Outgoing reports whether we placed the call (false: the server offered it).
func (c *Call) Outgoing() bool { return c.outgoing }

// CalledNumber is the number we dialled, or the one the caller dialled to
// reach us.
func (c *Call) CalledNumber() string { return c.called }

// CallerNumber and CallerName are the caller id of an incoming call.
func (c *Call) CallerNumber() string { return c.caller }

// CallerName is the caller's display name of an incoming call.
func (c *Call) CallerName() string { return c.callerNm }

// State returns the current state.
func (c *Call) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Format returns the negotiated codec, 0 until the call is accepted.
func (c *Call) Format() Format {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.format
}

// Done is closed when the call is over, whichever side ended it.
func (c *Call) Done() <-chan struct{} { return c.done }

// MediaDropped counts voice frames dropped because Media was full.
func (c *Call) MediaDropped() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mediaDropped
}

func (c *Call) setState(s State) {
	c.mu.Lock()
	c.state = s
	c.mu.Unlock()
}

// timestamp returns the current call timestamp in milliseconds.
func (c *Call) timestamp() uint32 {
	return uint32(time.Since(c.start) / time.Millisecond) //nolint:gosec // wraps after 49 days by design
}

// sendFull sends a full frame on this call with the current timestamp.
func (c *Call) sendFull(typ FrameType, subclass uint64, data []byte) (*FullFrame, error) {
	return c.sendFullTS(typ, subclass, c.timestamp(), data)
}

// sendFullTS sends a full frame, stamping call numbers and sequence numbers.
// Frames that are not sequenced (ACK, INVAL, VNAK, TXCNT, TXACC, TXREL) do
// not consume an outbound sequence number.
func (c *Call) sendFullTS(typ FrameType, subclass uint64, ts uint32, data []byte) (*FullFrame, error) {
	c.mu.Lock()
	f := &FullFrame{
		SrcCall:   c.localNo,
		DstCall:   c.remoteNo,
		Timestamp: ts,
		OSeqno:    c.oseqno,
		ISeqno:    c.iseqno,
		Type:      typ,
		Subclass:  subclass,
		Data:      data,
	}
	if sequenced(f) {
		c.oseqno++
	}
	c.mu.Unlock()
	return f, c.client.send(f)
}

// resend retransmits a full frame unchanged apart from the R bit and the
// current inbound sequence number.
func (c *Call) resend(f *FullFrame) error {
	c.mu.Lock()
	r := *f
	r.Retransmit = true
	r.ISeqno = c.iseqno
	r.DstCall = c.remoteNo
	c.mu.Unlock()
	return c.client.send(&r)
}

// sendAck acknowledges a received full frame by echoing its timestamp.
func (c *Call) sendAck(f *FullFrame) error {
	_, err := c.sendFullTS(FrameIAX, uint64(IAXAck), f.Timestamp, nil)
	return err
}

// sequenced reports whether a full frame consumes a sequence number
// (RFC 5456 §8.1.1; same list as chan_iax2's "no sequence" exceptions).
func sequenced(f *FullFrame) bool {
	if f.Type != FrameIAX {
		return true
	}
	switch IAXSubclass(f.Subclass) {
	case IAXAck, IAXInval, IAXVnak, IAXTxCnt, IAXTxAcc, IAXTxRel:
		return false
	}
	return true
}

// Retransmission of a full frame awaiting its reply.
const (
	retransmitInterval = time.Second
	retransmitAttempts = 4
)

// await waits for an IAX frame whose subclass is one of want, retransmitting
// sent until it arrives. INVAL, REJECT, REGREJ and HANGUP end the wait with
// an error.
//
// Call tokens: a first-contact frame (NEW, REGREQ, REGREL, POKE) carries an
// empty CALLTOKEN IE to say we support them. The server then answers with a
// CALLTOKEN frame holding a token, and we resend the same frame with that
// token in place of the empty IE, without touching sequence numbers, as
// chan_iax2 does. A frame with no CALLTOKEN IE at all is rejected by
// default (requirecalltoken=yes).
func (c *Call) await(ctx context.Context, sent *FullFrame, want ...IAXSubclass) (*FullFrame, error) {
	retry := time.NewTimer(retransmitInterval)
	defer retry.Stop()
	attempts := 1
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.client.done:
			return nil, net.ErrClosed
		case <-retry.C:
			if attempts >= retransmitAttempts {
				return nil, fmt.Errorf("iax2: no reply to %s from %s after %d attempts",
					IAXSubclass(sent.Subclass), c.client.server, attempts)
			}
			attempts++
			if err := c.resend(sent); err != nil {
				return nil, err
			}
			retry.Reset(retransmitInterval)
		case f := <-c.inbox:
			if f.Type != FrameIAX {
				continue
			}
			sub := IAXSubclass(f.Subclass)
			for _, w := range want {
				if sub == w {
					return f, nil
				}
			}
			switch sub {
			case IAXCallToken:
				ies, err := f.IEs()
				if err != nil {
					return nil, err
				}
				tok, ok := ies.Get(IECallToken)
				if !ok {
					return nil, errors.New("iax2: CALLTOKEN frame without token")
				}
				with, err := withCallToken(sent.Data, tok)
				if err != nil {
					return nil, err
				}
				sent.Data = with
				if err := c.client.send(sent); err != nil {
					return nil, err
				}
			case IAXInval, IAXReject, IAXRegRej, IAXHangup:
				cause := ""
				if ies, err := f.IEs(); err == nil {
					cause = ies.String(IECause)
				}
				if cause == "" {
					return nil, fmt.Errorf("iax2: %s answered %s with %s", c.client.server, IAXSubclass(sent.Subclass), sub)
				}
				return nil, fmt.Errorf("iax2: %s answered %s with %s: %s", c.client.server, IAXSubclass(sent.Subclass), sub, cause)
			}
		}
	}
}

// withCallToken re-encodes an IE payload with its CALLTOKEN IE replaced.
func withCallToken(data []byte, tok IE) ([]byte, error) {
	ies, err := ParseIEs(data)
	if err != nil {
		return nil, err
	}
	out := make(IEs, 0, len(ies)+1)
	for _, ie := range ies {
		if ie.ID != IECallToken {
			out = append(out, ie)
		}
	}
	return append(out, tok).Encode()
}

// handleFull processes an inbound full frame addressed to this call. It
// learns the peer's call number, advances the inbound sequence number,
// answers PING and LAGRQ itself, delivers voice, acknowledges every other
// sequenced frame and queues signalling for await/run.
//
// TODO: out-of-order detection (VNAK), duplicate suppression, ACK matching
// against pending.
func (c *Call) handleFull(f *FullFrame) {
	// A CALLTOKEN reply comes from a fake call number 1 before the server
	// has created a call: it must not teach us anything.
	if f.Type == FrameIAX && IAXSubclass(f.Subclass) == IAXCallToken {
		c.queue(f)
		return
	}

	c.mu.Lock()
	if c.remoteNo == 0 && f.SrcCall != 0 {
		c.remoteNo = f.SrcCall
		c.client.bindRemote(c)
	}
	if sequenced(f) {
		c.iseqno = f.OSeqno + 1
	}
	if f.Timestamp > c.lastTS {
		c.lastTS = f.Timestamp
	}
	c.mu.Unlock()

	// Frames we answer with a sequenced reply are acknowledged by that reply
	// (its ISeqno), as in chan_iax2; everything else sequenced gets an ACK.
	answered := false
	switch f.Type {
	case FrameIAX:
		switch IAXSubclass(f.Subclass) {
		case IAXAuthReq, IAXRegAuth, IAXNew, IAXAuthRep:
			answered = true
		case IAXAccept:
			// Record the codec here, on the read goroutine, so that voice
			// frames following the ACCEPT closely are decoded with it.
			if ies, err := f.IEs(); err == nil {
				if format, err := ies.Format(IEFormat, IEFormat2); err == nil {
					c.mu.Lock()
					c.format = format
					c.mu.Unlock()
				}
			}
		case IAXPing:
			if _, err := c.sendFullTS(FrameIAX, uint64(IAXPong), f.Timestamp, nil); err != nil {
				c.client.log.Warn("pong", "err", err)
			}
			return
		case IAXLagRq:
			if _, err := c.sendFullTS(FrameIAX, uint64(IAXLagRp), f.Timestamp, nil); err != nil {
				c.client.log.Warn("lagrp", "err", err)
			}
			return
		}
	case FrameVoice:
		// The peer's full voice frame is authoritative for the codec.
		c.mu.Lock()
		c.format = Format(f.Subclass)
		c.mu.Unlock()
		c.deliverMedia(f.Timestamp, Format(f.Subclass), f.Data)
	}

	if sequenced(f) && !answered {
		if err := c.sendAck(f); err != nil {
			c.client.log.Warn("ack", "err", err)
		}
	}
	if f.Type != FrameVoice {
		c.queue(f)
	}
}

func (c *Call) queue(f *FullFrame) {
	select {
	case c.inbox <- f:
	default:
		c.client.log.Warn("inbox full, dropping", "call", c.localNo, "frame", f)
	}
}

// handleMini processes an inbound mini frame addressed to this call.
func (c *Call) handleMini(f *MiniFrame) {
	c.mu.Lock()
	ts := ExpandTimestamp(c.lastTS, f.Timestamp)
	if ts > c.lastTS {
		c.lastTS = ts
	}
	format := c.format
	c.mu.Unlock()
	c.deliverMedia(ts, format, f.Data)
}

func (c *Call) deliverMedia(ts uint32, format Format, data []byte) {
	c.mu.Lock()
	media := c.media
	c.mu.Unlock()
	if media == nil {
		return
	}
	select {
	case media <- MediaFrame{Timestamp: ts, Format: format, Data: data}:
	default:
		c.mu.Lock()
		c.mediaDropped++
		c.mu.Unlock()
	}
}

// run consumes signalling after the call is accepted, until it ends. It is
// the only writer and the closer of Events.
func (c *Call) run() {
	defer func() {
		c.setState(StateDown)
		c.client.release(c)
		close(c.Events)
		close(c.done)
	}()
	emit := func(ev Event) {
		select {
		case c.Events <- ev:
		default:
			c.client.log.Warn("events full, dropping", "call", c.localNo, "event", ev.Kind)
		}
	}
	for {
		select {
		case <-c.stop:
			return
		case <-c.client.done:
			return
		case f := <-c.inbox:
			switch f.Type {
			case FrameIAX:
				switch IAXSubclass(f.Subclass) {
				case IAXAck:
					select {
					case c.acks <- f.ISeqno:
					default:
					}
				case IAXHangup, IAXReject, IAXInval:
					ev := Event{Kind: EventHangup, Cause: IAXSubclass(f.Subclass).String()}
					if ies, err := f.IEs(); err == nil {
						if s := ies.String(IECause); s != "" {
							ev.Cause = s
						}
						ev.CauseCode, _ = ies.Uint8(IECauseCode)
					}
					emit(ev)
					return
				}
			case FrameControl:
				switch ControlSubclass(f.Subclass) {
				case ControlRinging:
					emit(Event{Kind: EventRinging})
				case ControlProgress, ControlProceeding:
					emit(Event{Kind: EventProceeding})
				case ControlAnswer:
					c.setState(StateUp)
					emit(Event{Kind: EventAnswered})
				case ControlHangup, ControlBusy, ControlCongestion:
					emit(Event{Kind: EventHangup, Cause: ControlSubclass(f.Subclass).String()})
					return
				default:
					c.client.log.Debug("ignoring control", "call", c.localNo, "frame", f)
				}
			case FrameDTMFBegin, FrameDTMFEnd:
				// Report the digit once: on DTMF_BEGIN when the peer sends
				// both frames, on DTMF_END alone when it only sends that
				// one, as the first IAX2 implementations did.
				digit := byte(f.Subclass) //nolint:gosec // ASCII digit
				if f.Type == FrameDTMFBegin {
					c.dtmfBegun = digit
					emit(Event{Kind: EventDTMF, Digit: digit})
				} else if c.dtmfBegun == digit {
					c.dtmfBegun = 0
				} else {
					emit(Event{Kind: EventDTMF, Digit: digit})
				}
			default:
				c.client.log.Debug("ignoring frame", "call", c.localNo, "frame", f)
			}
		}
	}
}

// SendVoice sends one encoded voice frame in the negotiated format. The
// first frame of a call is a full frame (it carries the codec); later ones
// go as mini frames unless the timestamp crossed a 16-bit boundary, in which
// case a full frame resynchronises the peer.
func (c *Call) SendVoice(data []byte) error {
	c.mu.Lock()
	if c.state != StateUp && c.state != StateAccepted {
		c.mu.Unlock()
		return ErrCallDown
	}
	ts := c.voiceTimestamp(len(data))
	full := !c.voiceStarted || ts&0xFFFF0000 != c.lastVoiceTS&0xFFFF0000
	c.voiceStarted = true
	c.lastVoiceTS = ts
	if !full {
		f := &MiniFrame{SrcCall: c.localNo, Timestamp: uint16(ts), Data: data} //nolint:gosec // low 16 bits by design
		c.mu.Unlock()
		return c.client.send(f)
	}
	format := c.format
	c.mu.Unlock()
	_, err := c.sendFullTS(FrameVoice, uint64(format), ts, data)
	return err
}

// voiceTimestampSkew is how far the wall clock may drift from the
// sample-count prediction before voice timestamps resynchronise to it.
const voiceTimestampSkew = 100

// voiceTimestamp returns the timestamp of the next voice frame of n bytes.
// Like chan_iax2, it predicts it from the previous frame plus the frame's
// duration so that frames captured in a burst still carry 20 ms spaced,
// monotonic timestamps, and falls back to the wall clock when the two drift
// apart (device paused, clock skew). Must be called with c.mu held.
func (c *Call) voiceTimestamp(n int) uint32 {
	ts := c.timestamp()
	if !c.voiceStarted {
		return ts
	}
	var frameMs uint32 = 20
	switch c.format {
	case FormatULAW, FormatALAW:
		if n > 0 {
			frameMs = uint32(n) / (G711SampleRate / 1000) //nolint:gosec // n > 0
		}
	}
	pred := c.lastVoiceTS + frameMs
	if d := int64(ts) - int64(pred); d > -voiceTimestampSkew && d < voiceTimestampSkew {
		return pred
	}
	if ts <= c.lastVoiceTS {
		return pred
	}
	return ts
}

// DTMFDuration is how long a digit sent with SendDTMF is held: the time
// between its DTMF_BEGIN and DTMF_END frames. Asterisk stretches anything
// shorter than its minimum (80 ms by default), so this only needs to be
// long enough to be heard as a key press by whatever is at the other end.
const DTMFDuration = 100 * time.Millisecond

// ErrBadDigit is returned by SendDTMF for a byte that is not a DTMF key.
var ErrBadDigit = errors.New("iax2: not a DTMF digit")

// ValidDigit reports whether b is a DTMF key: 0-9, *, # or A-D.
func ValidDigit(b byte) bool {
	return b >= '0' && b <= '9' || b == '*' || b == '#' || b >= 'A' && b <= 'D'
}

// digitKey upper-cases a-d and rejects anything that is not a key.
func digitKey(digit byte) (byte, error) {
	if digit >= 'a' && digit <= 'd' {
		digit -= 'a' - 'A'
	}
	if !ValidDigit(digit) {
		return 0, ErrBadDigit
	}
	return digit, nil
}

// SendDTMF sends a DTMF digit on an answered call: DTMF_BEGIN, then
// DTMF_END after DTMFDuration, each as a full frame carrying the ASCII
// digit as its subclass and acknowledged by the peer. Digits sent in a
// row are therefore spaced by at least DTMFDuration, and lower-case
// letters are accepted for A-D. It blocks for the duration of the digit.
func (c *Call) SendDTMF(digit byte) error {
	digit, err := digitKey(digit)
	if err != nil {
		return err
	}
	c.dtmfMu.Lock() // one digit at a time
	defer c.dtmfMu.Unlock()
	if err := c.dtmf(FrameDTMFBegin, digit); err != nil {
		return err
	}
	select {
	case <-time.After(DTMFDuration):
	case <-c.done:
		return ErrCallDown
	}
	return c.dtmf(FrameDTMFEnd, digit)
}

// DTMFBegin starts a digit that the user holds down: DTMF_BEGIN now,
// DTMF_END when DTMFEnd is called. The peer takes a second DTMF_BEGIN as
// the end of the previous digit, so the pair need not be kept strictly.
func (c *Call) DTMFBegin(digit byte) error {
	digit, err := digitKey(digit)
	if err != nil {
		return err
	}
	c.dtmfMu.Lock()
	defer c.dtmfMu.Unlock()
	return c.dtmf(FrameDTMFBegin, digit)
}

// DTMFEnd ends the digit started by DTMFBegin.
func (c *Call) DTMFEnd(digit byte) error {
	digit, err := digitKey(digit)
	if err != nil {
		return err
	}
	c.dtmfMu.Lock()
	defer c.dtmfMu.Unlock()
	return c.dtmf(FrameDTMFEnd, digit)
}

// dtmf sends one DTMF frame on an answered call and waits for its ACK.
// dtmfMu must be held.
func (c *Call) dtmf(typ FrameType, digit byte) error {
	if c.State() != StateUp {
		return ErrCallDown
	}
	f, err := c.sendFull(typ, uint64(digit), nil)
	if err != nil {
		return err
	}
	if err := c.waitAck(f); err != nil {
		return fmt.Errorf("iax2: %s %c not acknowledged by %s", typ, digit, c.client.server)
	}
	return nil
}

// ErrNotRinging is returned by Answer on a call that is not an incoming
// call waiting to be answered.
var ErrNotRinging = errors.New("iax2: call is not ringing")

// Answer accepts an incoming call: it sends CONTROL/ANSWER, waits for its
// ACK (resending once) and delivers the peer's voice frames to media.
func (c *Call) Answer(media chan<- MediaFrame) error {
	c.mu.Lock()
	if c.outgoing || c.state != StateRinging {
		c.mu.Unlock()
		return ErrNotRinging
	}
	c.media = media
	c.state = StateUp
	c.mu.Unlock()
	f, err := c.sendFull(FrameControl, uint64(ControlAnswer), nil)
	if err != nil {
		return err
	}
	if err := c.waitAck(f); err != nil {
		return fmt.Errorf("iax2: ANSWER not acknowledged by %s", c.client.server)
	}
	return nil
}

// Hangup ends the call: it sends HANGUP with the cause, waits briefly for
// the ACK (resending once) and stops the call. It is safe to call more than
// once and after the peer hung up.
func (c *Call) Hangup(cause uint8) error {
	c.mu.Lock()
	if c.state == StateDown || c.state == StateHangupSent || c.state == StateIdle {
		c.mu.Unlock()
		return nil
	}
	c.state = StateHangupSent
	c.mu.Unlock()
	defer c.stopOnce.Do(func() { close(c.stop) })

	ies := IEs{StringIE(IECause, CauseText(cause)), Uint8IE(IECauseCode, cause)}
	data, err := ies.Encode()
	if err != nil {
		return err
	}
	f, err := c.sendFull(FrameIAX, uint64(IAXHangup), data)
	if err != nil {
		return err
	}
	if err := c.waitAck(f); err != nil {
		return fmt.Errorf("iax2: HANGUP not acknowledged by %s", c.client.server)
	}
	return nil
}

// errNoAck is returned by waitAck when the peer never acknowledged.
var errNoAck = errors.New("no ack")

// waitAck waits for the ACK of f (an ACK whose ISeqno is past f's OSeqno)
// while run is consuming signalling, resending f once. It returns nil if
// the call ended meanwhile.
func (c *Call) waitAck(f *FullFrame) error {
	for attempt := 0; attempt < 2; attempt++ {
		timer := time.NewTimer(retransmitInterval)
		for {
			select {
			case iseq := <-c.acks:
				if iseq == f.OSeqno+1 {
					timer.Stop()
					return nil
				}
				continue
			case <-timer.C:
			case <-c.done:
				timer.Stop()
				return nil
			}
			break
		}
		if attempt == 0 {
			if err := c.resend(f); err != nil {
				return err
			}
		}
	}
	return errNoAck
}
