// Package spoof changes a Bluetooth adapter's BD_ADDR with vendor-specific HCI
// commands. There is no standard command for this, so the right one is chosen
// by the Bluetooth SIG manufacturer ID reported by HCI_Read_Local_Version.
//
// Opcode sources: the Linux btusb/btintel/btmtk drivers, BlueZ tools/bdaddr.c,
// and the validation notes of FreeTHX/pywakepsx-on-bt.
package spoof

import (
	"errors"
	"fmt"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/btaddr"
	"github.com/protomothis/smartthings-ps5-control/internal/hci"
)

// Bluetooth SIG company identifiers.
const (
	MfrIntel     uint16 = 0x0002
	MfrCSR       uint16 = 0x000A
	MfrTI        uint16 = 0x000D
	MfrBroadcom  uint16 = 0x000F
	MfrAtheros   uint16 = 0x0045
	MfrMediaTek  uint16 = 0x0046
	MfrRealtek   uint16 = 0x005D
	MfrQualcomm  uint16 = 0x00E0
	MfrCypress   uint16 = 0x0131
	MfrSiliconLb uint16 = 0x0A4D // not a spoof target; listed for naming only
)

// ResetMode says what to do after the vendor write so the address takes effect.
type ResetMode int

const (
	ResetNone       ResetMode = iota // address is live immediately
	ResetRequired                    // HCI_Reset must succeed
	ResetBestEffort                  // HCI_Reset is attempted; a timeout is tolerated
)

// Strategy describes how one chip family rewrites its BD_ADDR.
type Strategy struct {
	Vendor string
	// Rating 0..4: how well the strategy is validated upstream (4 = proven on
	// many firmware versions, 1 = opcode from kernel source only, untested).
	Rating int
	Notes  string

	opcode uint16
	params func(a btaddr.Addr) []byte
	reset  ResetMode
}

// Opcode returns the vendor opcode the strategy sends.
func (s Strategy) Opcode() uint16 { return s.opcode }

// Reset returns the strategy's reset behaviour.
func (s Strategy) Reset() ResetMode { return s.reset }

func plainLE(a btaddr.Addr) []byte { return a.LE() }

// Atheros/Qualcomm ROME use a tagged frame: 0x01 0x01 0x00 0x06 + addr LE
// (btusb_set_bdaddr_ath3012 in drivers/bluetooth/btusb.c).
func taggedLE(a btaddr.Addr) []byte { return append([]byte{0x01, 0x01, 0x00, 0x06}, a.LE()...) }

var strategies = map[uint16]Strategy{
	MfrBroadcom: {
		Vendor: "Broadcom", Rating: 4,
		Notes:  "HCI_BCM_Write_BD_ADDR 0xFC01, HCI_Reset activates. Validated on BCM20702/20703.",
		opcode: 0xFC01, params: plainLE, reset: ResetRequired,
	},
	MfrCypress: {
		Vendor: "Cypress", Rating: 3,
		Notes:  "Same 0xFC01 as Broadcom. Validated on CYW43438/43455 (Raspberry Pi). Reset may time out on some firmware.",
		opcode: 0xFC01, params: plainLE, reset: ResetBestEffort,
	},
	MfrIntel: {
		Vendor: "Intel", Rating: 3,
		Notes:  "HCI_VS_Intel_Write_BD_Address 0xFC31, HCI_Reset required. Validated on AX201.",
		opcode: 0xFC31, params: plainLE, reset: ResetRequired,
	},
	MfrMediaTek: {
		Vendor: "MediaTek", Rating: 3,
		Notes:  "0xFC1A (btmtk_set_bdaddr), live immediately. Validated on MT7921.",
		opcode: 0xFC1A, params: plainLE, reset: ResetNone,
	},
	MfrTI: {
		Vendor: "Texas Instruments", Rating: 1,
		Notes:  "0xFC06 from bdaddr.c (WiLink CC256x). Untested on hardware.",
		opcode: 0xFC06, params: plainLE, reset: ResetBestEffort,
	},
	MfrAtheros: {
		Vendor: "Atheros", Rating: 1,
		Notes:  "0xFC0B tagged frame (AR3011/AR3012). Untested on hardware.",
		opcode: 0xFC0B, params: taggedLE, reset: ResetBestEffort,
	},
	MfrQualcomm: {
		Vendor: "Qualcomm ROME", Rating: 1,
		Notes:  "0xFC0B tagged frame (QCA9377/QCA6174). WCN6855+ use 0xFC14 and are not supported. Untested.",
		opcode: 0xFC0B, params: taggedLE, reset: ResetBestEffort,
	},
}

// Unsupported chips we can at least name in error messages.
var knownUnsupported = map[uint16]string{
	MfrCSR:       "CSR / Silicon Labs (needs BCCMD PS-key write over 0xFC00; not implemented yet)",
	MfrRealtek:   "Realtek (BD_ADDR comes from the firmware config blob; no runtime HCI command)",
	MfrSiliconLb: "Silicon Labs",
}

// Lookup returns the strategy for a manufacturer ID.
func Lookup(mfr uint16) (Strategy, bool) {
	s, ok := strategies[mfr]
	return s, ok
}

// ManufacturerName returns a display name for a manufacturer ID, including
// chips that are recognised but not spoofable.
func ManufacturerName(mfr uint16) string {
	if s, ok := strategies[mfr]; ok {
		return s.Vendor
	}
	if n, ok := knownUnsupported[mfr]; ok {
		return n
	}
	return fmt.Sprintf("unknown manufacturer 0x%04X", mfr)
}

// ErrUnsupportedAdapter is returned when no strategy exists for the adapter.
var ErrUnsupportedAdapter = errors.New("spoof: adapter chipset cannot change its BD_ADDR at runtime")

// UnsupportedError carries the manufacturer behind ErrUnsupportedAdapter.
type UnsupportedError struct {
	Manufacturer uint16
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("%v: %s", ErrUnsupportedAdapter, ManufacturerName(e.Manufacturer))
}

func (e *UnsupportedError) Unwrap() error { return ErrUnsupportedAdapter }

// Detect reads the adapter's manufacturer and picks a strategy.
func Detect(open hci.Opener) (hci.LocalVersion, Strategy, error) {
	t, err := open(3 * time.Second)
	if err != nil {
		return hci.LocalVersion{}, Strategy{}, err
	}
	defer t.Close()
	ver, err := hci.ReadLocalVersion(t)
	if err != nil {
		return hci.LocalVersion{}, Strategy{}, err
	}
	s, ok := Lookup(ver.Manufacturer)
	if !ok {
		return ver, Strategy{}, &UnsupportedError{Manufacturer: ver.Manufacturer}
	}
	return ver, s, nil
}

// Logger receives progress messages; nil discards them.
type Logger func(format string, args ...any)

// Apply writes addr as the adapter's BD_ADDR and, when the chip needs it,
// resets the controller so the new address is used on air. It opens its own
// transport because HCI_Reset invalidates in-flight state on the socket.
func (s Strategy) Apply(open hci.Opener, addr btaddr.Addr, log Logger) error {
	if log == nil {
		log = func(string, ...any) {}
	}
	t, err := open(3 * time.Second)
	if err != nil {
		return err
	}
	defer t.Close()

	log("%s: write BD_ADDR %s (opcode 0x%04X)", s.Vendor, addr, s.opcode)
	st, _, err := hci.Request(t, s.opcode, s.params(addr))
	if err != nil {
		return fmt.Errorf("spoof: %s write: %w", s.Vendor, err)
	}
	if st != 0 {
		return fmt.Errorf("spoof: %s rejected BD_ADDR write: %w", s.Vendor, &hci.StatusError{Opcode: s.opcode, Status: st})
	}

	switch s.reset {
	case ResetNone:
	case ResetRequired:
		if err := hci.Reset(t); err != nil {
			return fmt.Errorf("spoof: %s: HCI_Reset after write: %w", s.Vendor, err)
		}
		log("%s: HCI_Reset done, %s active", s.Vendor, addr)
	case ResetBestEffort:
		if err := hci.Reset(t); err != nil {
			log("%s: HCI_Reset after write failed (%v); continuing, address may still be active", s.Vendor, err)
		} else {
			log("%s: HCI_Reset done, %s active", s.Vendor, addr)
		}
	}
	return nil
}
