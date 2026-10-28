package remoteplay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeConsole answers the registration search on UDP and the REST/control
// protocol on TCP, on the same port number, the way a PS5 does on 9295.
type fakeConsole struct {
	t    *testing.T
	ln   net.Listener
	udp  net.PacketConn
	port int
	wg   sync.WaitGroup

	pin         uint32
	accountID   string
	rpKey       []byte
	registKey   string
	serverNonce []byte
	// login is what the console answers to Login: "ok", "passcode", "fail".
	login string
	// hangUp closes the control connection after Standby, as a console
	// going to rest does.
	hangUp bool

	mu      sync.Mutex
	events  []string
	regBody []byte
}

func newFakeConsole(t *testing.T, pin uint32) *fakeConsole {
	t.Helper()
	f := &fakeConsole{
		t:           t,
		pin:         pin,
		accountID:   "AAAAAAAAAEI=",
		rpKey:       unhex(t, vecRPKey),
		registKey:   vecRegistKey,
		serverNonce: unhex(t, vecServerNonce),
		login:       "ok",
		hangUp:      true,
	}
	for range 20 {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		udp, err := net.ListenPacket("udp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			ln.Close()
			continue
		}
		f.ln, f.udp, f.port = ln, udp, port
		break
	}
	if f.ln == nil {
		t.Fatal("no port free for both TCP and UDP")
	}
	if loopbackFiltered(t) {
		// Keep the UDP port (the search goes to the same number) but carry
		// the TCP side over pipes.
		f.ln.Close()
		pl := &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
		f.ln = pl
		old := dialREST
		dialREST = func(ctx context.Context, _ *Client) (net.Conn, error) { return pl.dial(ctx) }
		t.Cleanup(func() { dialREST = old })
	}
	f.wg.Add(2)
	go f.serveUDP()
	go f.serveTCP()
	t.Cleanup(func() {
		f.ln.Close()
		f.udp.Close()
		f.wg.Wait()
	})
	return f
}

func (f *fakeConsole) client() *Client {
	return &Client{Host: "127.0.0.1", RESTPort: f.port, SearchLocalPort: 0, Timeout: 3 * time.Second}
}

func (f *fakeConsole) event(e string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeConsole) eventList() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.events, " ")
}

func (f *fakeConsole) serveUDP() {
	defer f.wg.Done()
	buf := make([]byte, 512)
	for {
		n, from, err := f.udp.ReadFrom(buf)
		if err != nil {
			return
		}
		if string(buf[:n]) == "SRC3" {
			f.event("search")
			f.udp.WriteTo([]byte("RES3\nhost-id:0123456789ab\n"), from)
		}
	}
}

func (f *fakeConsole) serveTCP() {
	defer f.wg.Done()
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			f.handle(conn)
		}()
	}
}

func (f *fakeConsole) handle(conn net.Conn) {
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		f.t.Errorf("console: read request: %v", err)
		return
	}
	if ua := req.Header.Get("User-Agent"); ua != "remoteplay Windows" {
		f.t.Errorf("console: User-Agent %q", ua)
	}
	if v := req.Header.Get("RP-Version"); v != "1.0" {
		f.t.Errorf("console: RP-Version %q", v)
	}
	switch req.URL.Path {
	case "/sie/ps5/rp/sess/rgst":
		f.register(conn, req)
	case "/sie/ps5/rp/sess/init":
		f.event("init")
		if req.Method != http.MethodGet || req.Header.Get("RP-RegistKey") != f.registKey {
			reply(conn, "403 Forbidden", "RP-Application-Reason: 80108bff\r\n", nil)
			return
		}
		reply(conn, "200 OK", "RP-Nonce: "+base64.StdEncoding.EncodeToString(f.serverNonce)+"\r\n", nil)
	case "/sie/ps5/rp/sess/ctrl":
		f.control(conn, br, req)
	default:
		reply(conn, "404 Not Found", "", nil)
	}
}

func reply(w io.Writer, status, headers string, body []byte) {
	fmt.Fprintf(w, "HTTP/1.1 %s\r\n%sContent-Length: %d\r\n\r\n", status, headers, len(body))
	if len(body) > 0 {
		w.Write(body)
	}
}

// register recovers the client nonce from the aeropause signature in the
// preface, rebuilds the PIN cipher with the console's own PIN, and answers
// with the encrypted registration record.
func (f *fakeConsole) register(conn net.Conn, req *http.Request) {
	f.event("register")
	body, err := io.ReadAll(req.Body)
	if err != nil || len(body) <= paddingBytes {
		f.t.Errorf("console: registration body of %d bytes: %v", len(body), err)
		return
	}
	f.mu.Lock()
	f.regBody = body
	f.mu.Unlock()

	var sig [NonceLength]byte
	copy(sig[8:], body[0xc7:0xc7+8])
	copy(sig[:8], body[0x191:0x191+8])
	off := int(body[0] >> 3)
	nonce := make([]byte, NonceLength)
	for i := range nonce {
		nonce[i] = (sig[i] - 0xD3 - byte(i)) ^ aeroKey[i*0x20+off]
	}
	pc, err := newPinCrypto(f.pin, nonce)
	if err != nil {
		f.t.Errorf("console: %v", err)
		return
	}
	if !bytes.Equal(body[:paddingBytes], pc.preface) {
		f.t.Error("console: preface does not match the recovered nonce")
	}
	plain, _ := pc.decrypt(body[paddingBytes:])
	rec := parseRecord(plain)
	if rec["Client-Type"] != clientType || rec["Np-AccountId"] != f.accountID {
		// What a real console says when the PIN is wrong: the record
		// decrypts to garbage.
		reply(conn, "403 Forbidden", "RP-Application-Reason: 80108b09\r\n", nil)
		return
	}
	enc, _ := pc.encrypt([]byte("AP-Ssid: ssid\r\nAP-Bssid: 0a1b2c3d4e5f\r\nAP-Key: apkey\r\nAP-Name: PS5-123\r\n" +
		"PS5-Mac: a1b2c3d4e5f6\r\nPS5-RegistKey: " + f.registKey + "\r\nPS5-Nickname: PS5-123\r\n" +
		"RP-KeyType: 2\r\nRP-Key: " + hex.EncodeToString(f.rpKey) + "\r\n"))
	reply(conn, "200 OK", "", enc)
}

// control checks the encrypted headers, then plays the console side of the
// packet stream. Its cipher mirrors the client's: it decrypts with the
// counters the client encrypted with, and the other way round.
func (f *fakeConsole) control(conn net.Conn, br *bufio.Reader, req *http.Request) {
	f.event("ctrl")
	sc, _ := newSessionCrypto(f.rpKey, f.serverNonce)
	auth := make([]byte, NonceLength)
	copy(auth, unhex(f.t, f.registKey))
	did := append(append([]byte{}, didPrefix...), make([]byte, 22)...)
	for _, h := range []struct {
		name string
		want []byte
	}{
		{"RP-Auth", auth},
		{"RP-Did", did},
		{"RP-OSType", []byte("Win10.0.0")},
		{"RP-StartBitrate", []byte{0, 0, 0, 0}},
		{"RP-StreamingType", []byte{1, 0, 0, 0}},
	} {
		raw, err := base64.StdEncoding.DecodeString(req.Header.Get(h.name))
		if err != nil {
			f.t.Errorf("console: %s: %v", h.name, err)
			return
		}
		if got, _ := sc.decrypt(raw); !bytes.Equal(got, h.want) {
			f.t.Errorf("console: %s decrypts to %x, want %x", h.name, got, h.want)
		}
	}
	for name, want := range map[string]string{"RP-ControllerType": "3", "RP-ClientType": "11", "RP-ConPath": "1"} {
		if got := req.Header.Get(name); got != want {
			f.t.Errorf("console: %s = %q, want %q", name, got, want)
		}
	}
	st, _ := sc.encrypt([]byte{0x02, 0x00})
	reply(conn, "200 OK", "RP-Server-Type: "+base64.StdEncoding.EncodeToString(st)+"\r\n", nil)

	send := func(typ uint16, payload []byte) {
		pkt, _ := encodePacket(sc, typ, payload)
		conn.Write(pkt)
	}
	expect := func(want uint16) bool {
		typ, payload, err := readPacket(br, sc)
		if err != nil || typ != want || len(payload) != 0 {
			f.t.Errorf("console: got packet %#x %x (%v), want %#x", typ, payload, err, want)
			return false
		}
		return true
	}

	if !expect(cmdLogin) {
		return
	}
	f.event("login")
	send(respHeartbeat, nil)
	if !expect(cmdHeartbeat) {
		return
	}
	f.event("heartbeat")
	switch f.login {
	case "passcode":
		send(respPasscode, nil)
		return
	case "fail":
		send(respLogin, []byte{1})
		return
	}
	send(respLogin, []byte{0})
	if !expect(cmdStandby) {
		return
	}
	f.event("standby")
	if f.hangUp {
		return
	}
	// Stay up; the client gives up after its linger time.
	io.Copy(io.Discard, br)
}

func TestRegisterStandbyWake(t *testing.T) {
	f := newFakeConsole(t, 13374201)
	c := f.client()
	vecNonce := unhex(t, "1337deadbeefc0ffee42636869616b69")
	c.nonce = func() ([]byte, error) { return vecNonce, nil }
	ctx := context.Background()

	reg, err := c.Register(ctx, "AAAAAAAAAEI=", "13374201")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	f.mu.Lock()
	wire := f.regBody
	f.mu.Unlock()
	eqHex(t, "registration body on the wire", wire, vecRegistrationBody)
	if !reg.Valid() || reg.RegistKey != vecRegistKey || reg.RPKey != vecRPKey || reg.AccountID != "AAAAAAAAAEI=" ||
		reg.Nickname != "PS5-123" || reg.Mac != "a1b2c3d4e5f6" || reg.APName != "PS5-123" || reg.APBssid != "0a1b2c3d4e5f" ||
		reg.RegisteredAt.IsZero() {
		t.Errorf("Register = %+v", reg)
	}

	if err := c.Standby(ctx, reg); err != nil {
		t.Fatalf("Standby: %v", err)
	}
	if got, want := f.eventList(), "search register init ctrl login heartbeat standby"; got != want {
		t.Errorf("console saw %q, want %q", got, want)
	}

	wake, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer wake.Close()
	c.WakePort = wake.LocalAddr().(*net.UDPAddr).Port
	warmUDP(t, wake)
	if err := c.Wake(ctx, reg); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	wake.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 512)
	n, _, err := wake.ReadFrom(buf)
	if err != nil {
		t.Fatalf("no WAKEUP: %v", err)
	}
	if got, want := string(buf[:n]), string(WakeMessage("19088743")); got != want {
		t.Errorf("WAKEUP:\n%q\nwant\n%q", got, want)
	}
}

func TestRegisterWrongPIN(t *testing.T) {
	f := newFakeConsole(t, 13374201)
	_, err := f.client().Register(context.Background(), "AAAAAAAAAEI=", "13374202")
	var re *ReasonError
	if !errors.As(err, &re) || re.Status != 403 || re.Code != "80108b09" {
		t.Fatalf("Register with wrong PIN: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid PIN") {
		t.Errorf("error text %q", err)
	}
}

func TestRegisterRejectsBadInput(t *testing.T) {
	c := &Client{Host: "127.0.0.1", RESTPort: 1, Timeout: time.Second}
	for _, tc := range []struct{ account, pin string }{
		{"AAAAAAAAAEI=", "1337420"},
		{"AAAAAAAAAEI=", "1337420x"},
		{"not base64", "13374201"},
		{"AAAA", "13374201"},
	} {
		if _, err := c.Register(context.Background(), tc.account, tc.pin); err == nil {
			t.Errorf("Register(%q, %q) succeeded", tc.account, tc.pin)
		}
	}
}

func TestRegisterSearchTimeout(t *testing.T) {
	silent, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	c := &Client{Host: "127.0.0.1", RESTPort: silent.LocalAddr().(*net.UDPAddr).Port, Timeout: 300 * time.Millisecond}
	start := time.Now()
	_, err = c.Register(context.Background(), "AAAAAAAAAEI=", "13374201")
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("Register without a search answer: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("search took %v", time.Since(start))
	}
}

func TestStandbyOutcomes(t *testing.T) {
	old := standbyLinger
	standbyLinger = 300 * time.Millisecond
	defer func() { standbyLinger = old }()

	reg := Registration{AccountID: "AAAAAAAAAEI=", RegistKey: vecRegistKey, RPKey: vecRPKey}
	for _, tc := range []struct {
		name    string
		login   string
		hangUp  bool
		wantErr func(error) bool
		events  string
	}{
		{"console stays up after standby", "ok", false, func(err error) bool { return err == nil }, "init ctrl login heartbeat standby"},
		{"passcode", "passcode", true, func(err error) bool { return errors.Is(err, ErrPasscode) }, "init ctrl login heartbeat"},
		{"login refused", "fail", true, func(err error) bool {
			return err != nil && strings.Contains(err.Error(), "refused login (result 1)")
		}, "init ctrl login heartbeat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeConsole(t, 13374201)
			f.login, f.hangUp = tc.login, tc.hangUp
			err := f.client().Standby(context.Background(), reg)
			if !tc.wantErr(err) {
				t.Errorf("Standby: %v", err)
			}
			if got := f.eventList(); got != tc.events {
				t.Errorf("console saw %q, want %q", got, tc.events)
			}
		})
	}
}

func TestStandbyWrongRegistKey(t *testing.T) {
	f := newFakeConsole(t, 13374201)
	reg := Registration{RegistKey: "3031323334353638", RPKey: vecRPKey}
	err := f.client().Standby(context.Background(), reg)
	var re *ReasonError
	if !errors.As(err, &re) || re.Status != 403 {
		t.Fatalf("Standby with an unknown key: %v", err)
	}
}

func TestStandbyCancelled(t *testing.T) {
	f := newFakeConsole(t, 13374201)
	f.hangUp = false
	ctx, cancel := context.WithCancel(context.Background())
	reg := Registration{RegistKey: vecRegistKey, RPKey: vecRPKey}
	done := make(chan error, 1)
	go func() { done <- f.client().Standby(ctx, reg) }()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.HasSuffix(f.eventList(), "standby") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Standby after cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Standby did not return after cancel")
	}
}

var (
	loopbackOnce     sync.Once
	loopbackMangled  bool
	loopbackProbeErr error
)

// loopbackFiltered reports whether something on this machine (a filtering
// proxy such as AdGuard for Windows) sits in loopback TCP and treats the
// bytes after an HTTP response as a new request, as it would break the
// control stream. The fake console then runs over in-memory pipes; CI and
// the server's container see plain loopback.
func loopbackFiltered(t *testing.T) bool {
	loopbackOnce.Do(func() {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			loopbackProbeErr = err
			return
		}
		defer ln.Close()
		go func() {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(2 * time.Second))
			br := bufio.NewReader(c)
			if _, err := http.ReadRequest(br); err != nil {
				return
			}
			io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
			var pkt [8]byte
			if _, err := io.ReadFull(br, pkt[:]); err != nil {
				return
			}
			c.Write([]byte{0, 0, 0, 0, 0, 0xfe, 0, 0})
			io.Copy(io.Discard, br)
		}()
		conn, err := net.Dial("tcp4", ln.Addr().String())
		if err != nil {
			loopbackProbeErr = err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, r, err := rawRequest(conn, "GET", "probe", "/probe", nil, nil) //nolint:bodyclose // probe
		if err != nil {
			loopbackMangled = true
			return
		}
		conn.Write([]byte{0, 0, 0, 0, 0, 5, 0, 0})
		var got [8]byte
		_, err = io.ReadFull(r, got[:])
		loopbackMangled = err != nil || got != [8]byte{0, 0, 0, 0, 0, 0xfe, 0, 0}
	})
	if loopbackProbeErr != nil {
		t.Fatal(loopbackProbeErr)
	}
	if loopbackMangled {
		t.Log("loopback TCP is rewritten by a local HTTP filter; fake console runs over net.Pipe")
	}
	return loopbackMangled
}

// pipeListener hands out the far ends of net.Pipe pairs.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *pipeListener) dial(ctx context.Context) (net.Conn, error) {
	a, b := net.Pipe()
	select {
	case l.conns <- b:
		return a, nil
	case <-l.done:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// warmUDP gets a first datagram through to l. Some Windows hosts drop the
// first datagram to a new local listener (the registration search survives
// that by resending); WAKEUP is sent once, so the test opens the path first.
func warmUDP(t *testing.T, l net.PacketConn) {
	t.Helper()
	conn, err := net.Dial("udp4", l.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	buf := make([]byte, 64)
	for range 5 {
		conn.Write([]byte("warm"))
		l.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if n, _, err := l.ReadFrom(buf); err == nil && string(buf[:n]) == "warm" {
			return
		}
	}
	t.Fatal("no datagram reaches the wake listener")
}
