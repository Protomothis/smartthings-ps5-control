package hci

import (
	"bytes"
	"errors"
	"testing"

	"github.com/protomothis/smartthings-ps5-control/internal/btaddr"
)

func TestCreateConnectionPacket(t *testing.T) {
	pkt := CreateConnection(btaddr.MustParse("11:22:33:44:55:66"))
	want := []byte{
		0x01,       // command
		0x05, 0x04, // opcode 0x0405 LE
		13,                                 // param len
		0x66, 0x55, 0x44, 0x33, 0x22, 0x11, // bd_addr LE
		0x18, 0xCC, // packet type
		0x02, 0x00, // page scan rep mode, reserved
		0x00, 0x00, // clock offset
		0x01, // role switch
	}
	if !bytes.Equal(pkt, want) {
		t.Fatalf("got % X\nwant % X", pkt, want)
	}
}

func TestParseEvents(t *testing.T) {
	// Command Complete for Read_BD_ADDR: num_pkts=1, opcode 0x1009, status 0, addr LE
	raw := []byte{0x04, 0x0E, 0x0A, 0x01, 0x09, 0x10, 0x00, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11}
	ev, ok := ParseEvent(raw)
	if !ok {
		t.Fatal("ParseEvent failed")
	}
	st, ret, ok := ev.CommandComplete(OpReadBDAddr)
	if !ok || st != 0 || btaddr.FromLE(ret) != btaddr.MustParse("11:22:33:44:55:66") {
		t.Fatalf("CommandComplete: ok=%v st=%d ret=% X", ok, st, ret)
	}
	if _, _, ok := ev.CommandComplete(OpReset); ok {
		t.Fatal("opcode mismatch should not match")
	}

	// Command Status: status 0x0C, num_pkts 1, opcode 0x0405
	raw = []byte{0x04, 0x0F, 0x04, 0x0C, 0x01, 0x05, 0x04}
	ev, _ = ParseEvent(raw)
	if st, ok := ev.CommandStatus(OpCreateConnection); !ok || st != StatusCommandDisallowed {
		t.Fatalf("CommandStatus: ok=%v st=%#x", ok, st)
	}

	// Connection Complete: status 0x04 (page timeout), handle 0x0000, addr, ACL, no encryption
	raw = []byte{0x04, 0x03, 0x0B, 0x04, 0x00, 0x00, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 0x01, 0x00}
	ev, _ = ParseEvent(raw)
	cc, ok := ev.ConnectionComplete()
	if !ok || cc.Status != StatusPageTimeout || cc.Addr.String() != "11:22:33:44:55:66" || cc.LinkType != 1 {
		t.Fatalf("ConnectionComplete: ok=%v %+v", ok, cc)
	}

	// Truncated packet
	if _, ok := ParseEvent([]byte{0x04, 0x03, 0x0B, 0x04}); ok {
		t.Fatal("truncated event should not parse")
	}
	// Not an event
	if _, ok := ParseEvent([]byte{0x02, 0x00, 0x00, 0x00}); ok {
		t.Fatal("ACL packet should not parse as event")
	}
}

// fakeTransport replays scripted event packets.
type fakeTransport struct {
	sent  [][]byte
	queue [][]byte
}

func (f *fakeTransport) Send(p []byte) error {
	f.sent = append(f.sent, append([]byte(nil), p...))
	return nil
}
func (f *fakeTransport) Recv() ([]byte, error) {
	if len(f.queue) == 0 {
		return nil, ErrTimeout
	}
	p := f.queue[0]
	f.queue = f.queue[1:]
	return p, nil
}
func (f *fakeTransport) Close() error { return nil }

func TestRequestSkipsUnrelatedEvents(t *testing.T) {
	ft := &fakeTransport{queue: [][]byte{
		{0x04, 0x0E, 0x04, 0x01, 0x03, 0x0C, 0x00},                                                 // complete for Reset (unrelated)
		{0x04, 0x0E, 0x0C, 0x01, 0x01, 0x10, 0x00, 0x0B, 0x34, 0x12, 0x0B, 0x0F, 0x00, 0x78, 0x56}, // complete for Read_Local_Version
	}}
	v, err := ReadLocalVersion(ft)
	if err != nil {
		t.Fatal(err)
	}
	if v.Manufacturer != 0x000F || v.HCIVersion != 0x0B || v.HCIRevision != 0x1234 || v.LMPSubversion != 0x5678 {
		t.Fatalf("got %+v", v)
	}
	if len(ft.sent) != 1 || !bytes.Equal(ft.sent[0], Command(OpReadLocalVersion, nil)) {
		t.Fatalf("sent % X", ft.sent)
	}
}

func TestRequestTimeout(t *testing.T) {
	ft := &fakeTransport{}
	_, err := ReadBDAddr(ft)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v", err)
	}
}

func TestRequestCommandStatusRejection(t *testing.T) {
	ft := &fakeTransport{queue: [][]byte{{0x04, 0x0F, 0x04, 0x01, 0x01, 0x03, 0x0C}}}
	err := Reset(ft)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != StatusUnknownCommand {
		t.Fatalf("err = %v", err)
	}
}
