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

// Package phone is the softphone controller: one registration, one call at
// a time, the sound device bridged to it. It is what a user interface
// drives (the CLI, later a GUI), so that none of them has to know IAX2 or
// the audio engine.
//
// All methods are safe for concurrent use and return quickly; progress is
// reported on Events.
package phone

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/ppomes/iaxphone/audio"
	"github.com/ppomes/iaxphone/iax2"
)

// Errors returned by the controller.
var (
	ErrNotStarted = errors.New("phone: not started")
	ErrBusy       = errors.New("phone: a call is already in progress")
	ErrNoCall     = errors.New("phone: no call")
	ErrNotRinging = errors.New("phone: no incoming call to answer")
	ErrNotActive  = errors.New("phone: the call is not answered")
	ErrCodec      = errors.New("phone: unsupported codec")
)

// MinKeyDuration is how long a key pressed with PressKey sounds, and is
// held on the wire during a call, at least, however quickly it is released.
const MinKeyDuration = iax2.DTMFDuration

// KeyIdleClose is how long the sound device stays open for keypad tones
// after the last key is released when no call is in progress.
var KeyIdleClose = 2 * time.Second

// RegState is the state of our registration with the server.
type RegState int

// Registration states.
const (
	RegNone    RegState = iota // not registering (Config.Register is false)
	RegPending                 // registering, or retrying after a failure
	RegOK                      // registered
	RegFailed                  // last attempt failed; retrying
)

var regNames = [...]string{"none", "pending", "registered", "failed"}

func (s RegState) String() string {
	if int(s) < len(regNames) {
		return regNames[s]
	}
	return "RegState(?)"
}

// CallState is the state of the current call.
type CallState int

// Call states.
const (
	CallNone    CallState = iota
	CallDialing           // outgoing: waiting for the peer to accept and answer
	CallRinging           // incoming: waiting for Answer
	CallActive            // answered, media flowing
	CallEnded             // over; only seen in EventEnded
)

var callNames = [...]string{"none", "dialing", "ringing", "active", "ended"}

func (s CallState) String() string {
	if int(s) < len(callNames) {
		return callNames[s]
	}
	return "CallState(?)"
}

// Kind classifies events.
type Kind int

// Event kinds.
const (
	EventRegistration  Kind = iota + 1 // Event.Registration changed
	EventIncoming                      // an incoming call is ringing (Event.Call)
	EventDialing                       // an outgoing call was placed
	EventRemoteRinging                 // outgoing call: the peer is ringing
	EventAnswered                      // the call is up, either direction
	EventEnded                         // the call is over; Event.Call has the cause and statistics
	EventDTMF                          // the peer pressed a key (Event.Digit)
	EventHold                          // the call went on hold, by us or by the peer (Event.Call.OnHold, Held)
	EventUnhold                        // the call came back off hold
	EventTransfer                      // the peer asks us to call Event.Number instead; the call then ends
)

var kindNames = [...]string{"?", "registration", "incoming", "dialing", "remote-ringing", "answered", "ended", "dtmf",
	"hold", "unhold", "transfer"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "Kind(?)"
}

// Event is what the controller reports.
type Event struct {
	Kind         Kind
	Registration RegInfo  // EventRegistration
	Call         CallInfo // call events
	Digit        byte     // EventDTMF: '0'-'9', '*', '#' or 'A'-'D'
	Number       string   // EventTransfer: the number the peer asks us to call
}

// RegInfo is a snapshot of the registration.
type RegInfo struct {
	State        RegState
	Refresh      time.Duration
	ApparentAddr string
	Err          error // last failure, nil when registered
}

// CallInfo is a snapshot of the current (or just ended) call.
type CallInfo struct {
	Outgoing bool
	Number   string // dialled number, or the caller's number ("unknown" if absent)
	Name     string // caller's name (incoming)
	Format   iax2.Format
	State    CallState
	Started  time.Time
	Answered time.Time // zero until answered
	OnHold   bool      // we put the peer on hold (Hold)
	Held     bool      // the peer put us on hold
	// TransferredTo is the number we asked the server to transfer the call
	// to (Transfer); the server hangs up on us once it has done so.
	TransferredTo string

	// Set in EventEnded.
	HungUpByUs bool
	// Transferred says the server hung up on us after our transfer request
	// (TransferredTo). It stays false when the call ended some other way:
	// hung up by us, or lost because the server stopped answering.
	Transferred bool
	Cause       string // the peer's hangup cause, or why the call failed
	Err         error  // non-nil when the call failed (rejected, timeout...)
	Stats       audio.Stats
}

// TalkTime is how long the call has been (or was) answered.
func (c CallInfo) TalkTime(now time.Time) time.Duration {
	if c.Answered.IsZero() {
		return 0
	}
	return now.Sub(c.Answered).Round(time.Millisecond)
}

// Config configures a Phone.
type Config struct {
	// Server is the IAX2 server host[:port].
	Server string
	// Username and Secret are our account on the server.
	Username string
	Secret   string
	// CallerNumber and CallerName are sent on outgoing calls.
	CallerNumber string
	CallerName   string
	// Register keeps a registration alive, which is what makes the server
	// send us calls. Outgoing calls work without it.
	Register bool
	// OpenDevice opens the sound device for a call; nil opens the system's
	// default devices through miniaudio.
	OpenDevice func() (audio.Device, error)
	// JitterMs is the playout delay; 0 means audio.DefaultJitterMs.
	JitterMs int
	// LocalAddr optionally binds the UDP socket.
	LocalAddr string
	Logger    *slog.Logger
}

// Phone is the controller.
type Phone struct {
	cfg    Config
	log    *slog.Logger
	events chan Event

	mu     sync.Mutex
	client *iax2.Client
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	reg    RegInfo
	cur    *activeCall
	muted  bool
	closed bool
	holdMu sync.Mutex // Hold and Unhold, one at a time, state and event included

	key         *pressedKey       // the key held down, if any
	player      *audio.Player     // keypad tones outside a call
	playerTimer *time.Timer       // closes the idle player
	dtmfQueue   chan func() error // DTMF frames of the pressed keys, sent in order
}

// pressedKey is a key held down with PressKey.
type pressedKey struct {
	digit byte
	at    time.Time
	call  *iax2.Call  // the call it is sent on, nil outside a call
	timer *time.Timer // the end of a key released before MinKeyDuration
	ended bool
}

// activeCall is the call in progress with its audio.
type activeCall struct {
	call   *iax2.Call // nil while an outgoing NEW is pending
	cancel context.CancelFunc
	info   CallInfo
	media  chan iax2.MediaFrame
	dev    audio.Device
	engine *audio.Engine
	// peerHangup is set when the peer hung up on us, as opposed to the
	// call being dropped for lack of a reply.
	peerHangup bool
}

// New creates a phone; Start opens the socket.
func New(cfg Config) *Phone {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.JitterMs == 0 {
		cfg.JitterMs = audio.DefaultJitterMs
	}
	if cfg.OpenDevice == nil {
		cfg.OpenDevice = func() (audio.Device, error) {
			return audio.OpenMalgo(func(msg string) { cfg.Logger.Debug("miniaudio", "msg", msg) })
		}
	}
	return &Phone{cfg: cfg, log: cfg.Logger, events: make(chan Event, 64)}
}

// Events delivers the controller's events. It is closed by Close.
func (p *Phone) Events() <-chan Event { return p.events }

// Start opens the socket, registers when configured and starts listening
// for incoming calls. It returns once the socket is open.
func (p *Phone) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil || p.closed {
		return errors.New("phone: already started")
	}
	client, err := iax2.Dial(ctx, iax2.Config{
		Server:       p.cfg.Server,
		Username:     p.cfg.Username,
		Secret:       p.cfg.Secret,
		CallerNumber: p.cfg.CallerNumber,
		CallerName:   p.cfg.CallerName,
		LocalAddr:    p.cfg.LocalAddr,
		Logger:       p.log,
	})
	if err != nil {
		return err
	}
	p.client = client
	p.ctx, p.cancel = context.WithCancel(ctx)
	p.dtmfQueue = make(chan func() error, 64)
	p.wg.Add(2)
	go p.listen()
	go p.sendKeys()
	if p.cfg.Register {
		p.reg = RegInfo{State: RegPending}
		p.wg.Add(1)
		go p.register()
	}
	return nil
}

// Close hangs up, unregisters, closes the socket and the Events channel.
// It is safe to call more than once.
func (p *Phone) Close() error {
	p.mu.Lock()
	client, cancel := p.client, p.cancel
	if client == nil {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	_ = p.Hangup()
	cancel()
	p.wg.Wait()
	err := client.Close()
	p.mu.Lock()
	p.client = nil
	p.closePlayer()
	p.mu.Unlock()
	close(p.events)
	return err
}

// LocalAddr returns the socket address, "" before Start.
func (p *Phone) LocalAddr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == nil {
		return ""
	}
	return p.client.LocalAddr().String()
}

// Registration returns the registration state.
func (p *Phone) Registration() RegInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reg
}

// Call returns the current call, and false when there is none.
func (p *Phone) Call() (CallInfo, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur == nil {
		return CallInfo{}, false
	}
	return p.cur.info, true
}

// Muted reports whether the microphone is muted.
func (p *Phone) Muted() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.muted
}

// SetMuted mutes or unmutes the microphone, for this and later calls.
func (p *Phone) SetMuted(muted bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.muted = muted
	if p.cur != nil && p.cur.engine != nil {
		p.cur.engine.SetMuted(muted)
	}
}

// Levels returns the peak capture and playback levels in dBFS since the
// previous call, −∞ when no audio is running. Outside a call the playback
// level is that of the keypad tones.
func (p *Phone) Levels() (in, out float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur == nil || p.cur.engine == nil {
		out = math.Inf(-1)
		if p.player != nil {
			out = p.player.Level()
		}
		return math.Inf(-1), out
	}
	return p.cur.engine.Levels()
}

// Stats returns the audio statistics of the current call.
func (p *Phone) Stats() audio.Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur == nil || p.cur.engine == nil {
		return audio.Stats{}
	}
	return p.cur.engine.Stats()
}

// Dial places a call. It returns once the NEW is on its way; EventDialing,
// EventRemoteRinging, EventAnswered and EventEnded follow.
func (p *Phone) Dial(number string) error {
	p.mu.Lock()
	if p.client == nil {
		p.mu.Unlock()
		return ErrNotStarted
	}
	if p.cur != nil {
		p.mu.Unlock()
		return ErrBusy
	}
	ctx, cancel := context.WithCancel(p.ctx)
	ac := &activeCall{
		cancel: cancel,
		info:   CallInfo{Outgoing: true, Number: number, State: CallDialing, Started: time.Now()},
		media:  make(chan iax2.MediaFrame, 256),
	}
	p.cur = ac
	client := p.client
	p.mu.Unlock()
	p.emit(Event{Kind: EventDialing, Call: ac.info})

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		call, err := client.Call(ctx, number, ac.media)
		if err != nil {
			p.endCall(ac, err)
			return
		}
		p.mu.Lock()
		ac.call = call
		ac.info.Format = call.Format()
		err = p.startAudio(ac)
		p.mu.Unlock()
		if err != nil {
			_ = call.Hangup(iax2.CauseCongestion)
			p.endCall(ac, err)
			return
		}
		p.watch(ac)
	}()
	return nil
}

// Answer takes the ringing incoming call.
func (p *Phone) Answer() error {
	p.mu.Lock()
	ac := p.cur
	if ac == nil || ac.info.State != CallRinging {
		p.mu.Unlock()
		return ErrNotRinging
	}
	if err := p.startAudio(ac); err != nil {
		p.mu.Unlock()
		_ = ac.call.Hangup(iax2.CauseCongestion)
		return err
	}
	ac.engine.SetTone(nil)
	ac.info.State = CallActive
	ac.info.Answered = time.Now()
	p.mu.Unlock()
	if err := ac.call.Answer(ac.media); err != nil {
		return err
	}
	p.emit(Event{Kind: EventAnswered, Call: p.snapshot(ac)})
	return nil
}

// Hangup ends the current call (or declines the ringing one). It is a
// no-op without a call.
func (p *Phone) Hangup() error {
	p.mu.Lock()
	ac := p.cur
	if ac == nil {
		p.mu.Unlock()
		return nil
	}
	ac.info.HungUpByUs = true
	call, state := ac.call, ac.info.State
	p.mu.Unlock()
	if call == nil {
		ac.cancel() // NEW pending: client.Call aborts it
		return nil
	}
	cause := uint8(iax2.CauseNormalClearing)
	if state == CallRinging {
		cause = iax2.CauseCallRejected
	}
	return call.Hangup(cause)
}

// active returns the current call when it is answered.
func (p *Phone) active() (*activeCall, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ac := p.cur
	if ac == nil || ac.call == nil {
		return nil, ErrNoCall
	}
	if ac.info.State != CallActive {
		return nil, ErrNotActive
	}
	return ac, nil
}

// Hold puts the active call on hold: the peer stops sending its audio and
// may play music to whoever is at the other end, and we send nothing. It
// returns once the peer has acknowledged, with EventHold on the way, and
// does nothing on a call already on hold.
func (p *Phone) Hold() error { return p.setHold(true) }

// Unhold takes the active call back off hold; EventUnhold follows.
func (p *Phone) Unhold() error { return p.setHold(false) }

// setHold changes the hold state of the active call, one change at a time
// so that they take effect in the order asked. A call that ended while
// the peer was acknowledging gets no event: its EventEnded is the last.
func (p *Phone) setHold(on bool) error {
	p.holdMu.Lock()
	defer p.holdMu.Unlock()
	ac, err := p.active()
	if err != nil {
		return err
	}
	p.mu.Lock()
	same := ac.info.OnHold == on
	p.mu.Unlock()
	if same {
		return nil
	}
	if on {
		err = ac.call.Hold()
	} else {
		err = ac.call.Unhold()
	}
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.cur != ac || ac.info.State != CallActive {
		p.mu.Unlock()
		return ErrNoCall
	}
	ac.info.OnHold = on
	info := ac.info
	p.mu.Unlock()
	kind := EventUnhold
	if on {
		kind = EventHold
	}
	p.emit(Event{Kind: kind, Call: info})
	return nil
}

// Transfer asks the server to send whoever is at the other end of the
// active call to number (a blind transfer) and returns once the server has
// acknowledged. The server then ends our call: EventEnded follows, with
// TransferredTo set. The acknowledgement says the server took the request,
// not that it could carry it out: a call that is not bridged goes on.
func (p *Phone) Transfer(number string) error {
	ac, err := p.active()
	if err != nil {
		return err
	}
	// Recorded before sending: the server may hang up right behind its ACK.
	p.mu.Lock()
	ac.info.TransferredTo = number
	p.mu.Unlock()
	if err := ac.call.Transfer(number); err != nil {
		p.mu.Lock()
		ac.info.TransferredTo = ""
		p.mu.Unlock()
		return err
	}
	return nil
}

// SendDTMF presses a key on the active call: '0'-'9', '*', '#' or 'A'-'D'.
// It returns once the peer has acknowledged the key release, after
// iax2.DTMFDuration, so a GUI should call it off its event loop. Digits
// are serialised: concurrent calls go out one after the other.
func (p *Phone) SendDTMF(digit byte) error {
	p.mu.Lock()
	ac := p.cur
	var state CallState
	if ac != nil {
		state = ac.info.State
	}
	p.mu.Unlock()
	if ac == nil || ac.call == nil {
		return ErrNoCall
	}
	if state != CallActive {
		return ErrNotActive
	}
	return ac.call.SendDTMF(digit)
}

// PressKey holds a keypad key down until ReleaseKey: its tone sounds on
// the device, and during a call DTMF_BEGIN is sent now and DTMF_END on the
// release. Outside a call the device is opened for the tone and closed
// KeyIdleClose after the last key. Pressing a key releases the previous
// one. It returns ErrNotActive during a call that is not answered.
func (p *Phone) PressKey(key byte) error {
	if key >= 'a' && key <= 'd' {
		key -= 'a' - 'A'
	}
	tone := audio.DTMFTone(key)
	if tone == nil {
		return iax2.ErrBadDigit
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == nil {
		return ErrNotStarted
	}
	if p.key != nil {
		p.endKey(p.key)
	}
	k := &pressedKey{digit: key, at: time.Now()}
	switch {
	case p.cur == nil:
		if err := p.startPlayer(); err != nil {
			return err
		}
		p.player.SetTone(tone)
	case p.cur.info.State == CallActive:
		if p.cur.engine != nil {
			p.cur.engine.SetTone(tone)
		}
		k.call = p.cur.call
		call := k.call
		p.queueKey(func() error { return call.DTMFBegin(key) })
	default:
		return ErrNotActive
	}
	p.key = k
	return nil
}

// ReleaseKey releases the key held by PressKey; the key goes on for what
// is left of MinKeyDuration. It does nothing when no key is held.
func (p *Phone) ReleaseKey() {
	p.mu.Lock()
	defer p.mu.Unlock()
	k := p.key
	if k == nil || k.timer != nil {
		return
	}
	if left := MinKeyDuration - time.Since(k.at); left > 0 {
		k.timer = time.AfterFunc(left, func() {
			p.mu.Lock()
			p.endKey(k)
			p.mu.Unlock()
		})
		return
	}
	p.endKey(k)
}

// endKey stops the tone of k and sends its DTMF_END; p.mu must be held.
func (p *Phone) endKey(k *pressedKey) {
	if k.ended {
		return
	}
	k.ended = true
	if k.timer != nil {
		k.timer.Stop()
	}
	if p.key == k {
		p.key = nil
	}
	if k.call == nil {
		if p.player != nil {
			p.player.SetTone(nil)
			p.playerTimer = time.AfterFunc(KeyIdleClose, p.closeIdlePlayer)
		}
		return
	}
	if p.cur != nil && p.cur.call == k.call && p.cur.engine != nil {
		p.cur.engine.SetTone(nil)
	}
	call, digit := k.call, k.digit
	p.queueKey(func() error { return call.DTMFEnd(digit) })
}

// queueKey hands a DTMF frame to sendKeys; p.mu must be held.
func (p *Phone) queueKey(send func() error) {
	select {
	case p.dtmfQueue <- send:
	default:
		p.log.Warn("DTMF frame dropped, the peer is not acknowledging")
	}
}

// sendKeys sends the DTMF frames of the pressed keys one after the other,
// in order, off the caller's goroutine: each waits for its ACK.
func (p *Phone) sendKeys() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case send := <-p.dtmfQueue:
			if err := send(); err != nil && !errors.Is(err, iax2.ErrCallDown) {
				p.log.Warn("dtmf", "err", err)
			}
		}
	}
}

// startPlayer opens the device for keypad tones outside a call, unless it
// is open already; p.mu must be held.
func (p *Phone) startPlayer() error {
	if p.playerTimer != nil {
		p.playerTimer.Stop()
		p.playerTimer = nil
	}
	if p.player != nil {
		return nil
	}
	dev, err := p.cfg.OpenDevice()
	if err != nil {
		return err
	}
	player := audio.NewPlayer(dev)
	if err := player.Start(); err != nil {
		_ = dev.Close()
		return err
	}
	p.player = player
	return nil
}

// closePlayer closes the keypad tone device; p.mu must be held.
func (p *Phone) closePlayer() {
	if p.playerTimer != nil {
		p.playerTimer.Stop()
		p.playerTimer = nil
	}
	if p.player != nil {
		_ = p.player.Stop()
		p.player = nil
	}
}

// closeIdlePlayer runs KeyIdleClose after the last key outside a call.
func (p *Phone) closeIdlePlayer() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.key == nil {
		p.closePlayer()
	}
}

// listen takes the calls the server offers us.
func (p *Phone) listen() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case call := <-p.client.Incoming:
			p.incoming(call)
		}
	}
}

func (p *Phone) incoming(call *iax2.Call) {
	p.mu.Lock()
	if p.cur != nil {
		p.mu.Unlock()
		p.log.Info("incoming call while busy, answering busy", "from", call.CallerNumber())
		_ = call.Hangup(iax2.CauseUserBusy)
		return
	}
	number := call.CallerNumber()
	if number == "" {
		number = "unknown"
	}
	ac := &activeCall{
		call:   call,
		cancel: func() {},
		info: CallInfo{Number: number, Name: call.CallerName(), Format: call.Format(),
			State: CallRinging, Started: time.Now()},
		media: make(chan iax2.MediaFrame, 256),
	}
	p.cur = ac
	// Ring now; if the device cannot be opened, Answer tries again.
	if err := p.startAudio(ac); err != nil {
		p.log.Warn("cannot ring", "err", err)
	} else {
		ac.engine.SetTone(audio.RingTone())
	}
	p.mu.Unlock()
	p.emit(Event{Kind: EventIncoming, Call: ac.info})
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.watch(ac)
	}()
}

// startAudio opens the device and the engine of a call. p.mu must be held.
func (p *Phone) startAudio(ac *activeCall) error {
	if ac.engine != nil {
		return nil
	}
	codec, err := codecFor(ac.info.Format)
	if err != nil {
		return err
	}
	p.closePlayer() // the call's engine takes the device over
	dev, err := p.cfg.OpenDevice()
	if err != nil {
		return err
	}
	call := ac.call
	engine := audio.NewEngine(dev, codec, p.cfg.JitterMs, func(payload []byte) error {
		if call.State() != iax2.StateUp {
			return audio.ErrSkipped
		}
		if err := call.SendVoice(payload); !errors.Is(err, iax2.ErrOnHold) {
			return err
		}
		return audio.ErrSkipped
	})
	engine.SetMuted(p.muted)
	if err := engine.Start(); err != nil {
		_ = dev.Close()
		return err
	}
	ac.dev, ac.engine = dev, engine
	return nil
}

// watch forwards media to the engine and the call's events to ours until
// the call ends.
func (p *Phone) watch(ac *activeCall) {
	call := ac.call
	for {
		select {
		case m := <-ac.media:
			p.mu.Lock()
			engine := ac.engine
			p.mu.Unlock()
			if engine != nil {
				engine.Receive(m.Timestamp, m.Data)
			}
		case ev, ok := <-call.Events:
			if !ok {
				p.endCall(ac, nil)
				return
			}
			switch ev.Kind {
			case iax2.EventRinging:
				p.mu.Lock()
				if ac.engine != nil {
					ac.engine.SetTone(audio.RingTone())
				}
				p.mu.Unlock()
				p.emit(Event{Kind: EventRemoteRinging, Call: p.snapshot(ac)})
			case iax2.EventAnswered:
				p.mu.Lock()
				if ac.engine != nil {
					ac.engine.SetTone(nil)
				}
				ac.info.State = CallActive
				ac.info.Answered = time.Now()
				p.mu.Unlock()
				p.emit(Event{Kind: EventAnswered, Call: p.snapshot(ac)})
			case iax2.EventDTMF:
				p.emit(Event{Kind: EventDTMF, Call: p.snapshot(ac), Digit: ev.Digit})
			case iax2.EventHold, iax2.EventUnhold:
				p.mu.Lock()
				ac.info.Held = ev.Kind == iax2.EventHold
				p.mu.Unlock()
				kind := EventHold
				if !ac.info.Held {
					kind = EventUnhold
				}
				p.emit(Event{Kind: kind, Call: p.snapshot(ac)})
			case iax2.EventTransfer:
				p.emit(Event{Kind: EventTransfer, Call: p.snapshot(ac), Number: ev.Number})
			case iax2.EventHangup:
				p.mu.Lock()
				ac.info.Cause = ev.Cause
				if ev.CauseCode != 0 {
					ac.info.Cause = fmt.Sprintf("%s (%d)", ev.Cause, ev.CauseCode)
				}
				ac.peerHangup = !ev.Local
				p.mu.Unlock()
			}
		}
	}
}

// endCall tears down the audio and reports the end of the call.
func (p *Phone) endCall(ac *activeCall, err error) {
	p.mu.Lock()
	if p.cur != ac {
		p.mu.Unlock()
		return
	}
	p.cur = nil
	ac.cancel()
	if k := p.key; k != nil && k.call == ac.call {
		// Too late for its DTMF_END.
		k.ended = true
		if k.timer != nil {
			k.timer.Stop()
		}
		p.key = nil
	}
	if ac.engine != nil {
		ac.info.Stats = ac.engine.Stats()
		_ = ac.engine.Stop()
	} else if ac.dev != nil {
		_ = ac.dev.Close()
	}
	ac.info.State = CallEnded
	switch {
	case ac.info.HungUpByUs:
		ac.info.Err = nil
		if ac.info.Cause == "" {
			ac.info.Cause = "hung up"
		}
	case err != nil:
		ac.info.Err = err
		ac.info.Cause = err.Error()
	case ac.peerHangup:
		ac.info.Transferred = ac.info.TransferredTo != ""
	}
	info := ac.info
	p.mu.Unlock()
	p.emit(Event{Kind: EventEnded, Call: info})
}

func (p *Phone) snapshot(ac *activeCall) CallInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return ac.info
}

func (p *Phone) emit(ev Event) {
	select {
	case p.events <- ev:
	default:
		p.log.Warn("event dropped, nobody is reading Events", "kind", ev.Kind)
	}
}

// register keeps the registration alive and reports its state.
func (p *Phone) register() {
	defer p.wg.Done()
	_ = p.client.RegisterWith(p.ctx, func(reg *iax2.Registration, err error) {
		p.mu.Lock()
		if err != nil {
			p.reg = RegInfo{State: RegFailed, Err: err}
		} else {
			p.reg = RegInfo{State: RegOK, Refresh: reg.Refresh, ApparentAddr: reg.ApparentAddr}
		}
		info := p.reg
		p.mu.Unlock()
		p.emit(Event{Kind: EventRegistration, Registration: info})
	})
}

func codecFor(f iax2.Format) (audio.Codec, error) {
	switch f {
	case iax2.FormatULAW:
		return audio.Ulaw{}, nil
	case iax2.FormatALAW:
		return audio.Alaw{}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrCodec, f)
	}
}
