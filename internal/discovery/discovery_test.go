package discovery

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	on := "HTTP/1.1 200 Ok\nhost-id:0123456789AB\nhost-type:PS5\nhost-name:MyPS5\nhost-request-port:997\ndevice-discovery-protocol-version:00030010\nsystem-version:08000000\n"
	st, err := Parse([]byte(on))
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateOn || st.Code != 200 || st.Fields["host-type"] != "PS5" || st.Fields["host-name"] != "MyPS5" {
		t.Fatalf("got %+v", st)
	}

	st, err = Parse([]byte("HTTP/1.1 620 Server Standby\r\nhost-id:0123456789AB\r\nhost-type:PS5\r\n"))
	if err != nil || st.State != StateStandby || st.Code != 620 {
		t.Fatalf("standby: %+v %v", st, err)
	}

	st, err = Parse([]byte("HTTP/1.1 503 Busy\n"))
	if err != nil || st.State != StateUnknown {
		t.Fatalf("unknown: %+v %v", st, err)
	}

	if _, err := Parse([]byte("garbage")); err == nil {
		t.Fatal("garbage should fail")
	}
}

func TestProbeTimeoutIsOff(t *testing.T) {
	// Nothing listens on this loopback port, so the probe must time out
	// and report "off" without an error.
	st, err := Probe(context.Background(), "127.0.0.1", 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateOff {
		t.Fatalf("state = %s", st.State)
	}
}

func TestProbeAgainstFakeConsole(t *testing.T) {
	// Probe targets the fixed port 9302, so run a fake console there and skip
	// if it is already taken on this machine.
	srv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: Port})
	if err != nil {
		t.Skipf("port %d busy: %v", Port, err)
	}
	defer srv.Close()
	go func() {
		buf := make([]byte, 512)
		n, from, err := srv.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if string(buf[:n]) != probePayload {
			return
		}
		srv.WriteToUDP([]byte("HTTP/1.1 620 Server Standby\nhost-type:PS5\n"), from)
	}()
	st, err := Probe(context.Background(), "127.0.0.1", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateStandby || st.Fields["host-type"] != "PS5" {
		t.Fatalf("got %+v", st)
	}
}

func TestUnreachableErrors(t *testing.T) {
	// A console that is booting answers the probe with ICMP port unreachable;
	// a host that is away yields host/net unreachable. Those are "off", not
	// failures. Anything else stays an error.
	wrap := func(errno syscall.Errno) error {
		return &net.OpError{Op: "read", Net: "udp", Err: &os.SyscallError{Syscall: "recvfrom", Err: errno}}
	}
	for _, e := range []syscall.Errno{syscall.ECONNREFUSED, syscall.EHOSTUNREACH, syscall.ENETUNREACH} {
		if !unreachable(wrap(e)) {
			t.Errorf("%v should count as unreachable", e)
		}
	}
	if unreachable(wrap(syscall.EACCES)) {
		t.Error("EACCES is a local failure")
	}
	if unreachable(errors.New("plain")) {
		t.Error("a plain error is not unreachable")
	}
}
