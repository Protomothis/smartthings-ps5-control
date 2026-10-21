// Package btaddr handles Bluetooth device addresses (BD_ADDR).
//
// An Addr is stored in display order: Addr[0] is the first octet of
// "AA:BB:CC:DD:EE:FF". HCI commands carry addresses least-significant byte
// first, so use LE / FromLE at the HCI boundary.
package btaddr

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Addr is a 48-bit Bluetooth device address in display (big-endian) order.
type Addr [6]byte

// ErrFormat is returned when a string is not a valid 6-octet address.
var ErrFormat = errors.New("btaddr: expected 6 hex octets like AA:BB:CC:DD:EE:FF")

// Parse accepts "AA:BB:CC:DD:EE:FF", "AA-BB-CC-DD-EE-FF" or "AABBCCDDEEFF"
// in any case.
func Parse(s string) (Addr, error) {
	var a Addr
	clean := strings.NewReplacer(":", "", "-", "", " ", "").Replace(strings.TrimSpace(s))
	if len(clean) != 12 {
		return a, fmt.Errorf("%w: %q", ErrFormat, s)
	}
	b, err := hex.DecodeString(clean)
	if err != nil {
		return a, fmt.Errorf("%w: %q", ErrFormat, s)
	}
	copy(a[:], b)
	return a, nil
}

// MustParse is Parse for constants and tests; it panics on error.
func MustParse(s string) Addr {
	a, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return a
}

// String formats the address as upper-case colon-separated octets.
func (a Addr) String() string {
	return fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X", a[0], a[1], a[2], a[3], a[4], a[5])
}

// IsZero reports whether the address is all zeros (unset).
func (a Addr) IsZero() bool { return a == Addr{} }

// LE returns the address in HCI wire order (least-significant byte first).
func (a Addr) LE() []byte {
	return []byte{a[5], a[4], a[3], a[2], a[1], a[0]}
}

// FromLE builds an Addr from 6 bytes in HCI wire order.
func FromLE(b []byte) Addr {
	var a Addr
	if len(b) < 6 {
		return a
	}
	for i := 0; i < 6; i++ {
		a[i] = b[5-i]
	}
	return a
}

// MarshalText implements encoding.TextMarshaler (JSON uses the string form).
func (a Addr) MarshalText() ([]byte, error) { return []byte(a.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (a *Addr) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		*a = Addr{}
		return nil
	}
	p, err := Parse(string(text))
	if err != nil {
		return err
	}
	*a = p
	return nil
}
