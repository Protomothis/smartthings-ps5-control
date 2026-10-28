// Package ssdp answers the SmartThings Edge driver's discovery search.
//
// The driver cannot ask the user for an IP address, so it multicasts an SSDP
// M-SEARCH for our search target on the LAN and this responder answers with
// a LOCATION pointing at GET /ssdp.json on the HTTP port. The answer carries
// only what the driver needs to create the device: a stable instance id, a
// name and the port. Nothing here is authenticated; the API keeps its own
// secret.
//
// Modelled on the responder of smartthings-pc-control (service/stapi/ssdp.go).
package ssdp

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ST is the search target the driver sends and we answer.
	ST = "urn:smartthings-ps5-control:device:ps5:1"
	// searchAll is the wildcard every SSDP device answers.
	searchAll = "ssdp:all"
	// Group and Port are the SSDP multicast endpoint.
	Group = "239.255.255.250"
	Port  = 1900
	// maxAge is the CACHE-CONTROL lifetime of one answer, in seconds.
	maxAge = 1800
	// maxMX caps the wait a searcher may impose on us.
	maxMX = 3
	// maxPacket is generous for an M-SEARCH; anything longer is junk.
	maxPacket = 2048
	// perSourceInterval rate-limits answers to one searcher: hubs repeat a
	// search two or three times in a burst.
	perSourceInterval = time.Second
	// DescriptionPath is where LOCATION points.
	DescriptionPath = "/ssdp.json"
)

// Info is what the responder advertises.
type Info struct {
	// ID is the stable instance id (config.instance_id); it becomes the USN
	// and the driver's device network id.
	ID string
	// Name is the label the driver gives a new device.
	Name string
	// Version is the service version for the SERVER header.
	Version string
	// Port is the plain HTTP port LOCATION points at.
	Port int
}

// MSearch is the part of a search this responder acts on.
type MSearch struct {
	// ST is the requested target, lower-cased: ST or ssdp:all.
	ST string
	// MX is the searcher's maximum wait in seconds, clamped to [0, maxMX].
	MX int
}

// ParseMSearch reads one datagram. ok is false for anything that is not an
// M-SEARCH for a target we serve.
func ParseMSearch(pkt []byte) (MSearch, bool) {
	if len(pkt) == 0 || len(pkt) > maxPacket {
		return MSearch{}, false
	}
	lines := strings.Split(strings.ReplaceAll(string(pkt), "\r\n", "\n"), "\n")
	if !isRequestLine(lines[0]) {
		return MSearch{}, false
	}
	var out MSearch
	var st string
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "st":
			st = strings.ToLower(value)
		case "man":
			if strings.ToLower(strings.Trim(value, `"`)) != "ssdp:discover" {
				return MSearch{}, false
			}
		case "mx":
			if n, err := strconv.Atoi(value); err == nil {
				out.MX = clampMX(n)
			}
		}
	}
	if st != ST && st != searchAll {
		return MSearch{}, false
	}
	out.ST = st
	return out, true
}

func isRequestLine(line string) bool {
	f := strings.Fields(strings.TrimSpace(line))
	return len(f) == 3 && strings.EqualFold(f[0], "M-SEARCH") && f[1] == "*" && strings.HasPrefix(strings.ToUpper(f[2]), "HTTP/1.")
}

func clampMX(mx int) int {
	if mx < 0 {
		return 0
	}
	if mx > maxMX {
		return maxMX
	}
	return mx
}

// ResponseText builds the unicast 200 OK. localIP is the address of the
// interface the search arrived on, so LOCATION is reachable for the searcher.
func ResponseText(info Info, localIP net.IP, now time.Time) string {
	location := "http://" + net.JoinHostPort(localIP.String(), strconv.Itoa(info.Port)) + DescriptionPath
	headers := []string{
		"HTTP/1.1 200 OK",
		"CACHE-CONTROL: max-age=" + strconv.Itoa(maxAge),
		"DATE: " + now.UTC().Format(http.TimeFormat),
		"EXT:",
		"LOCATION: " + location,
		"SERVER: Linux UPnP/1.0 smartthings-ps5-control/" + info.Version,
		"ST: " + ST,
		"USN: uuid:" + info.ID + "::" + ST,
		// Our own headers, so the driver needs no second request.
		"PS5CTL-ID: " + info.ID,
		"PS5CTL-NAME: " + sanitizeHeader(info.Name),
		"PS5CTL-PORT: " + strconv.Itoa(info.Port),
	}
	return strings.Join(headers, "\r\n") + "\r\n\r\n"
}

func sanitizeHeader(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, s)
}

// Responder listens for searches on every multicast-capable interface.
type Responder struct {
	Info func() Info
	Log  func(format string, args ...any)

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	limitMu sync.Mutex
	last    map[string]time.Time
}

func (r *Responder) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

// allow reports whether ip may be answered now (once per perSourceInterval).
func (r *Responder) allow(ip string, now time.Time) bool {
	r.limitMu.Lock()
	defer r.limitMu.Unlock()
	if r.last == nil {
		r.last = map[string]time.Time{}
	}
	if t, ok := r.last[ip]; ok && now.Sub(t) < perSourceInterval {
		return false
	}
	r.last[ip] = now
	return true
}

type socket struct {
	name    string
	localIP net.IP
	recv    *net.UDPConn
	send    *net.UDPConn
}

func (s *socket) close() {
	if s.send != nil && s.send != s.recv {
		s.send.Close()
	}
	s.recv.Close()
}

// openSockets joins the SSDP group on every usable IPv4 interface. An
// interface that refuses the join is logged and skipped; only "none worked"
// is an error.
func (r *Responder) openSockets() ([]*socket, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	group := &net.UDPAddr{IP: net.ParseIP(Group), Port: Port}
	var out []*socket
	for i := range ifaces {
		ifi := ifaces[i]
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		ip := firstIPv4(&ifi)
		if ip == nil {
			continue
		}
		recv, err := net.ListenMulticastUDP("udp4", &ifi, group)
		if err != nil {
			r.logf("%s did not join %s: %v", ifi.Name, Group, err)
			continue
		}
		// The receiving socket is bound to the group address, which is not
		// a valid source for a unicast reply; answers go out on a second
		// socket bound to the interface's own address.
		send, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: 0})
		if err != nil {
			r.logf("%s has no unicast sender (%v); replying from the group socket", ifi.Name, err)
			send = recv
		}
		out = append(out, &socket{name: ifi.Name, localIP: ip, recv: recv, send: send})
	}
	if len(out) == 0 {
		return nil, errors.New("ssdp: no multicast-capable IPv4 interface")
	}
	return out, nil
}

func firstIPv4(ifi *net.Interface) net.IP {
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip4 := ip.To4(); ip4 != nil && !ip4.IsUnspecified() && !ip4.IsLoopback() {
			return ip4
		}
	}
	return nil
}

// Start opens the sockets and answers searches until Stop. Starting twice
// is a no-op.
func (r *Responder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return nil
	}
	socks, err := r.openSockets()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var wg sync.WaitGroup
	names := make([]string, 0, len(socks))
	for _, s := range socks {
		names = append(names, fmt.Sprintf("%s (%s)", s.name, s.localIP))
		wg.Add(1)
		go func(s *socket) {
			defer wg.Done()
			r.serve(ctx, s.recv, s.send, s.localIP)
		}(s)
	}
	go func() {
		<-ctx.Done()
		for _, s := range socks {
			s.close()
		}
		wg.Wait()
		close(done)
	}()
	r.cancel, r.done = cancel, done
	r.logf("responder listening on %s", strings.Join(names, ", "))
	return nil
}

// Stop ends the responder and waits briefly for it.
func (r *Responder) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel == nil {
		return
	}
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		r.logf("responder did not stop in time")
	}
	r.cancel, r.done = nil, nil
}

// serve reads searches from recv until it is closed and answers each on
// send, after the random delay the searcher's MX allows.
func (r *Responder) serve(ctx context.Context, recv, send *net.UDPConn, localIP net.IP) {
	buf := make([]byte, maxPacket)
	for {
		n, src, err := recv.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() == nil {
				r.logf("read failed: %v", err)
			}
			return
		}
		req, ok := ParseMSearch(buf[:n])
		if !ok || src == nil {
			continue
		}
		if !r.allow(src.IP.String(), time.Now()) {
			continue
		}
		delay := time.Duration(0)
		if req.MX > 0 {
			delay = time.Duration(rand.Float64() * float64(time.Duration(req.MX)*time.Second)) //nolint:gosec // not security-relevant
		}
		go func(src *net.UDPAddr, delay time.Duration) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			text := ResponseText(r.Info(), localIP, time.Now())
			if _, err := send.WriteToUDP([]byte(text), src); err != nil {
				if ctx.Err() == nil {
					r.logf("answer to %s failed: %v", src, err)
				}
				return
			}
			r.logf("answered search from %s", src)
		}(src, delay)
	}
}
