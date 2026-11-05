# iaxphone

A softphone for the IAX2 protocol ([RFC 5456](https://www.rfc-editor.org/rfc/rfc5456)),
written in Go.

IAX2 multiplexes signalling and media on a single UDP port (4569), which makes
it a much smaller protocol than SIP + SDP + RTP. This project is a from-scratch
implementation: a reusable `iax2` library, an `audio` package (codecs, jitter
buffer, sound devices), a command-line client and a small Fyne window.

The only dependency is [malgo](https://github.com/gen2brain/malgo), the Go
binding of miniaudio, which needs cgo and a C compiler; it talks to
PulseAudio/PipeWire, ALSA, CoreAudio or WASAPI at run time without extra
libraries. A `CGO_ENABLED=0` build still works, with sound disabled.
The window uses [Fyne](https://fyne.io), which on Linux compiles GLFW and
needs the X11, Wayland and OpenGL headers (Debian/Ubuntu: `libgl1-mesa-dev
xorg-dev libwayland-dev libxkbcommon-dev wayland-protocols`). `make gui`
builds it; the CLI does not need any of that.

## Layout

```
iax2/              protocol library: framing, information elements, calls, client
audio/             G.711, jitter buffer, sound devices (miniaudio), audio engine
phone/             softphone controller: registration, one call, audio bridged; what UIs drive
cmd/iaxphone/      CLI client
cmd/iaxphone-gui/  Fyne window on top of phone
internal/fakeiax/  minimal IAX2 server for the tests above iax2
docker/            throwaway Asterisk (chan_iax2) for end-to-end testing
```

## Status

Early skeleton. What exists today:

- [x] full/mini frame encoding and decoding, subclass compression
- [x] information element (IE) parsing and building
- [x] 16-bit mini-frame timestamp expansion (rollover handling)
- [x] MD5 challenge/response
- [x] G.711 µ-law and A-law
- [x] UDP transport, POKE/PONG liveness check (`iaxphone ping`), verified against Asterisk 23
- [ ] retransmission timers for every full frame, VNAK, INVAL replies
- [x] registration (REGREQ / REGAUTH / REGACK), refresh and REGREL, verified against Asterisk 23
- [x] outgoing calls (NEW / AUTHREQ / ACCEPT / ANSWER / HANGUP), voice in both directions, verified against Asterisk 23 (echo and tone tests)
- [x] incoming calls (NEW / AUTHREQ / ACCEPT / RINGING / ANSWER), caller authentication, verified against Asterisk 23
- [x] fixed-depth jitter buffer (adaptive depth and loss concealment later)
- [x] sound I/O (miniaudio via malgo, cgo), verified against Asterisk 23 with the system devices
- [x] DTMF (DTMF_BEGIN / DTMF_END both ways), verified against Asterisk 23
- [x] Keypad tones: a key sounds while it is held, and is held on the wire as long
- [ ] hold, transfer
- [x] call tokens (CALLTOKEN IE), answering the server's POKE (qualify=yes)
- [ ] AES-128 frame encryption
- [ ] Opus (cgo, libopus)
- [x] `phone` controller package (events, dial/answer/hangup/mute) shared by the UIs
- [x] GUI (Fyne): dial, answer, hang up, keypad/DTMF, mute, level meters, settings, status icon, single instance
- [x] ring and ringback tones (generated locally, IAX2 carries no audio before the answer), device selection

## Building

```
make build
make test
```

## The window

`bin/iaxphone-gui` shows the registration state, the current call, a
number entry and a keypad (it dials when idle and sends DTMF during a
call; a key sounds for as long as it is held, 100 ms at least, and so is
the DTMF), Call / Answer / Hang up, mute, peak level meters and the keys
received. The gear opens the settings (server, account, caller id,
registration, jitter, sound, microphone and speaker), stored in Fyne's
preferences for the app id
`com.github.ppomes.iaxphone`, secret included, in clear.

On a desktop with a status area (the StatusNotifierItem protocol: GNOME
with the AppIndicator extension, KDE, Xfce...) the phone also sits in it
as a handset whose colour is the state: grey not connected, blue ready,
orange ringing, green in a call. There the phone starts behind its icon,
with no window, and closing the window puts it back there while the phone
keeps taking calls; the icon, its menu (show, answer, hang up, quit) and
an incoming call bring the window up. Only one `iaxphone-gui` runs per
user: a second one, say from the applications menu, shows the window of
the first and exits (a socket in `$XDG_RUNTIME_DIR`).

The window is tested on Fyne's test driver against the fake server, see
`cmd/iaxphone-gui/ui_test.go`.

## Debian packages

`make deb` builds `iaxphone` (the command line) and `iaxphone-gui` (the
window, with a desktop entry) in `dist/deb/`, offline, from an orig
tarball that vendors the Go modules (`debian/make-orig-tarball.sh`). It
needs `debhelper`, Go 1.26 and the X11, Wayland and GL development
packages listed in `debian/control`.

## Testing against Asterisk

```
make asterisk-up
bin/iaxphone -server 127.0.0.1 -user phone -secret s3cret register
bin/iaxphone -server 127.0.0.1 -user phone -secret s3cret call 600
```

Extension 600 is an echo test, 601 plays a 1004 Hz tone for 5 s and hangs up,
602 plays "hello world", 603 reads up to four keys and presses them back
(`-dtmf 1234` presses keys a second after the call is answered, each held for
`-hold`, 100 ms), 604 presses `159*#` at the phone. Any other number is
answered with the "not in service" prompt. The image is andrius/asterisk
plus the English core sounds, built by `make asterisk-up` from
`docker/asterisk/Dockerfile`.
While `register` runs it also takes incoming calls, answering them after
`-auto-answer` (2 s). From the Asterisk console:

```
docker exec -it iaxphone-asterisk asterisk -rvvv
*CLI> channel originate IAX2/phone application Milliwatt
*CLI> channel originate Local/phone@iaxphone application Echo   (with a caller id)
```

The call uses the system's default sound devices (`iaxphone devices` lists
them, `-mic` and `-speaker` pick others by name); `-audio none` sends silence
instead, `-jitter` sets the playout delay
in ms (default 60), and `-v` prints the peak capture and playback levels once
a second along with every full frame. Wireshark dissects IAX2 natively, which
is the quickest way to compare what the client sends with what `chan_iax2`
expects.

## References

- RFC 5456, IAX: Inter-Asterisk eXchange Version 2
- `channels/chan_iax2.c` and `channels/iax2/parser.c` in the Asterisk source tree,
  which are the real reference where the RFC is silent
- Asterisk `include/asterisk/format_compatibility.h` for the codec bitmask

## License

MIT, see `LICENSE`.
