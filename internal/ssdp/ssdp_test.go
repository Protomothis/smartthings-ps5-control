package ssdp

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseMSearch(t *testing.T) {
	ok := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: " + ST + "\r\n\r\n"
	m, good := ParseMSearch([]byte(ok))
	if !good || m.ST != ST || m.MX != 2 {
		t.Fatalf("%+v %v", m, good)
	}
	m, good = ParseMSearch([]byte("m-search * HTTP/1.0\nMX: 99\nST: SSDP:ALL\n"))
	if !good || m.ST != "ssdp:all" || m.MX != maxMX {
		t.Fatalf("wildcard, clamped: %+v %v", m, good)
	}
	for _, bad := range []string{
		"",
		"NOTIFY * HTTP/1.1\r\nST: " + ST + "\r\n",
		"M-SEARCH * HTTP/1.1\r\nST: urn:schemas-upnp-org:device:MediaRenderer:1\r\n",
		"M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:something\"\r\nST: " + ST + "\r\n",
		strings.Repeat("x", maxPacket+1),
	} {
		if _, good := ParseMSearch([]byte(bad)); good {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestResponseText(t *testing.T) {
	info := Info{ID: "abc123", Name: "PS5\r\nX", Version: "v0.1.0", Port: 8580}
	text := ResponseText(info, net.IPv4(192, 168, 1, 50), time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC))
	for _, want := range []string{
		"HTTP/1.1 200 OK\r\n",
		"LOCATION: http://192.168.1.50:8580/ssdp.json\r\n",
		"ST: " + ST + "\r\n",
		"USN: uuid:abc123::" + ST + "\r\n",
		"PS5CTL-NAME: PS5  X\r\n",
		"PS5CTL-PORT: 8580\r\n",
		"DATE: Wed, 28 Oct 2026 01:02:03 GMT\r\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
	if !strings.HasSuffix(text, "\r\n\r\n") {
		t.Fatal("no terminating blank line")
	}
}

// The serve loop over plain unicast sockets on loopback: an M-SEARCH gets one
// answer, a repeat within a second gets none, a foreign ST gets none.
func TestServeAnswers(t *testing.T) {
	recv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip("no loopback udp:", err)
	}
	searcher, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer searcher.Close()
	r := &Responder{Info: func() Info { return Info{ID: "id1", Name: "PS5", Version: "t", Port: 8580} }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.serve(ctx, &socket{recv: recv, send: recv, localIP: net.IPv4(127, 0, 0, 1)}); close(done) }()

	search := "M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nMX: 0\r\nST: " + ST + "\r\n\r\n"
	buf := make([]byte, 2048)
	// On some hosts the first datagram to a fresh loopback listener is
	// dropped (seen on this Windows box with a filtering driver), so retry
	// after the per-source rate limit has passed.
	var n int
	for attempt := 0; attempt < 4; attempt++ {
		if _, err = searcher.WriteToUDP([]byte(search), recv.LocalAddr().(*net.UDPAddr)); err != nil {
			t.Fatal(err)
		}
		_ = searcher.SetReadDeadline(time.Now().Add(1200 * time.Millisecond))
		n, _, err = searcher.ReadFromUDP(buf)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal("no answer:", err)
	}
	if !strings.Contains(string(buf[:n]), "USN: uuid:id1::") || !strings.Contains(string(buf[:n]), "LOCATION: http://127.0.0.1:8580/ssdp.json") {
		t.Fatalf("answer = %q", buf[:n])
	}

	// rate limited repeat, then a foreign target: silence
	_, _ = searcher.WriteToUDP([]byte(search), recv.LocalAddr().(*net.UDPAddr))
	_, _ = searcher.WriteToUDP([]byte("M-SEARCH * HTTP/1.1\r\nST: upnp:rootdevice\r\n\r\n"), recv.LocalAddr().(*net.UDPAddr))
	_ = searcher.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	if n, _, err := searcher.ReadFromUDP(buf); err == nil {
		t.Fatalf("unexpected answer %q", buf[:n])
	}

	cancel()
	recv.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not return")
	}
}
