package hci

import (
	"errors"
	"fmt"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/btaddr"
)

// Transport is one open raw HCI channel to an adapter.
type Transport interface {
	// Send writes one complete HCI packet.
	Send(pkt []byte) error
	// Recv returns the next HCI event packet, or ErrTimeout when the
	// receive timeout given to Open elapses first.
	Recv() ([]byte, error)
	Close() error
}

var (
	// ErrTimeout is returned by Recv when no packet arrives in time.
	ErrTimeout = errors.New("hci: timeout waiting for event")
	// ErrUnsupported is returned by Open on non-Linux platforms.
	ErrUnsupported = errors.New("hci: raw HCI sockets are only available on Linux")
	// ErrPermission is returned when the process lacks CAP_NET_RAW/CAP_NET_ADMIN.
	ErrPermission = errors.New("hci: permission denied (need CAP_NET_RAW and CAP_NET_ADMIN, or root)")
)

// Opener opens a Transport to a fixed adapter with the given receive timeout.
// It exists so higher layers (spoof, wake) can be driven by fakes in tests.
type Opener func(timeout time.Duration) (Transport, error)

// OpenerFor returns an Opener bound to adapter index dev (0 for hci0).
func OpenerFor(dev int) Opener {
	return func(timeout time.Duration) (Transport, error) { return Open(dev, timeout) }
}

// Request sends a command and waits for its Command Complete event, skipping
// unrelated events. It returns the status byte and the return parameters.
func Request(t Transport, opcode uint16, params []byte) (status byte, ret []byte, err error) {
	if err := t.Send(Command(opcode, params)); err != nil {
		return 0, nil, fmt.Errorf("hci: send 0x%04X: %w", opcode, err)
	}
	for {
		raw, err := t.Recv()
		if err != nil {
			return 0, nil, fmt.Errorf("hci: waiting for 0x%04X completion: %w", opcode, err)
		}
		ev, ok := ParseEvent(raw)
		if !ok {
			continue
		}
		if st, r, ok := ev.CommandComplete(opcode); ok {
			return st, r, nil
		}
		// A Command Status with a non-zero status means the controller
		// rejected the command and no Command Complete will follow.
		if st, ok := ev.CommandStatus(opcode); ok && st != 0 {
			return st, nil, nil
		}
	}
}

// StatusError reports a command that completed with a non-zero status.
type StatusError struct {
	Opcode uint16
	Status byte
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("hci: command 0x%04X failed: 0x%02X %s", e.Opcode, e.Status, StatusText(e.Status))
}

// ReadLocalVersion issues HCI_Read_Local_Version_Information.
func ReadLocalVersion(t Transport) (LocalVersion, error) {
	st, ret, err := Request(t, OpReadLocalVersion, nil)
	if err != nil {
		return LocalVersion{}, err
	}
	if st != 0 {
		return LocalVersion{}, &StatusError{OpReadLocalVersion, st}
	}
	return parseLocalVersion(ret)
}

// ReadBDAddr issues HCI_Read_BD_ADDR.
func ReadBDAddr(t Transport) (btaddr.Addr, error) {
	st, ret, err := Request(t, OpReadBDAddr, nil)
	if err != nil {
		return btaddr.Addr{}, err
	}
	if st != 0 {
		return btaddr.Addr{}, &StatusError{OpReadBDAddr, st}
	}
	if len(ret) < 6 {
		return btaddr.Addr{}, fmt.Errorf("hci: short Read_BD_ADDR reply (%d bytes)", len(ret))
	}
	return btaddr.FromLE(ret[:6]), nil
}

// Reset issues HCI_Reset and waits for completion.
func Reset(t Transport) error {
	st, _, err := Request(t, OpReset, nil)
	if err != nil {
		return err
	}
	if st != 0 {
		return &StatusError{OpReset, st}
	}
	return nil
}
