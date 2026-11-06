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

import "fmt"

// DefaultPort is the IANA-registered IAX2 UDP port.
const DefaultPort = 4569

// G711SampleRate is the sample rate of the G.711 codecs, one byte per
// sample; voice timestamps are derived from it.
const G711SampleRate = 8000

// ProtocolVersion is the only IAX protocol version in existence (VERSION IE).
const ProtocolVersion uint16 = 2

// FrameType is the full-frame "frame type" byte (RFC 5456 §8.2).
type FrameType uint8

// Frame types.
const (
	FrameDTMFEnd   FrameType = 1
	FrameVoice     FrameType = 2
	FrameVideo     FrameType = 3
	FrameControl   FrameType = 4
	FrameNull      FrameType = 5
	FrameIAX       FrameType = 6
	FrameText      FrameType = 7
	FrameImage     FrameType = 8
	FrameHTML      FrameType = 9
	FrameCNG       FrameType = 10
	FrameModem     FrameType = 11
	FrameDTMFBegin FrameType = 12
)

var frameTypeNames = map[FrameType]string{
	FrameDTMFEnd: "DTMF_END", FrameVoice: "VOICE", FrameVideo: "VIDEO",
	FrameControl: "CONTROL", FrameNull: "NULL", FrameIAX: "IAX",
	FrameText: "TEXT", FrameImage: "IMAGE", FrameHTML: "HTML",
	FrameCNG: "CNG", FrameModem: "MODEM", FrameDTMFBegin: "DTMF_BEGIN",
}

func (t FrameType) String() string {
	if s, ok := frameTypeNames[t]; ok {
		return s
	}
	return fmt.Sprintf("FrameType(%d)", uint8(t))
}

// IAXSubclass is the subclass of an IAX (type 6) frame (RFC 5456 §8.4).
type IAXSubclass uint8

// IAX subclasses.
const (
	IAXNew       IAXSubclass = 1
	IAXPing      IAXSubclass = 2
	IAXPong      IAXSubclass = 3
	IAXAck       IAXSubclass = 4
	IAXHangup    IAXSubclass = 5
	IAXReject    IAXSubclass = 6
	IAXAccept    IAXSubclass = 7
	IAXAuthReq   IAXSubclass = 8
	IAXAuthRep   IAXSubclass = 9
	IAXInval     IAXSubclass = 10
	IAXLagRq     IAXSubclass = 11
	IAXLagRp     IAXSubclass = 12
	IAXRegReq    IAXSubclass = 13
	IAXRegAuth   IAXSubclass = 14
	IAXRegAck    IAXSubclass = 15
	IAXRegRej    IAXSubclass = 16
	IAXRegRel    IAXSubclass = 17
	IAXVnak      IAXSubclass = 18
	IAXDpReq     IAXSubclass = 19
	IAXDpRep     IAXSubclass = 20
	IAXDial      IAXSubclass = 21
	IAXTxReq     IAXSubclass = 22
	IAXTxCnt     IAXSubclass = 23
	IAXTxAcc     IAXSubclass = 24
	IAXTxReady   IAXSubclass = 25
	IAXTxRel     IAXSubclass = 26
	IAXTxRej     IAXSubclass = 27
	IAXQuelch    IAXSubclass = 28
	IAXUnquelch  IAXSubclass = 29
	IAXPoke      IAXSubclass = 30
	IAXPage      IAXSubclass = 31
	IAXMWI       IAXSubclass = 32
	IAXUnsupport IAXSubclass = 33
	IAXTransfer  IAXSubclass = 34
	IAXProvision IAXSubclass = 35
	IAXFwDownl   IAXSubclass = 36
	IAXFwData    IAXSubclass = 37
	IAXTxMedia   IAXSubclass = 38
	IAXRtKey     IAXSubclass = 39
	IAXCallToken IAXSubclass = 40
)

var iaxSubclassNames = map[IAXSubclass]string{
	IAXNew: "NEW", IAXPing: "PING", IAXPong: "PONG", IAXAck: "ACK",
	IAXHangup: "HANGUP", IAXReject: "REJECT", IAXAccept: "ACCEPT",
	IAXAuthReq: "AUTHREQ", IAXAuthRep: "AUTHREP", IAXInval: "INVAL",
	IAXLagRq: "LAGRQ", IAXLagRp: "LAGRP", IAXRegReq: "REGREQ",
	IAXRegAuth: "REGAUTH", IAXRegAck: "REGACK", IAXRegRej: "REGREJ",
	IAXRegRel: "REGREL", IAXVnak: "VNAK", IAXDpReq: "DPREQ", IAXDpRep: "DPREP",
	IAXDial: "DIAL", IAXTxReq: "TXREQ", IAXTxCnt: "TXCNT", IAXTxAcc: "TXACC",
	IAXTxReady: "TXREADY", IAXTxRel: "TXREL", IAXTxRej: "TXREJ",
	IAXQuelch: "QUELCH", IAXUnquelch: "UNQUELCH", IAXPoke: "POKE",
	IAXPage: "PAGE", IAXMWI: "MWI", IAXUnsupport: "UNSUPPORT",
	IAXTransfer: "TRANSFER", IAXProvision: "PROVISION", IAXFwDownl: "FWDOWNL",
	IAXFwData: "FWDATA", IAXTxMedia: "TXMEDIA", IAXRtKey: "RTKEY",
	IAXCallToken: "CALLTOKEN",
}

func (s IAXSubclass) String() string {
	if n, ok := iaxSubclassNames[s]; ok {
		return n
	}
	return fmt.Sprintf("IAXSubclass(%d)", uint8(s))
}

// ControlSubclass is the subclass of a CONTROL (type 4) frame (RFC 5456 §8.3).
type ControlSubclass uint8

// Control subclasses (values match Asterisk's AST_CONTROL_*).
const (
	ControlHangup     ControlSubclass = 1
	ControlRing       ControlSubclass = 2
	ControlRinging    ControlSubclass = 3
	ControlAnswer     ControlSubclass = 4
	ControlBusy       ControlSubclass = 5
	ControlCongestion ControlSubclass = 8
	ControlFlash      ControlSubclass = 9
	ControlOption     ControlSubclass = 11
	ControlKey        ControlSubclass = 12
	ControlUnkey      ControlSubclass = 13
	ControlProgress   ControlSubclass = 14
	ControlProceeding ControlSubclass = 15
	ControlHold       ControlSubclass = 16
	ControlUnhold     ControlSubclass = 17
	ControlVidUpdate  ControlSubclass = 18
	ControlT38        ControlSubclass = 19
	ControlSrcUpdate  ControlSubclass = 20
	ControlStopSounds ControlSubclass = 255
)

var controlSubclassNames = map[ControlSubclass]string{
	ControlHangup: "HANGUP", ControlRing: "RING", ControlRinging: "RINGING",
	ControlAnswer: "ANSWER", ControlBusy: "BUSY", ControlCongestion: "CONGESTION",
	ControlFlash: "FLASH", ControlOption: "OPTION", ControlKey: "KEY",
	ControlUnkey: "UNKEY", ControlProgress: "PROGRESS", ControlProceeding: "PROCEEDING",
	ControlHold: "HOLD", ControlUnhold: "UNHOLD", ControlVidUpdate: "VIDUPDATE",
	ControlT38: "T38", ControlSrcUpdate: "SRCUPDATE", ControlStopSounds: "STOPSOUNDS",
}

func (s ControlSubclass) String() string {
	if n, ok := controlSubclassNames[s]; ok {
		return n
	}
	return fmt.Sprintf("ControlSubclass(%d)", uint8(s))
}

// Format is a codec bitmask as used in the CAPABILITY and FORMAT IEs and in
// the subclass of VOICE frames. The bit assignment comes from Asterisk's
// include/asterisk/format_compatibility.h. Bits above 31 (Opus) need the
// 64-bit CAPABILITY2/FORMAT2 IEs.
type Format uint64

// Audio formats.
const (
	FormatG723    Format = 1 << 0
	FormatGSM     Format = 1 << 1
	FormatULAW    Format = 1 << 2
	FormatALAW    Format = 1 << 3
	FormatG726AAL Format = 1 << 4
	FormatADPCM   Format = 1 << 5
	FormatSLIN    Format = 1 << 6
	FormatLPC10   Format = 1 << 7
	FormatG729    Format = 1 << 8
	FormatSpeex   Format = 1 << 9
	FormatILBC    Format = 1 << 10
	FormatG726    Format = 1 << 11
	FormatG722    Format = 1 << 12
	FormatSiren7  Format = 1 << 13
	FormatSiren14 Format = 1 << 14
	FormatSLIN16  Format = 1 << 15
	FormatG719    Format = 1 << 32
	FormatSpeex16 Format = 1 << 33
	FormatOpus    Format = 1 << 34
)

var formatNames = map[Format]string{
	FormatG723: "g723", FormatGSM: "gsm", FormatULAW: "ulaw", FormatALAW: "alaw",
	FormatG726AAL: "g726aal2", FormatADPCM: "adpcm", FormatSLIN: "slin",
	FormatLPC10: "lpc10", FormatG729: "g729", FormatSpeex: "speex",
	FormatILBC: "ilbc", FormatG726: "g726", FormatG722: "g722",
	FormatSiren7: "siren7", FormatSiren14: "siren14", FormatSLIN16: "slin16",
	FormatG719: "g719", FormatSpeex16: "speex16", FormatOpus: "opus",
}

// ParseFormat returns the codec called name, as String writes it ("ulaw",
// "opus"...).
func ParseFormat(name string) (Format, error) {
	for f, n := range formatNames {
		if n == name {
			return f, nil
		}
	}
	return 0, fmt.Errorf("iax2: unknown codec %q", name)
}

func (f Format) String() string {
	if n, ok := formatNames[f]; ok {
		return n
	}
	s := ""
	for bit := 0; bit < 64; bit++ {
		if b := Format(1) << bit; f&b != 0 {
			if s != "" {
				s += "|"
			}
			if n, ok := formatNames[b]; ok {
				s += n
			} else {
				s += fmt.Sprintf("bit%d", bit)
			}
		}
	}
	if s == "" {
		return "none"
	}
	return s
}

// AuthMethod is the AUTHMETHODS IE bitmask.
type AuthMethod uint16

// Authentication methods.
const (
	AuthPlaintext AuthMethod = 1 << 0
	AuthMD5       AuthMethod = 1 << 1
	AuthRSA       AuthMethod = 1 << 2
)

// Cause codes (CAUSECODE IE), the subset a client cares about. Values follow
// Q.931.
const (
	CauseUnallocated      uint8 = 1
	CauseNormalClearing   uint8 = 16
	CauseUserBusy         uint8 = 17
	CauseNoAnswer         uint8 = 19
	CauseCallRejected     uint8 = 21
	CauseOutOfOrder       uint8 = 27
	CauseCongestion       uint8 = 34
	CauseBearerCapability uint8 = 58

	// CauseLocal is used for events raised by this side, not by the peer.
	CauseLocal uint8 = 0
)

var causeNames = map[uint8]string{
	CauseUnallocated: "Unallocated (unassigned) number", CauseNormalClearing: "Normal Clearing",
	CauseUserBusy: "User busy", CauseNoAnswer: "No answer", CauseCallRejected: "Call Rejected",
	CauseOutOfOrder: "Destination out of order",
	CauseCongestion: "Circuit/channel congestion", CauseBearerCapability: "Bearer capability not available",
}

// CauseText returns the Q.931 description used in CAUSE IEs.
func CauseText(code uint8) string {
	if s, ok := causeNames[code]; ok {
		return s
	}
	return fmt.Sprintf("Cause %d", code)
}
