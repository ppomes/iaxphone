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
	"encoding/binary"
	"errors"
	"fmt"
)

// IEID identifies an information element (RFC 5456 §8.6).
type IEID uint8

// Information element identifiers.
const (
	IECalledNumber    IEID = 1
	IECallingNumber   IEID = 2
	IECallingANI      IEID = 3
	IECallingName     IEID = 4
	IECalledContext   IEID = 5
	IEUsername        IEID = 6
	IEPassword        IEID = 7
	IECapability      IEID = 8
	IEFormat          IEID = 9
	IELanguage        IEID = 10
	IEVersion         IEID = 11
	IEADSICPE         IEID = 12
	IEDNID            IEID = 13
	IEAuthMethods     IEID = 14
	IEChallenge       IEID = 15
	IEMD5Result       IEID = 16
	IERSAResult       IEID = 17
	IEApparentAddr    IEID = 18
	IERefresh         IEID = 19
	IEDPStatus        IEID = 20
	IECallNo          IEID = 21
	IECause           IEID = 22
	IEIAXUnknown      IEID = 23
	IEMsgCount        IEID = 24
	IEAutoAnswer      IEID = 25
	IEMusicOnHold     IEID = 26
	IETransferID      IEID = 27
	IERDNIS           IEID = 28
	IEProvisioning    IEID = 29
	IEAESProvisioning IEID = 30
	IEDateTime        IEID = 31
	IEDeviceType      IEID = 32
	IEServiceIdent    IEID = 33
	IEFirmwareVer     IEID = 34
	IEFWBlockDesc     IEID = 35
	IEFWBlockData     IEID = 36
	IEProvVer         IEID = 37
	IECallingPres     IEID = 38
	IECallingTON      IEID = 39
	IECallingTNS      IEID = 40
	IESamplingRate    IEID = 41
	IECauseCode       IEID = 42
	IEEncryption      IEID = 43
	IEEncKey          IEID = 44
	IECodecPrefs      IEID = 45
	IERRJitter        IEID = 46
	IERRLoss          IEID = 47
	IERRPkts          IEID = 48
	IERRDelay         IEID = 49
	IERRDropped       IEID = 50
	IERROOO           IEID = 51
	IEVariable        IEID = 52
	IEOSPToken        IEID = 53
	IECallToken       IEID = 54
	IECapability2     IEID = 55
	IEFormat2         IEID = 56
)

var ieNames = map[IEID]string{
	IECalledNumber: "CALLED_NUMBER", IECallingNumber: "CALLING_NUMBER",
	IECallingANI: "CALLING_ANI", IECallingName: "CALLING_NAME",
	IECalledContext: "CALLED_CONTEXT", IEUsername: "USERNAME",
	IEPassword: "PASSWORD", IECapability: "CAPABILITY", IEFormat: "FORMAT",
	IELanguage: "LANGUAGE", IEVersion: "VERSION", IEADSICPE: "ADSICPE",
	IEDNID: "DNID", IEAuthMethods: "AUTHMETHODS", IEChallenge: "CHALLENGE",
	IEMD5Result: "MD5_RESULT", IERSAResult: "RSA_RESULT",
	IEApparentAddr: "APPARENT_ADDR", IERefresh: "REFRESH", IEDPStatus: "DPSTATUS",
	IECallNo: "CALLNO", IECause: "CAUSE", IEIAXUnknown: "IAX_UNKNOWN",
	IEMsgCount: "MSGCOUNT", IEAutoAnswer: "AUTOANSWER",
	IEMusicOnHold: "MUSICONHOLD", IETransferID: "TRANSFERID", IERDNIS: "RDNIS",
	IEProvisioning: "PROVISIONING", IEAESProvisioning: "AESPROVISIONING",
	IEDateTime: "DATETIME", IEDeviceType: "DEVICETYPE",
	IEServiceIdent: "SERVICEIDENT", IEFirmwareVer: "FIRMWAREVER",
	IEFWBlockDesc: "FWBLOCKDESC", IEFWBlockData: "FWBLOCKDATA",
	IEProvVer: "PROVVER", IECallingPres: "CALLINGPRES", IECallingTON: "CALLINGTON",
	IECallingTNS: "CALLINGTNS", IESamplingRate: "SAMPLINGRATE",
	IECauseCode: "CAUSECODE", IEEncryption: "ENCRYPTION", IEEncKey: "ENCKEY",
	IECodecPrefs: "CODEC_PREFS", IERRJitter: "RR_JITTER", IERRLoss: "RR_LOSS",
	IERRPkts: "RR_PKTS", IERRDelay: "RR_DELAY", IERRDropped: "RR_DROPPED",
	IERROOO: "RR_OOO", IEVariable: "VARIABLE", IEOSPToken: "OSPTOKEN",
	IECallToken: "CALLTOKEN", IECapability2: "CAPABILITY2", IEFormat2: "FORMAT2",
}

func (id IEID) String() string {
	if n, ok := ieNames[id]; ok {
		return n
	}
	return fmt.Sprintf("IE(%d)", uint8(id))
}

// IE errors.
var (
	ErrShortIE = errors.New("iax2: truncated information element")
	ErrLongIE  = errors.New("iax2: information element longer than 255 bytes")
	ErrIESize  = errors.New("iax2: information element has unexpected size")
)

// IE is one information element: a one-byte id, a one-byte length and the
// data. Strings are not NUL-terminated on the wire.
type IE struct {
	ID   IEID
	Data []byte
}

// IEs is the ordered list of information elements of an IAX frame.
type IEs []IE

// ParseIEs decodes a list of information elements. Data slices alias b.
func ParseIEs(b []byte) (IEs, error) {
	var ies IEs
	for len(b) > 0 {
		if len(b) < 2 {
			return nil, ErrShortIE
		}
		id, n := IEID(b[0]), int(b[1])
		if len(b) < 2+n {
			return nil, ErrShortIE
		}
		ies = append(ies, IE{ID: id, Data: b[2 : 2+n]})
		b = b[2+n:]
	}
	return ies, nil
}

// AppendTo appends the wire encoding of the list to dst.
func (ies IEs) AppendTo(dst []byte) ([]byte, error) {
	for _, ie := range ies {
		if len(ie.Data) > 255 {
			return nil, fmt.Errorf("%w: %s", ErrLongIE, ie.ID)
		}
		dst = append(dst, uint8(ie.ID), uint8(len(ie.Data)))
		dst = append(dst, ie.Data...)
	}
	return dst, nil
}

// Encode returns the wire encoding of the list.
func (ies IEs) Encode() ([]byte, error) { return ies.AppendTo(nil) }

// Get returns the first element with the given id.
func (ies IEs) Get(id IEID) (IE, bool) {
	for _, ie := range ies {
		if ie.ID == id {
			return ie, true
		}
	}
	return IE{}, false
}

// Has reports whether an element with the given id is present.
func (ies IEs) Has(id IEID) bool {
	_, ok := ies.Get(id)
	return ok
}

// String returns the element as a string, or "" when absent.
func (ies IEs) String(id IEID) string {
	ie, ok := ies.Get(id)
	if !ok {
		return ""
	}
	return string(ie.Data)
}

// Uint8 returns a one-byte element.
func (ies IEs) Uint8(id IEID) (uint8, error) {
	ie, ok := ies.Get(id)
	if !ok {
		return 0, fmt.Errorf("iax2: missing %s", id)
	}
	if len(ie.Data) != 1 {
		return 0, fmt.Errorf("%w: %s", ErrIESize, id)
	}
	return ie.Data[0], nil
}

// Uint16 returns a two-byte big-endian element.
func (ies IEs) Uint16(id IEID) (uint16, error) {
	ie, ok := ies.Get(id)
	if !ok {
		return 0, fmt.Errorf("iax2: missing %s", id)
	}
	if len(ie.Data) != 2 {
		return 0, fmt.Errorf("%w: %s", ErrIESize, id)
	}
	return binary.BigEndian.Uint16(ie.Data), nil
}

// Uint32 returns a four-byte big-endian element.
func (ies IEs) Uint32(id IEID) (uint32, error) {
	ie, ok := ies.Get(id)
	if !ok {
		return 0, fmt.Errorf("iax2: missing %s", id)
	}
	if len(ie.Data) != 4 {
		return 0, fmt.Errorf("%w: %s", ErrIESize, id)
	}
	return binary.BigEndian.Uint32(ie.Data), nil
}

// Format returns the codec bitmask from a FORMAT2/FORMAT (or
// CAPABILITY2/CAPABILITY) pair, preferring the 64-bit variant. The 64-bit IEs
// carry a one-byte version (0) followed by eight bytes.
func (ies IEs) Format(id32, id64 IEID) (Format, error) {
	if ie, ok := ies.Get(id64); ok {
		if len(ie.Data) != 9 || ie.Data[0] != 0 {
			return 0, fmt.Errorf("%w: %s", ErrIESize, id64)
		}
		return Format(binary.BigEndian.Uint64(ie.Data[1:])), nil
	}
	v, err := ies.Uint32(id32)
	return Format(v), err
}

// StringIE builds a string element.
func StringIE(id IEID, s string) IE { return IE{ID: id, Data: []byte(s)} }

// Uint8IE builds a one-byte element.
func Uint8IE(id IEID, v uint8) IE { return IE{ID: id, Data: []byte{v}} }

// Uint16IE builds a two-byte big-endian element.
func Uint16IE(id IEID, v uint16) IE {
	return IE{ID: id, Data: binary.BigEndian.AppendUint16(nil, v)}
}

// Uint32IE builds a four-byte big-endian element.
func Uint32IE(id IEID, v uint32) IE {
	return IE{ID: id, Data: binary.BigEndian.AppendUint32(nil, v)}
}

// Format64IE builds a nine-byte CAPABILITY2/FORMAT2 element.
func Format64IE(id IEID, f Format) IE {
	return IE{ID: id, Data: binary.BigEndian.AppendUint64([]byte{0}, uint64(f))}
}

// EmptyIE builds a zero-length element (for example AUTOANSWER).
func EmptyIE(id IEID) IE { return IE{ID: id} }
