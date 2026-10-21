package spoof

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/btaddr"
	"github.com/protomothis/smartthings-ps5-control/internal/hci"
)

type fake struct {
	sent  [][]byte
	queue [][]byte
}

func (f *fake) Send(p []byte) error { f.sent = append(f.sent, append([]byte(nil), p...)); return nil }
func (f *fake) Recv() ([]byte, error) {
	if len(f.queue) == 0 {
		return nil, hci.ErrTimeout
	}
	p := f.queue[0]
	f.queue = f.queue[1:]
	return p, nil
}
func (f *fake) Close() error { return nil }

func opener(f *fake) hci.Opener {
	return func(time.Duration) (hci.Transport, error) { return f, nil }
}

func complete(opcode uint16, status byte) []byte {
	return []byte{0x04, 0x0E, 0x04, 0x01, byte(opcode), byte(opcode >> 8), status}
}

func TestBroadcomApplyWritesAndResets(t *testing.T) {
	f := &fake{queue: [][]byte{complete(0xFC01, 0), complete(hci.OpReset, 0)}}
	s, _ := Lookup(MfrBroadcom)
	if err := s.Apply(opener(f), btaddr.MustParse("AA:BB:CC:DD:EE:FF"), nil); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x01, 0x01, 0xFC, 6, 0xFF, 0xEE, 0xDD, 0xCC, 0xBB, 0xAA}
	if len(f.sent) != 2 || !bytes.Equal(f.sent[0], want) || !bytes.Equal(f.sent[1], hci.Command(hci.OpReset, nil)) {
		t.Fatalf("sent % X", f.sent)
	}
}

func TestIntelRequiresReset(t *testing.T) {
	f := &fake{queue: [][]byte{complete(0xFC31, 0)}} // no reset completion → timeout
	s, _ := Lookup(MfrIntel)
	err := s.Apply(opener(f), btaddr.MustParse("AA:BB:CC:DD:EE:FF"), nil)
	if !errors.Is(err, hci.ErrTimeout) {
		t.Fatalf("err = %v", err)
	}
}

func TestCypressToleratesResetTimeout(t *testing.T) {
	f := &fake{queue: [][]byte{complete(0xFC01, 0)}}
	s, _ := Lookup(MfrCypress)
	if err := s.Apply(opener(f), btaddr.MustParse("AA:BB:CC:DD:EE:FF"), nil); err != nil {
		t.Fatal(err)
	}
}

func TestMediaTekNoReset(t *testing.T) {
	f := &fake{queue: [][]byte{complete(0xFC1A, 0)}}
	s, _ := Lookup(MfrMediaTek)
	if err := s.Apply(opener(f), btaddr.MustParse("AA:BB:CC:DD:EE:FF"), nil); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 {
		t.Fatalf("expected a single write, sent % X", f.sent)
	}
}

func TestAtherosTaggedFrame(t *testing.T) {
	f := &fake{queue: [][]byte{complete(0xFC0B, 0), complete(hci.OpReset, 0)}}
	s, _ := Lookup(MfrAtheros)
	if err := s.Apply(opener(f), btaddr.MustParse("AA:BB:CC:DD:EE:FF"), nil); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x01, 0x0B, 0xFC, 10, 0x01, 0x01, 0x00, 0x06, 0xFF, 0xEE, 0xDD, 0xCC, 0xBB, 0xAA}
	if !bytes.Equal(f.sent[0], want) {
		t.Fatalf("sent % X want % X", f.sent[0], want)
	}
}

func TestWriteRejected(t *testing.T) {
	f := &fake{queue: [][]byte{complete(0xFC01, 0x01)}}
	s, _ := Lookup(MfrBroadcom)
	err := s.Apply(opener(f), btaddr.MustParse("AA:BB:CC:DD:EE:FF"), nil)
	var se *hci.StatusError
	if !errors.As(err, &se) || se.Status != 0x01 {
		t.Fatalf("err = %v", err)
	}
}

func TestDetectUnsupported(t *testing.T) {
	// Read_Local_Version reply with manufacturer 0x005D (Realtek)
	f := &fake{queue: [][]byte{{0x04, 0x0E, 0x0C, 0x01, 0x01, 0x10, 0x00, 0x0B, 0x00, 0x00, 0x0B, 0x5D, 0x00, 0x00, 0x00}}}
	ver, _, err := Detect(opener(f))
	if !errors.Is(err, ErrUnsupportedAdapter) || ver.Manufacturer != MfrRealtek {
		t.Fatalf("ver=%+v err=%v", ver, err)
	}
	var ue *UnsupportedError
	if !errors.As(err, &ue) || ue.Manufacturer != MfrRealtek {
		t.Fatalf("err = %v", err)
	}
}

func TestDetectSupported(t *testing.T) {
	f := &fake{queue: [][]byte{{0x04, 0x0E, 0x0C, 0x01, 0x01, 0x10, 0x00, 0x0B, 0x00, 0x00, 0x0B, 0x0F, 0x00, 0x00, 0x00}}}
	_, s, err := Detect(opener(f))
	if err != nil || s.Vendor != "Broadcom" {
		t.Fatalf("s=%+v err=%v", s, err)
	}
}
