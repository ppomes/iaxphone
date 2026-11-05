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
	"fmt"
	"math/rand/v2"
)

// handleNew is the callee side of a NEW frame (RFC 5456 §6.3.2): it creates
// the call, authenticates the caller when we have a secret, picks the codec,
// sends ACCEPT and RINGING and hands the call to Client.Incoming.
func (c *Client) handleNew(f *FullFrame) {
	// chan_iax2 retransmits NEW until it gets a sequenced reply; a repeat
	// for a call we already created must not create another one.
	c.mu.Lock()
	existing := c.byRemote[f.SrcCall]
	c.mu.Unlock()
	if existing != nil && !existing.outgoing {
		c.log.Debug("duplicate NEW", "frame", f)
		return
	}
	ies, err := f.IEs()
	if err != nil {
		c.log.Warn("NEW with bad IEs", "frame", f, "err", err)
		return
	}
	call := c.newCall()
	call.mu.Lock()
	call.state = StateIncoming
	call.called = ies.String(IECalledNumber)
	call.caller = ies.String(IECallingNumber)
	call.callerNm = ies.String(IECallingName)
	call.mu.Unlock()
	call.handleFull(f) // learns the peer's call number, no ACK (we reply)
	go c.acceptIncoming(call, ies)
}

func (c *Client) acceptIncoming(call *Call, ies IEs) {
	// Drop the NEW handleFull queued: everything we need is in ies.
	select {
	case <-call.inbox:
	default:
	}
	reject := func(cause uint8, text string) {
		data, _ := (IEs{StringIE(IECause, text), Uint8IE(IECauseCode, cause)}).Encode()
		_, _ = call.sendFull(FrameIAX, uint64(IAXReject), data)
		c.abort(call, false)
	}

	format, err := c.chooseFormat(ies)
	if err != nil {
		c.log.Warn("incoming call rejected", "called", call.called, "err", err)
		reject(CauseBearerCapability, "Unable to negotiate codec")
		return
	}

	if c.cfg.Secret != "" {
		challenge := fmt.Sprintf("%09d", rand.IntN(1_000_000_000)) //nolint:gosec // not a secret, the response is keyed
		req := IEs{
			Uint16IE(IEAuthMethods, uint16(AuthMD5)),
			StringIE(IEChallenge, challenge),
			StringIE(IEUsername, c.cfg.Username),
		}
		data, err := req.Encode()
		if err != nil {
			reject(CauseCongestion, err.Error())
			return
		}
		sent, err := call.sendFull(FrameIAX, uint64(IAXAuthReq), data)
		if err != nil {
			c.abort(call, false)
			return
		}
		reply, err := call.await(context.Background(), sent, IAXAuthRep)
		if err != nil {
			c.log.Warn("incoming call: no AUTHREP", "called", call.called, "err", err)
			c.abort(call, true)
			return
		}
		rep, err := reply.IEs()
		if err != nil || rep.String(IEMD5Result) != MD5Response(challenge, c.cfg.Secret) {
			c.log.Warn("incoming call: bad secret", "called", call.called, "caller", call.caller)
			reject(CauseCallRejected, "No authority found")
			return
		}
	}

	call.mu.Lock()
	call.format = format
	call.state = StateRinging
	call.mu.Unlock()
	acc := IEs{
		Uint32IE(IEFormat, uint32(format)), //nolint:gosec // low 32 bits of the mask
		Format64IE(IEFormat2, format),
	}
	data, err := acc.Encode()
	if err != nil {
		reject(CauseCongestion, err.Error())
		return
	}
	if _, err := call.sendFull(FrameIAX, uint64(IAXAccept), data); err != nil {
		c.abort(call, false)
		return
	}
	if _, err := call.sendFull(FrameControl, uint64(ControlRinging), nil); err != nil {
		c.abort(call, false)
		return
	}
	go call.run()
	select {
	case c.Incoming <- call:
		c.log.Info("incoming call", "from", call.caller, "name", call.callerNm,
			"to", call.called, "format", format, "local", call.localNo, "remote", call.RemoteNumber())
	default:
		c.log.Warn("incoming call with nobody listening, busy", "called", call.called)
		_ = call.Hangup(CauseUserBusy)
	}
}

// chooseFormat picks the codec of an incoming call: the caller's preferred
// FORMAT if we support it, else our preferred one if the caller does, else
// any codec in common.
func (c *Client) chooseFormat(ies IEs) (Format, error) {
	offered, _ := ies.Format(IEFormat, IEFormat2)
	capability, _ := ies.Format(IECapability, IECapability2)
	if capability == 0 {
		capability = offered
	}
	if offered != 0 && offered&(offered-1) == 0 && offered&c.cfg.Capability != 0 {
		return offered, nil
	}
	common := capability & c.cfg.Capability
	if common == 0 {
		return 0, fmt.Errorf("iax2: caller offers %s, we accept %s", capability, c.cfg.Capability)
	}
	if common&c.cfg.Preferred != 0 {
		return c.cfg.Preferred, nil
	}
	return common & -common, nil // lowest common bit
}
