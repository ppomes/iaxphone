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
	"errors"
	"fmt"
	"strings"
)

// ErrOnHold is returned by SendVoice while the call is on hold, on either
// side: nothing is sent.
var ErrOnHold = errors.New("iax2: call is on hold")

// OnHold reports whether we put the peer on hold (Hold).
func (c *Call) OnHold() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onHold
}

// Held reports whether the peer put us on hold (QUELCH received): it
// stopped sending its audio and asked us to do the same.
func (c *Call) Held() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.quelched
}

// Hold puts the peer on hold: QUELCH with a MUSICONHOLD IE, acknowledged
// by the peer, which then stops sending its audio and may play music to
// whoever it bridged us with (chan_iax2 does, with the mohsuggest class).
// Our voice frames are dropped until Unhold. It does nothing on a call
// already on hold and returns ErrCallDown on a call that is not up. Hold
// and Unhold are serialised: each takes effect in the order it was called.
func (c *Call) Hold() error {
	data, err := (IEs{StringIE(IEMusicOnHold, "")}).Encode()
	if err != nil {
		return err
	}
	return c.setHold(true, IAXQuelch, data)
}

// Unhold takes the peer back off hold: UNQUELCH, acknowledged by the peer,
// which resumes its audio; so do we.
func (c *Call) Unhold() error {
	return c.setHold(false, IAXUnquelch, nil)
}

// setHold records the hold state and sends the command that goes with it,
// one change at a time. The state is reverted when the command cannot be
// sent: what the peer knows and what we do stay the same.
func (c *Call) setHold(on bool, sub IAXSubclass, data []byte) error {
	c.holdMu.Lock()
	defer c.holdMu.Unlock()
	c.mu.Lock()
	if c.state != StateUp {
		c.mu.Unlock()
		return ErrCallDown
	}
	if c.onHold == on {
		c.mu.Unlock()
		return nil
	}
	c.onHold = on
	c.mu.Unlock()
	f, err := c.sendFull(FrameIAX, uint64(sub), data)
	if err != nil {
		c.mu.Lock()
		c.onHold = !on
		c.mu.Unlock()
		return err
	}
	if err := c.waitAck(f, 0); err != nil {
		return fmt.Errorf("iax2: %s not acknowledged by %s: %w", sub, c.client.server, err)
	}
	return nil
}

// Transfer asks the peer to connect whoever it bridged us with to number
// (a blind transfer): TRANSFER with a CALLED_NUMBER IE, acknowledged by
// the peer. Asterisk sends the other party to that extension in our
// context and then hangs up on us; a call that is not bridged is left as
// it is. A number with an @ names the context after it.
func (c *Call) Transfer(number string) error {
	if number == "" {
		return errors.New("iax2: transfer: no number")
	}
	if c.State() != StateUp {
		return ErrCallDown
	}
	ies := IEs{StringIE(IECalledNumber, number)}
	if i := strings.IndexByte(number, '@'); i >= 0 {
		ies = IEs{StringIE(IECalledNumber, number[:i]), StringIE(IECalledContext, number[i+1:])}
	}
	data, err := ies.Encode()
	if err != nil {
		return err
	}
	return c.command(IAXTransfer, data)
}

// command sends one IAX frame on the call and waits for its ACK.
func (c *Call) command(sub IAXSubclass, data []byte) error {
	f, err := c.sendFull(FrameIAX, uint64(sub), data)
	if err != nil {
		return err
	}
	if err := c.waitAck(f, 0); err != nil {
		return fmt.Errorf("iax2: %s not acknowledged by %s: %w", sub, c.client.server, err)
	}
	return nil
}
