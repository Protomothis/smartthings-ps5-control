package wake

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/btaddr"
	"github.com/protomothis/smartthings-ps5-control/internal/hci"
	"github.com/protomothis/smartthings-ps5-control/internal/spoof"
)

// scripted is a fake adapter: every sent command gets a canned reply pushed
// into the receive queue, and Create_Connection replies with the configured
// page results in order.
type scripted struct {
	mfr      uint16
	addr     btaddr.Addr
	pages    [][]byte // raw event packets replayed per Create_Connection; nil entry = silence (local timeout)
	pageIdx  int
	writes   []btaddr.Addr
	sent     []uint16
	queue    [][]byte
	resets   int
	failOpen error
}

func complete(opcode uint16, status byte, ret ...byte) []byte {
	p := append([]byte{0x01, byte(opcode), byte(opcode >> 8), status}, ret...)
	return append([]byte{0x04, 0x0E, byte(len(p))}, p...)
}

func connComplete(status byte, addr btaddr.Addr) []byte {
	p := append([]byte{status, 0x01, 0x00}, addr.LE()...)
	p = append(p, 0x01, 0x00)
	return append([]byte{0x04, 0x03, byte(len(p))}, p...)
}

func (s *scripted) Send(p []byte) error {
	opcode := uint16(p[1]) | uint16(p[2])<<8
	s.sent = append(s.sent, opcode)
	switch opcode {
	case hci.OpReadLocalVersion:
		s.queue = append(s.queue, complete(opcode, 0, 0x0B, 0, 0, 0x0B, byte(s.mfr), byte(s.mfr>>8), 0, 0))
	case hci.OpReadBDAddr:
		s.queue = append(s.queue, complete(opcode, 0, s.addr.LE()...))
	case hci.OpReset:
		s.resets++
		s.queue = append(s.queue, complete(opcode, 0))
	case hci.OpCreateConnection:
		if s.pageIdx < len(s.pages) {
			if ev := s.pages[s.pageIdx]; ev != nil {
				s.queue = append(s.queue, []byte{0x04, 0x0F, 0x04, 0x00, 0x01, 0x05, 0x04}, ev)
			}
			s.pageIdx++
		}
	case hci.OpDisconnect:
	default:
		if opcode&0xFC00 == hci.OpVendorMask {
			a := btaddr.FromLE(p[4:10])
			s.writes = append(s.writes, a)
			s.addr = a
			s.queue = append(s.queue, complete(opcode, 0))
		}
	}
	return nil
}

func (s *scripted) Recv() ([]byte, error) {
	if len(s.queue) == 0 {
		return nil, hci.ErrTimeout
	}
	p := s.queue[0]
	s.queue = s.queue[1:]
	return p, nil
}

func (s *scripted) Close() error { s.queue = nil; return nil }

func (s *scripted) opener() hci.Opener {
	return func(time.Duration) (hci.Transport, error) {
		if s.failOpen != nil {
			return nil, s.failOpen
		}
		return s, nil
	}
}

var (
	ctrl    = btaddr.MustParse("AA:BB:CC:DD:EE:FF")
	console = btaddr.MustParse("11:22:33:44:55:66")
	dongle  = btaddr.MustParse("00:11:22:33:44:55")
)

func opts(s *scripted) Options {
	return Options{
		Adapter: "hci0", Controller: ctrl, Console: console,
		Timeout: time.Second, Attempts: 3, RetryDelay: time.Millisecond,
		open: s.opener(), devUp: func(int) error { return nil },
	}
}

func TestWakeSuccessFirstTry(t *testing.T) {
	s := &scripted{mfr: spoof.MfrBroadcom, addr: dongle, pages: [][]byte{connComplete(0x13, console)}}
	res, err := Wake(context.Background(), opts(s))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeSuccess || res.Status != 0x13 || res.Attempts != 1 || !res.Restored {
		t.Fatalf("res = %+v", res)
	}
	if res.OriginalAddr != dongle || res.Vendor != "Broadcom" {
		t.Fatalf("res = %+v", res)
	}
	// spoof to controller, then restore to dongle
	if len(s.writes) != 2 || s.writes[0] != ctrl || s.writes[1] != dongle {
		t.Fatalf("writes = %v", s.writes)
	}
	if s.addr != dongle {
		t.Fatalf("adapter left at %s", s.addr)
	}
}

func TestWakeRetriesThenSucceeds(t *testing.T) {
	s := &scripted{mfr: spoof.MfrIntel, addr: dongle, pages: [][]byte{
		connComplete(hci.StatusPageTimeout, console),
		nil, // silence → local timeout
		connComplete(0x00, console),
	}}
	res, err := Wake(context.Background(), opts(s))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeSuccess || res.Attempts != 3 || res.Status != 0 {
		t.Fatalf("res = %+v", res)
	}
	// success with status 0 → we disconnect the link ourselves
	found := false
	for _, op := range s.sent {
		if op == hci.OpDisconnect {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a Disconnect after a live connection")
	}
	if s.addr != dongle {
		t.Fatalf("adapter left at %s", s.addr)
	}
}

func TestWakeAllTimeouts(t *testing.T) {
	s := &scripted{mfr: spoof.MfrBroadcom, addr: dongle, pages: [][]byte{
		connComplete(hci.StatusPageTimeout, console),
		connComplete(hci.StatusPageTimeout, console),
		connComplete(hci.StatusPageTimeout, console),
	}}
	res, err := Wake(context.Background(), opts(s))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeTimeout || res.Attempts != 3 || !res.Restored {
		t.Fatalf("res = %+v", res)
	}
}

func TestWakeCommandRejected(t *testing.T) {
	// Command Status with 0x0C and no Connection Complete
	rejected := []byte{0x04, 0x0F, 0x04, hci.StatusCommandDisallowed, 0x01, 0x05, 0x04}
	s := &scripted{mfr: spoof.MfrBroadcom, addr: dongle}
	s.pages = [][]byte{rejected, rejected, rejected}
	// The fake emits an accepted Command Status first; page() skips it and
	// then returns on the 0x0C Command Status.
	res, err := Wake(context.Background(), opts(s))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeError || res.Status != hci.StatusCommandDisallowed {
		t.Fatalf("res = %+v", res)
	}
}

func TestWakeUnsupportedChip(t *testing.T) {
	s := &scripted{mfr: spoof.MfrRealtek, addr: dongle}
	res, err := Wake(context.Background(), opts(s))
	if !errors.Is(err, spoof.ErrUnsupportedAdapter) {
		t.Fatalf("err = %v", err)
	}
	if res.Version.Manufacturer != spoof.MfrRealtek || len(s.writes) != 0 {
		t.Fatalf("res = %+v writes = %v", res, s.writes)
	}
}

func TestWakeOpenFailure(t *testing.T) {
	s := &scripted{mfr: spoof.MfrBroadcom, addr: dongle, failOpen: hci.ErrPermission}
	_, err := Wake(context.Background(), opts(s))
	if !errors.Is(err, hci.ErrPermission) {
		t.Fatalf("err = %v", err)
	}
}

func TestWakeContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &scripted{mfr: spoof.MfrBroadcom, addr: dongle}
	_, err := Wake(ctx, opts(s))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseAdapter(t *testing.T) {
	if n, err := ParseAdapter("hci1"); err != nil || n != 1 {
		t.Fatalf("hci1 → %d %v", n, err)
	}
	for _, bad := range []string{"", "hci", "0", "hci0x", "HCI0"} {
		if _, err := ParseAdapter(bad); err == nil {
			t.Fatalf("%q should fail", bad)
		}
	}
}

func TestCreateConnectionTargetsConsole(t *testing.T) {
	s := &scripted{mfr: spoof.MfrBroadcom, addr: dongle, pages: [][]byte{connComplete(0x13, console)}}
	// capture the raw page packet by wrapping Send
	var pagePkt []byte
	wrapped := &captureTransport{inner: s, onSend: func(p []byte) {
		if len(p) >= 3 && uint16(p[1])|uint16(p[2])<<8 == hci.OpCreateConnection {
			pagePkt = append([]byte(nil), p...)
		}
	}}
	o := opts(s)
	o.open = func(time.Duration) (hci.Transport, error) { return wrapped, nil }
	if _, err := Wake(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pagePkt, hci.CreateConnection(console)) {
		t.Fatalf("page packet % X", pagePkt)
	}
}

type captureTransport struct {
	inner  *scripted
	onSend func([]byte)
}

func (c *captureTransport) Send(p []byte) error   { c.onSend(p); return c.inner.Send(p) }
func (c *captureTransport) Recv() ([]byte, error) { return c.inner.Recv() }
func (c *captureTransport) Close() error          { return c.inner.Close() }
