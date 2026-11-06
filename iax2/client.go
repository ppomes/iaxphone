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
	"log/slog"
	"net"
	"sync"
	"time"
)

// Config configures a Client.
type Config struct {
	// Server is the peer host[:port]; the port defaults to DefaultPort.
	Server string
	// Username and Secret authenticate registrations and calls.
	Username string
	Secret   string
	// CallerID fields sent in NEW (CALLING_NUMBER / CALLING_NAME).
	CallerNumber string
	CallerName   string
	// Capability is the set of codecs we accept; Preferred is the one we ask
	// for in the FORMAT IE. Defaults: ULAW|ALAW, ULAW.
	Capability Format
	Preferred  Format
	// RegisterRefresh is the registration lifetime requested from the
	// server (REFRESH IE). Default 60 s.
	RegisterRefresh time.Duration
	// LocalAddr optionally binds the UDP socket ("0.0.0.0:0" by default).
	LocalAddr string
	Logger    *slog.Logger
}

func (c *Config) withDefaults() Config {
	cfg := *c
	if cfg.Capability == 0 {
		cfg.Capability = FormatULAW | FormatALAW
	}
	if cfg.Preferred == 0 {
		cfg.Preferred = FormatULAW
	}
	if cfg.RegisterRefresh == 0 {
		cfg.RegisterRefresh = 60 * time.Second
	}
	if cfg.LocalAddr == "" {
		cfg.LocalAddr = "0.0.0.0:0"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if _, _, err := net.SplitHostPort(cfg.Server); err != nil {
		cfg.Server = net.JoinHostPort(cfg.Server, fmt.Sprint(DefaultPort))
	}
	return cfg
}

// Client owns one UDP socket and the calls multiplexed on it. One Client
// talks to one server; the call number space (1..32767) is per socket.
type Client struct {
	cfg    Config
	conn   *net.UDPConn
	server *net.UDPAddr
	log    *slog.Logger

	mu       sync.Mutex
	calls    map[uint16]*Call // keyed by local call number
	byRemote map[uint16]*Call // keyed by the peer's call number (mini frames)
	nextNo   uint16

	// Incoming receives the calls the server offers us, already accepted
	// and ringing. The user must Answer or Hangup them; a call nobody takes
	// off this channel is answered busy.
	Incoming chan *Call

	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// Dial resolves the server, opens the socket and starts the read loop. It
// does not register; see Register.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()
	server, err := net.ResolveUDPAddr("udp", cfg.Server)
	if err != nil {
		return nil, fmt.Errorf("iax2: resolve %q: %w", cfg.Server, err)
	}
	local, err := net.ResolveUDPAddr("udp", cfg.LocalAddr)
	if err != nil {
		return nil, fmt.Errorf("iax2: resolve local %q: %w", cfg.LocalAddr, err)
	}
	conn, err := net.ListenUDP("udp", local)
	if err != nil {
		return nil, fmt.Errorf("iax2: listen: %w", err)
	}
	c := &Client{
		cfg:      cfg,
		conn:     conn,
		server:   server,
		log:      cfg.Logger.With("server", server.String()),
		calls:    make(map[uint16]*Call),
		byRemote: make(map[uint16]*Call),
		nextNo:   1,
		Incoming: make(chan *Call, 4),
		done:     make(chan struct{}),
	}
	c.wg.Add(1)
	go c.readLoop()
	_ = ctx // TODO: ctx cancellation closes the client
	return c, nil
}

// LocalAddr returns the bound UDP address.
func (c *Client) LocalAddr() net.Addr { return c.conn.LocalAddr() }

// Close ends every call and closes the socket. Calls in progress are not
// hung up on the wire: the caller is expected to have done that.
func (c *Client) Close() error {
	err := net.ErrClosed
	c.closeOnce.Do(func() {
		close(c.done)
		c.mu.Lock()
		calls := make([]*Call, 0, len(c.calls))
		for _, call := range c.calls {
			calls = append(calls, call)
		}
		c.mu.Unlock()
		for _, call := range calls {
			call.stopRetransmits()
		}
		err = c.conn.Close()
		c.wg.Wait()
	})
	return err
}

// Call places an outgoing call to number and returns once the server has
// accepted it (ACCEPT received, codec negotiated) or rejected it. Voice
// frames from the peer are delivered on media (may be nil); RINGING, ANSWER
// and the peer's HANGUP arrive on Call.Events.
func (c *Client) Call(ctx context.Context, number string, media chan<- MediaFrame) (*Call, error) {
	call := c.newCall()
	call.media = media
	call.outgoing = true
	call.called = number

	req := IEs{
		Uint16IE(IEVersion, ProtocolVersion),
		StringIE(IECalledNumber, number),
	}
	if c.cfg.CallerNumber != "" {
		req = append(req, StringIE(IECallingNumber, c.cfg.CallerNumber))
	}
	req = append(req,
		StringIE(IECallingName, c.cfg.CallerName),
		Uint8IE(IECallingPres, 0), // presentation allowed
		Uint8IE(IECallingTON, 0),  // unknown type of number
		Uint16IE(IECallingTNS, 0), // no transit network
		StringIE(IEUsername, c.cfg.Username),
		Uint32IE(IEFormat, uint32(c.cfg.Preferred)),      //nolint:gosec // low 32 bits of the mask
		Uint32IE(IECapability, uint32(c.cfg.Capability)), //nolint:gosec
		Format64IE(IEFormat2, c.cfg.Preferred),
		Format64IE(IECapability2, c.cfg.Capability),
		Uint32IE(IEDateTime, EncodeDateTime(time.Now())),
		EmptyIE(IECallToken),
	)
	data, err := req.Encode()
	if err != nil {
		c.release(call)
		return nil, err
	}
	call.setState(StateNewSent)
	sent, err := call.sendFull(FrameIAX, uint64(IAXNew), data)
	if err != nil {
		c.release(call)
		return nil, err
	}
	reply, err := call.await(ctx, sent, IAXAuthReq, IAXAccept)
	if err != nil {
		c.abort(call, ctx.Err() != nil)
		return nil, err
	}

	if IAXSubclass(reply.Subclass) == IAXAuthReq {
		ies, err := reply.IEs()
		if err != nil {
			c.abort(call, true)
			return nil, err
		}
		methods, _ := ies.Uint16(IEAuthMethods)
		if AuthMethod(methods)&AuthMD5 == 0 {
			c.abort(call, true)
			return nil, fmt.Errorf("iax2: server offers auth methods %#x, only MD5 is supported", methods)
		}
		challenge := ies.String(IEChallenge)
		if challenge == "" {
			c.abort(call, true)
			return nil, errors.New("iax2: AUTHREQ without CHALLENGE")
		}
		rep := IEs{StringIE(IEMD5Result, MD5Response(challenge, c.cfg.Secret))}
		data, err := rep.Encode()
		if err != nil {
			c.abort(call, true)
			return nil, err
		}
		call.setState(StateAuthSent)
		sent, err = call.sendFull(FrameIAX, uint64(IAXAuthRep), data)
		if err != nil {
			c.abort(call, true)
			return nil, err
		}
		reply, err = call.await(ctx, sent, IAXAccept, IAXAuthReq)
		if err != nil {
			c.abort(call, ctx.Err() != nil)
			return nil, err
		}
		if IAXSubclass(reply.Subclass) == IAXAuthReq {
			c.abort(call, true)
			return nil, ErrAuth
		}
	}

	// handleFull recorded the FORMAT of the ACCEPT; validate it.
	format := call.Format()
	if format == 0 {
		c.abort(call, true)
		return nil, errors.New("iax2: ACCEPT without a usable FORMAT")
	}
	if format&c.cfg.Capability == 0 || format&(format-1) != 0 {
		c.abort(call, true)
		return nil, fmt.Errorf("iax2: server chose format %s, not in our capability %s", format, c.cfg.Capability)
	}
	call.setState(StateAccepted)
	go call.run()
	return call, nil
}

// abort tears down a call that failed during setup. When hangup is set a
// HANGUP is sent, and retransmitted until acknowledged, so the server does
// not keep the call around.
func (c *Client) abort(call *Call, hangup bool) {
	if hangup && call.RemoteNumber() != 0 {
		ies := IEs{StringIE(IECause, CauseText(CauseCallRejected)), Uint8IE(IECauseCode, CauseCallRejected)}
		data, _ := ies.Encode()
		_, _ = call.sendFull(FrameIAX, uint64(IAXHangup), data)
	}
	call.end()
}

// Ping sends an IAX POKE on a fresh call number and waits for the PONG,
// returning the round-trip time. POKE is the out-of-call liveness check of
// IAX2 (a PING on an unknown call number is answered with INVAL); the server
// answers with a PONG carrying our timestamp, which we ACK.
func (c *Client) Ping(ctx context.Context) (time.Duration, error) {
	call := c.newCall()
	defer c.release(call)

	start := time.Now()
	data, err := (IEs{EmptyIE(IECallToken)}).Encode()
	if err != nil {
		return 0, err
	}
	poke, err := call.sendFull(FrameIAX, uint64(IAXPoke), data)
	if err != nil {
		return 0, err
	}
	if _, err := call.await(ctx, poke, IAXPong); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// newCall allocates a local call number.
func (c *Client) newCall() *Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		no := c.nextNo
		c.nextNo++
		if c.nextNo > MaxCallNumber {
			c.nextNo = 1
		}
		if _, used := c.calls[no]; !used {
			call := newCallState(c, no)
			c.calls[no] = call
			return call
		}
	}
}

// release frees a call number and stops the call's retransmissions. Call
// numbers are handed out round-robin over the whole 15-bit space, so a late
// frame from the peer for a released call finds no call and is answered
// with INVAL rather than mistaken for a new one.
func (c *Client) release(call *Call) {
	call.stopRetransmits()
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.calls, call.localNo)
	if remote := call.remoteNo; remote != 0 && c.byRemote[remote] == call {
		delete(c.byRemote, remote)
	}
}

// bindRemote indexes a call by the peer's call number, which is all a mini
// frame carries. Called with call.mu held.
func (c *Client) bindRemote(call *Call) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byRemote[call.remoteNo] = call
}

// send writes one frame to the server.
func (c *Client) send(f Frame) error {
	buf, err := f.AppendTo(make([]byte, 0, 1500))
	if err != nil {
		return err
	}
	if _, mini := f.(*MiniFrame); !mini {
		c.log.Debug("send", "frame", f)
	}
	_, err = c.conn.WriteToUDP(buf, c.server)
	return err
}

// readLoop receives datagrams and dispatches them to calls by the
// destination call number carried in the frame (our local number).
func (c *Client) readLoop() {
	defer c.wg.Done()
	buf := make([]byte, 65535)
	for {
		n, from, err := c.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-c.done:
				return
			default:
				c.log.Error("read", "err", err)
				return
			}
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		f, err := Parse(pkt)
		if err != nil {
			c.log.Warn("drop", "from", from, "err", err)
			continue
		}
		if _, mini := f.(*MiniFrame); !mini {
			c.log.Debug("recv", "frame", f)
		}
		c.dispatch(f)
	}
}

func (c *Client) dispatch(f Frame) {
	switch f := f.(type) {
	case *FullFrame:
		if f.DstCall == 0 {
			if f.Type == FrameIAX && IAXSubclass(f.Subclass) == IAXPoke {
				c.answerPoke(f)
				return
			}
			if f.Type == FrameIAX && IAXSubclass(f.Subclass) == IAXNew {
				c.handleNew(f)
				return
			}
			c.log.Info("unsolicited full frame", "frame", f)
			return
		}
		if call := c.lookup(f.DstCall); call != nil {
			call.handleFull(f)
			return
		}
		// A call we no longer have: tell the peer so it stops
		// retransmitting (RFC 5456 §6.9.3). Replies that expect nothing
		// back are just dropped, as chan_iax2 does.
		if f.Type == FrameIAX {
			switch IAXSubclass(f.Subclass) {
			case IAXAck, IAXInval, IAXVnak, IAXTxCnt, IAXTxAcc, IAXTxRel:
				c.log.Debug("frame for unknown call", "frame", f)
				return
			}
		}
		c.log.Info("frame for unknown call, INVAL", "frame", f)
		inval := &FullFrame{SrcCall: f.DstCall, DstCall: f.SrcCall, Type: FrameIAX, Subclass: uint64(IAXInval)}
		if err := c.send(inval); err != nil {
			c.log.Warn("inval", "err", err)
		}
	case *MiniFrame:
		// Mini frames carry the peer's call number only.
		c.mu.Lock()
		call := c.byRemote[f.SrcCall]
		c.mu.Unlock()
		if call != nil {
			call.handleMini(f)
		}
	}
}

// answerPoke replies PONG to a POKE from the server (qualify=yes). The
// server ACKs the PONG; the transient call is released shortly after.
func (c *Client) answerPoke(poke *FullFrame) {
	call := c.newCall()
	call.mu.Lock()
	call.remoteNo = poke.SrcCall
	call.iseqno = poke.OSeqno + 1
	call.mu.Unlock()
	pong := &FullFrame{
		SrcCall: call.localNo, DstCall: poke.SrcCall, Timestamp: poke.Timestamp,
		OSeqno: 0, ISeqno: call.iseqno, Type: FrameIAX, Subclass: uint64(IAXPong),
	}
	if err := c.send(pong); err != nil {
		c.log.Warn("pong", "err", err)
	}
	time.AfterFunc(2*time.Second, func() { c.release(call) })
}

func (c *Client) lookup(no uint16) *Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[no]
}
