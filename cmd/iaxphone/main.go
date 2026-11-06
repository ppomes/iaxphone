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

// Command iaxphone is a command-line IAX2 softphone.
//
//	iaxphone -server pbx -user phone -secret s3cret register   (and take incoming calls)
//	iaxphone -server pbx -user phone -secret s3cret call 600
//	iaxphone -server pbx ping
//	iaxphone devices
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ppomes/iaxphone/audio"
	"github.com/ppomes/iaxphone/iax2"
	"github.com/ppomes/iaxphone/phone"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "iaxphone:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		server   = flag.String("server", "127.0.0.1", "IAX2 server host[:port]")
		user     = flag.String("user", "", "username")
		secret   = flag.String("secret", "", "secret")
		cid      = flag.String("callerid", "", "caller id number")
		name     = flag.String("name", "iaxphone", "caller id name")
		duration = flag.Duration("duration", 0, "hang up after this long (0: until the peer hangs up)")
		audioDev = flag.String("audio", "default", "sound device: default (system devices) or none (silence)")
		mic      = flag.String("mic", "", "capture device name (see devices; empty: system default)")
		speaker  = flag.String("speaker", "", "playback device name (see devices; empty: system default)")
		jitter   = flag.Int("jitter", audio.DefaultJitterMs, "jitter buffer depth in ms")
		answer   = flag.Duration("auto-answer", 2*time.Second, "ring this long before answering an incoming call")
		dtmf     = flag.String("dtmf", "", "keys to press one second after the call is answered")
		hold     = flag.Duration("hold", iax2.DTMFDuration, "how long each -dtmf key is held")
		onhold   = flag.Duration("onhold", 0, "put the call on hold for this long, after the -dtmf keys")
		transfer = flag.String("transfer", "", "transfer the call to this number, after -dtmf and -onhold")
		verbose  = flag.Bool("v", false, "log every full frame and the audio levels")
	)
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), "usage: iaxphone [flags] register|call <number>|ping|devices\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	for i := 0; i < len(*dtmf); i++ {
		if !iax2.ValidDigit((*dtmf)[i]) {
			return fmt.Errorf("-dtmf: %q is not a DTMF key", (*dtmf)[i])
		}
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := flag.Arg(0)
	switch cmd {
	case "devices":
		return listDevices(log)
	case "ping":
		return ping(ctx, log, *server)
	case "register", "call":
	case "":
		flag.Usage()
		return fmt.Errorf("missing command")
	default:
		flag.Usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
	if cmd == "call" && flag.NArg() != 2 {
		flag.Usage()
		return fmt.Errorf("call needs a number")
	}

	openDevice, err := deviceOpener(log, *audioDev, *mic, *speaker)
	if err != nil {
		return err
	}
	p := phone.New(phone.Config{
		Server:       *server,
		Username:     *user,
		Secret:       *secret,
		CallerNumber: *cid,
		CallerName:   *name,
		Register:     cmd == "register",
		OpenDevice:   openDevice,
		JitterMs:     *jitter,
		Logger:       log,
	})
	if err := p.Start(ctx); err != nil {
		return err
	}
	defer p.Close() //nolint:errcheck
	log.Info("socket open", "local", p.LocalAddr())

	ui := &console{p: p, log: log, duration: *duration, autoAnswer: *answer, dtmf: *dtmf, hold: *hold,
		onhold: *onhold, transfer: *transfer, verbose: *verbose}
	if cmd == "call" {
		if err := p.Dial(flag.Arg(1)); err != nil {
			return err
		}
		return ui.loop(ctx, true)
	}
	return ui.loop(ctx, false)
}

// console drives a Phone from the terminal: it prints events, answers
// incoming calls after a delay and, once a call is up, presses the -dtmf
// keys, holds the call for -onhold and transfers it to -transfer. There
// is no keyboard input yet.
type console struct {
	p          *phone.Phone
	log        *slog.Logger
	duration   time.Duration
	autoAnswer time.Duration
	dtmf       string
	hold       time.Duration
	onhold     time.Duration
	transfer   string
	verbose    bool
}

// scripted reports whether there is something to do once a call is up.
func (c *console) scripted() bool {
	return c.dtmf != "" || c.onhold > 0 || c.transfer != ""
}

// script runs a second after the call was answered: it presses the -dtmf
// keys one after the other, each held for -hold, the way a finger would
// (the key sounds locally and DTMF_END follows the release), then holds
// the call for -onhold, then transfers it to -transfer.
func (c *console) script(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Second):
	}
	c.pressKeys(ctx)
	if c.onhold > 0 {
		if err := c.p.Hold(); err != nil {
			c.log.Error("hold", "err", err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.onhold):
		}
		if err := c.p.Unhold(); err != nil {
			c.log.Error("unhold", "err", err)
			return
		}
	}
	if c.transfer != "" {
		if err := c.p.Transfer(c.transfer); err != nil {
			c.log.Error("transfer", "err", err)
			return
		}
		fmt.Printf("transferring to %s\n", c.transfer)
	}
}

func (c *console) pressKeys(ctx context.Context) {
	for i := 0; i < len(c.dtmf); i++ {
		if err := c.p.PressKey(c.dtmf[i]); err != nil {
			c.log.Error("dtmf", "key", string(c.dtmf[i]), "err", err)
			return
		}
		select {
		case <-ctx.Done():
			c.p.ReleaseKey()
			return
		case <-time.After(c.hold):
		}
		c.p.ReleaseKey()
		fmt.Printf("key sent: %c\n", c.dtmf[i])
		select {
		case <-ctx.Done():
			return
		case <-time.After(iax2.DTMFDuration): // gap between keys
		}
	}
}

// loop runs until ctx is cancelled, or, when oneCall is set, until the
// current call ends.
func (c *console) loop(ctx context.Context, oneCall bool) error {
	var (
		answerAt <-chan time.Time
		hangupAt <-chan time.Time
		levels   <-chan time.Time
		lastErr  error
	)
	if c.verbose {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		levels = t.C
	}
	for {
		select {
		case <-ctx.Done():
			if _, inCall := c.p.Call(); inCall {
				_ = c.p.Hangup()
				c.waitEnded()
			}
			return lastErr
		case <-answerAt:
			answerAt = nil
			if err := c.p.Answer(); err != nil {
				c.log.Error("answer failed", "err", err)
			}
		case <-hangupAt:
			hangupAt = nil
			fmt.Println("duration elapsed, hanging up")
			_ = c.p.Hangup()
		case <-levels:
			in, out := c.p.Levels()
			if _, inCall := c.p.Call(); inCall {
				c.log.Debug("levels", "in_dBFS", fmt.Sprintf("%.1f", in), "out_dBFS", fmt.Sprintf("%.1f", out))
			}
		case ev, ok := <-c.p.Events():
			if !ok {
				return lastErr
			}
			switch ev.Kind {
			case phone.EventRegistration:
				r := ev.Registration
				if r.Err != nil {
					fmt.Printf("registration failed: %v\n", r.Err)
				} else {
					fmt.Printf("registered, refresh %s, seen as %s\n", r.Refresh, r.ApparentAddr)
				}
			case phone.EventIncoming:
				fmt.Printf("incoming call from %q <%s>, answering in %s\n", ev.Call.Name, ev.Call.Number, c.autoAnswer)
				answerAt = time.After(c.autoAnswer)
			case phone.EventDialing:
				fmt.Printf("calling %s\n", ev.Call.Number)
			case phone.EventRemoteRinging:
				fmt.Println("ringing")
			case phone.EventAnswered:
				fmt.Printf("answered, %s\n", ev.Call.Format)
				answerAt = nil
				if c.duration > 0 {
					hangupAt = time.After(c.duration)
				}
				if c.scripted() {
					go c.script(ctx)
				}
			case phone.EventDTMF:
				fmt.Printf("key received: %c\n", ev.Digit)
			case phone.EventHold:
				if ev.Call.Held {
					fmt.Println("put on hold by the peer")
				} else {
					fmt.Println("on hold")
				}
			case phone.EventUnhold:
				fmt.Println("off hold")
			case phone.EventTransfer:
				fmt.Printf("the peer asks us to call %s instead\n", ev.Number)
			case phone.EventEnded:
				answerAt, hangupAt = nil, nil
				printEnded(ev.Call)
				if ev.Call.Err != nil {
					lastErr = ev.Call.Err
				}
				if oneCall {
					return lastErr
				}
			}
		}
	}
}

// waitEnded drains events until the current call reports its end.
func (c *console) waitEnded() {
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-c.p.Events():
			if !ok {
				return
			}
			if ev.Kind == phone.EventEnded {
				printEnded(ev.Call)
				return
			}
		case <-timeout:
			return
		}
	}
}

func printEnded(ci phone.CallInfo) {
	direction, by := "call to", "peer"
	if !ci.Outgoing {
		direction = "call from"
	}
	if ci.HungUpByUs {
		by = "us"
	}
	if ci.Err != nil {
		fmt.Printf("%s %s failed: %v\n", direction, ci.Number, ci.Err)
		return
	}
	st := ci.Stats
	if ci.TransferredTo != "" {
		fmt.Printf("%s %s: %s, transferred to %s (%s)\n", direction, ci.Number, ci.Format, ci.TransferredTo, ci.Cause)
	} else {
		fmt.Printf("%s %s: %s, hung up by %s (%s)\n", direction, ci.Number, ci.Format, by, ci.Cause)
	}
	fmt.Printf("  talk time %s, voice frames sent %d, played %d, silent %d, tone %d; jitter: received %d, lost %d, late %d, dropped %d, underruns %d\n",
		ci.TalkTime(time.Now()), st.Sent, st.Played, st.Silent, st.Tone,
		st.Jitter.Received, st.Jitter.Lost, st.Jitter.Late, st.Jitter.Dropped, st.Jitter.Underruns)
}

func ping(ctx context.Context, log *slog.Logger, server string) error {
	client, err := iax2.Dial(ctx, iax2.Config{Server: server, Logger: log})
	if err != nil {
		return err
	}
	defer client.Close() //nolint:errcheck
	rtt, err := client.Ping(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("pong in %s\n", rtt)
	return nil
}

// openMalgo opens miniaudio on the named devices ("" for the defaults).
func openMalgo(logf func(string), mic, speaker string) (audio.Device, error) {
	m, err := audio.OpenMalgo(logf)
	if err != nil {
		return nil, err
	}
	if err := m.Select(mic, speaker); err != nil {
		_ = m.Close()
		return nil, err
	}
	return m, nil
}

func miniaudioLogger(log *slog.Logger) func(string) {
	return func(msg string) { log.Debug("miniaudio", "msg", strings.TrimSpace(msg)) }
}

func listDevices(log *slog.Logger) error {
	m, err := audio.OpenMalgo(miniaudioLogger(log))
	if err != nil {
		return err
	}
	defer m.Close() //nolint:errcheck
	devs, err := m.Devices()
	if err != nil {
		return err
	}
	for _, d := range devs {
		kind := "playback"
		if d.Capture {
			kind = "capture "
		}
		mark := " "
		if d.Default {
			mark = "*"
		}
		fmt.Printf("%s %s  %s\n", mark, kind, d.Name)
	}
	return nil
}

func deviceOpener(log *slog.Logger, name, mic, speaker string) (func() (audio.Device, error), error) {
	switch name {
	case "none":
		return func() (audio.Device, error) { return &audio.NullDevice{}, nil }, nil
	case "default":
		return func() (audio.Device, error) { return openMalgo(miniaudioLogger(log), mic, speaker) }, nil
	default:
		return nil, errors.New("unknown -audio " + name)
	}
}
