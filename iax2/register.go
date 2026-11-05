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
	"time"
)

// ErrAuth is returned when the server does not accept our credentials.
var ErrAuth = errors.New("iax2: authentication failed")

// Registration is what the server told us in its REGACK.
type Registration struct {
	// Refresh is the lifetime granted by the server; we must re-register
	// before it elapses.
	Refresh time.Duration
	// ApparentAddr is our address as seen by the server (APPARENT_ADDR),
	// empty when absent or not understood.
	ApparentAddr string
	// ServerTime is the server's local time (DATETIME), zero when absent.
	ServerTime time.Time
	// MsgCount is the raw MSGCOUNT IE (voicemail), 0xFFFF when unknown.
	MsgCount uint16
}

// RegisterOnce performs one registration exchange and returns the server's
// answer. It does not keep the registration alive; see Register.
func (c *Client) RegisterOnce(ctx context.Context) (*Registration, error) {
	return c.registration(ctx, IAXRegReq)
}

// Unregister releases the registration (REGREL).
func (c *Client) Unregister(ctx context.Context) error {
	_, err := c.registration(ctx, IAXRegRel)
	return err
}

// Register registers and re-registers at 5/6 of the granted refresh until
// ctx is cancelled, then releases the registration. A failed attempt is
// retried after 10 s. It returns nil when ctx is cancelled.
func (c *Client) Register(ctx context.Context) error {
	return c.RegisterWith(ctx, nil)
}

// RegisterWith is Register with a callback invoked after every attempt,
// with the server's answer or the error.
func (c *Client) RegisterWith(ctx context.Context, notify func(*Registration, error)) error {
	const retryAfter = 10 * time.Second
	for {
		reg, err := c.RegisterOnce(ctx)
		wait := retryAfter
		if ctx.Err() != nil {
			return c.releaseRegistration()
		}
		if notify != nil {
			notify(reg, err)
		}
		switch {
		case err != nil:
			c.log.Error("registration failed", "err", err, "retry_in", wait)
		default:
			wait = reg.Refresh * 5 / 6
			c.log.Info("registered", "user", c.cfg.Username, "refresh", reg.Refresh,
				"apparent_addr", reg.ApparentAddr, "server_time", reg.ServerTime, "next_in", wait)
		}
		select {
		case <-ctx.Done():
			return c.releaseRegistration()
		case <-c.done:
			return nil
		case <-time.After(wait):
		}
	}
}

// releaseRegistration sends a best-effort REGREL on a short deadline.
func (c *Client) releaseRegistration() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Unregister(ctx); err != nil {
		c.log.Warn("unregister failed", "err", err)
	}
	return nil
}

// registration runs REGREQ or REGREL: send the request, answer the MD5
// challenge in a REGAUTH, and acknowledge the final REGACK.
func (c *Client) registration(ctx context.Context, cmd IAXSubclass) (*Registration, error) {
	call := c.newCall()
	defer c.release(call)

	refresh := uint16(c.cfg.RegisterRefresh / time.Second) //nolint:gosec // bounded by the IE
	req := IEs{
		StringIE(IEUsername, c.cfg.Username),
		Uint16IE(IERefresh, refresh),
		EmptyIE(IECallToken), // we support call tokens; the server may send one
	}
	data, err := req.Encode()
	if err != nil {
		return nil, err
	}
	sent, err := call.sendFull(FrameIAX, uint64(cmd), data)
	if err != nil {
		return nil, err
	}
	reply, err := call.await(ctx, sent, IAXRegAuth, IAXRegAck)
	if err != nil {
		return nil, err
	}

	if IAXSubclass(reply.Subclass) == IAXRegAuth {
		ies, err := reply.IEs()
		if err != nil {
			return nil, err
		}
		methods, err := ies.Uint16(IEAuthMethods)
		if err != nil {
			return nil, err
		}
		if AuthMethod(methods)&AuthMD5 == 0 {
			return nil, fmt.Errorf("iax2: server offers auth methods %#x, only MD5 is supported", methods)
		}
		challenge := ies.String(IEChallenge)
		if challenge == "" {
			return nil, errors.New("iax2: REGAUTH without CHALLENGE")
		}
		rep := IEs{
			StringIE(IEUsername, c.cfg.Username),
			StringIE(IEMD5Result, MD5Response(challenge, c.cfg.Secret)),
			Uint16IE(IERefresh, refresh),
		}
		data, err := rep.Encode()
		if err != nil {
			return nil, err
		}
		sent, err = call.sendFull(FrameIAX, uint64(cmd), data)
		if err != nil {
			return nil, err
		}
		// A second REGAUTH means our answer was wrong (chan_iax2 challenges
		// again rather than rejecting).
		reply, err = call.await(ctx, sent, IAXRegAck, IAXRegAuth)
		if err != nil {
			return nil, err
		}
		if IAXSubclass(reply.Subclass) == IAXRegAuth {
			return nil, ErrAuth
		}
	}

	// REGACK is the server's final frame; handleFull has acknowledged it, so
	// the server destroys its side of the call.
	ies, err := reply.IEs()
	if err != nil {
		return nil, err
	}
	reg := &Registration{MsgCount: 0xFFFF}
	if v, err := ies.Uint16(IERefresh); err == nil {
		reg.Refresh = time.Duration(v) * time.Second
	} else {
		reg.Refresh = c.cfg.RegisterRefresh
	}
	if ie, ok := ies.Get(IEApparentAddr); ok {
		if ap, ok := parseSockaddr(ie.Data); ok {
			reg.ApparentAddr = ap.String()
		}
	}
	if v, err := ies.Uint32(IEDateTime); err == nil {
		reg.ServerTime = DecodeDateTime(v)
	}
	if v, err := ies.Uint16(IEMsgCount); err == nil {
		reg.MsgCount = v
	}
	return reg, nil
}
