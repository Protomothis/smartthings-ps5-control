// Package hci builds and parses Bluetooth HCI packets and, on Linux, talks to
// an adapter over a raw HCI socket (AF_BLUETOOTH / SOCK_RAW / BTPROTO_HCI).
//
// Packet builders and parsers are portable so they can be unit-tested on any
// OS; only Open and the ioctl helpers need Linux.
package hci

import (
	"encoding/binary"
	"fmt"

	"github.com/protomothis/smartthings-ps5-control/internal/btaddr"
)

// Packet indicator bytes (Bluetooth Core Spec Vol 4 Part A).
const (
	PktCommand byte = 0x01
	PktEvent   byte = 0x04
)

// Event codes.
const (
	EvtConnectionComplete byte = 0x03
	EvtCommandComplete    byte = 0x0E
	EvtCommandStatus      byte = 0x0F
)

// Opcodes (OGF<<10 | OCF).
const (
	OpCreateConnection uint16 = 0x0405 // Link Control, OCF 0x0005
	OpDisconnect       uint16 = 0x0406 // Link Control, OCF 0x0006
	OpReset            uint16 = 0x0C03 // Controller & Baseband, OCF 0x0003
	OpReadLocalVersion uint16 = 0x1001 // Informational, OCF 0x0001
	OpReadBDAddr       uint16 = 0x1009 // Informational, OCF 0x0009
	OpVendorMask       uint16 = 0xFC00 // OGF 0x3F
)

// Command assembles a complete HCI command packet.
func Command(opcode uint16, params []byte) []byte {
	p := make([]byte, 0, 4+len(params))
	p = append(p, PktCommand, byte(opcode), byte(opcode>>8), byte(len(params)))
	return append(p, params...)
}

// CreateConnection builds HCI_Create_Connection (Vol 4 Part E 7.1.5) for a
// classic BR/EDR page to target. Parameters mirror what hcitool cc sends:
//
//	packet type       0xCC18  DM1|DH1|DM3|DH3|DM5|DH5
//	page scan rep     0x02    R2
//	reserved          0x00
//	clock offset      0x0000  no hint, full page
//	allow role switch 0x01
func CreateConnection(target btaddr.Addr) []byte {
	params := make([]byte, 0, 13)
	params = append(params, target.LE()...)
	params = append(params, 0x18, 0xCC, 0x02, 0x00, 0x00, 0x00, 0x01)
	return Command(OpCreateConnection, params)
}

// Disconnect builds HCI_Disconnect for a connection handle. reason is a
// Bluetooth error code; 0x13 ("Remote User Terminated Connection") is the
// conventional value for a host-initiated disconnect.
func Disconnect(handle uint16, reason byte) []byte {
	return Command(OpDisconnect, []byte{byte(handle), byte(handle >> 8), reason})
}

// EventFilter is the HCI_FILTER socket option value that delivers every event
// packet: struct hci_ufilter { u32 type_mask; u32 event_mask[2]; u16 opcode; }.
// event_mask covers codes 0..63 only, so vendor events (0xFF) never arrive.
// The struct is 16 bytes with tail padding; kernels since 6.10 reject a
// shorter option with EINVAL (bt_copy_from_sockptr), so all 16 are sent.
func EventFilter() []byte {
	f := make([]byte, 16)
	binary.LittleEndian.PutUint32(f[0:], 1<<PktEvent)
	binary.LittleEndian.PutUint32(f[4:], 0xFFFFFFFF)
	binary.LittleEndian.PutUint32(f[8:], 0xFFFFFFFF)
	return f
}

// Event is a parsed HCI event packet.
type Event struct {
	Code   byte
	Params []byte
}

// ParseEvent splits a raw packet into an Event. ok is false for anything that
// is not a well-formed event packet.
func ParseEvent(raw []byte) (ev Event, ok bool) {
	if len(raw) < 3 || raw[0] != PktEvent {
		return Event{}, false
	}
	n := int(raw[2])
	if len(raw) < 3+n {
		return Event{}, false
	}
	return Event{Code: raw[1], Params: raw[3 : 3+n]}, true
}

// CommandComplete returns the status and return parameters when ev is a
// Command Complete event for opcode.
//
//	[num_hci_cmd_pkts 1][opcode 2 LE][status 1][return params...]
func (ev Event) CommandComplete(opcode uint16) (status byte, ret []byte, ok bool) {
	if ev.Code != EvtCommandComplete || len(ev.Params) < 4 {
		return 0, nil, false
	}
	if binary.LittleEndian.Uint16(ev.Params[1:3]) != opcode {
		return 0, nil, false
	}
	return ev.Params[3], ev.Params[4:], true
}

// CommandStatus returns the status when ev is a Command Status event for
// opcode.
//
//	[status 1][num_hci_cmd_pkts 1][opcode 2 LE]
func (ev Event) CommandStatus(opcode uint16) (status byte, ok bool) {
	if ev.Code != EvtCommandStatus || len(ev.Params) < 4 {
		return 0, false
	}
	if binary.LittleEndian.Uint16(ev.Params[2:4]) != opcode {
		return 0, false
	}
	return ev.Params[0], true
}

// ConnectionComplete is the payload of a Connection Complete event.
type ConnectionComplete struct {
	Status   byte
	Handle   uint16
	Addr     btaddr.Addr
	LinkType byte // 0x00 SCO, 0x01 ACL
	Encrypt  byte
}

// ConnectionComplete decodes ev when it is a Connection Complete event.
//
//	[status 1][handle 2 LE][bd_addr 6 LE][link type 1][encryption enabled 1]
func (ev Event) ConnectionComplete() (cc ConnectionComplete, ok bool) {
	if ev.Code != EvtConnectionComplete || len(ev.Params) < 11 {
		return ConnectionComplete{}, false
	}
	return ConnectionComplete{
		Status:   ev.Params[0],
		Handle:   binary.LittleEndian.Uint16(ev.Params[1:3]) & 0x0FFF,
		Addr:     btaddr.FromLE(ev.Params[3:9]),
		LinkType: ev.Params[9],
		Encrypt:  ev.Params[10],
	}, true
}

// LocalVersion is the return of HCI_Read_Local_Version_Information.
type LocalVersion struct {
	HCIVersion    byte   `json:"hci_version"`
	HCIRevision   uint16 `json:"hci_revision"`
	LMPVersion    byte   `json:"lmp_version"`
	Manufacturer  uint16 `json:"manufacturer"` // Bluetooth SIG company identifier
	LMPSubversion uint16 `json:"lmp_subversion"`
}

func parseLocalVersion(ret []byte) (LocalVersion, error) {
	if len(ret) < 8 {
		return LocalVersion{}, fmt.Errorf("hci: short Read_Local_Version reply (%d bytes)", len(ret))
	}
	return LocalVersion{
		HCIVersion:    ret[0],
		HCIRevision:   binary.LittleEndian.Uint16(ret[1:3]),
		LMPVersion:    ret[3],
		Manufacturer:  binary.LittleEndian.Uint16(ret[4:6]),
		LMPSubversion: binary.LittleEndian.Uint16(ret[6:8]),
	}, nil
}

// Status codes (Vol 1 Part F) that matter for a wake attempt.
const (
	StatusSuccess              byte = 0x00
	StatusUnknownCommand       byte = 0x01
	StatusHardwareFailure      byte = 0x03
	StatusPageTimeout          byte = 0x04
	StatusCommandDisallowed    byte = 0x0C
	StatusConnAcceptTimeout    byte = 0x10
	StatusRemoteUserTerminated byte = 0x13
	StatusLocalHostTerminated  byte = 0x16
)

var statusText = map[byte]string{
	StatusSuccess:              "Success",
	StatusUnknownCommand:       "Unknown HCI Command",
	StatusHardwareFailure:      "Hardware Failure",
	StatusPageTimeout:          "Page Timeout",
	StatusCommandDisallowed:    "Command Disallowed",
	StatusConnAcceptTimeout:    "Connection Accept Timeout Exceeded",
	StatusRemoteUserTerminated: "Remote User Terminated Connection",
	StatusLocalHostTerminated:  "Connection Terminated By Local Host",
}

// StatusText returns a human-readable name for a status code.
func StatusText(code byte) string {
	if s, ok := statusText[code]; ok {
		return s
	}
	return fmt.Sprintf("Unknown status 0x%02X", code)
}
