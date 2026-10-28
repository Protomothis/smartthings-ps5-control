// Package discovery probes a PlayStation's power state with the unauthenticated
// device-discovery protocol on UDP port 9302 (the same probe the PS Remote Play
// and PS App clients send).
//
// Responses:
//
//	HTTP/1.1 200 Ok             console is on
//	HTTP/1.1 620 Server Standby console is in rest mode ("stay connected to the
//	                            internet" must be enabled for this reply)
//	(no reply)                  console is fully off, unreachable, or rest mode
//	                            with networking disabled
package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Port is the UDP port the console listens on for discovery probes.
const Port = 9302

// Probe payload. The version string is the one PS5 firmware answers to.
const probePayload = "SRCH * HTTP/1.1\ndevice-discovery-protocol-version:00030010\n"

// State is the derived power state.
type State string

const (
	StateOn      State = "on"
	StateStandby State = "standby"
	StateOff     State = "off"     // no reply before the timeout
	StateUnknown State = "unknown" // replied with an unexpected status code
)

// Status is the result of one probe.
type Status struct {
	State  State             `json:"state"`
	Code   int               `json:"code,omitempty"`   // HTTP-style status code from the reply, 0 when no reply
	Fields map[string]string `json:"fields,omitempty"` // host-name, host-type, host-id, ... from the reply
	Raw    string            `json:"-"`
}

// DefaultTimeout is how long Probe waits for a reply when the caller passes 0.
const DefaultTimeout = 2 * time.Second

// resendInterval is how often the probe is re-sent while waiting; the console
// occasionally drops the first datagram after waking its network stack.
const resendInterval = 500 * time.Millisecond

// Probe sends discovery packets to host (IP or hostname) and waits up to
// timeout for a reply. A missing reply is reported as StateOff with a nil
// error; errors are reserved for local failures (bad host, socket errors).
func Probe(ctx context.Context, host string, timeout time.Duration) (Status, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	raddr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, strconv.Itoa(Port)))
	if err != nil {
		return Status{}, fmt.Errorf("discovery: resolve %q: %w", host, err)
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return Status{}, fmt.Errorf("discovery: open socket: %w", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()

	buf := make([]byte, 2048)
	for {
		if _, err := conn.WriteToUDP([]byte(probePayload), raddr); err != nil && !unreachable(err) {
			return Status{}, fmt.Errorf("discovery: send: %w", err)
		}
		next := time.Now().Add(resendInterval)
		if next.After(deadline) {
			next = deadline
		}
		_ = conn.SetReadDeadline(next)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					break
				}
				if ctx.Err() != nil {
					return Status{}, ctx.Err()
				}
				if unreachable(err) {
					continue // the console is not there (yet): keep waiting for the deadline
				}
				return Status{}, fmt.Errorf("discovery: receive: %w", err)
			}
			if !from.IP.Equal(raddr.IP) {
				continue // stray datagram from another host
			}
			return Parse(buf[:n])
		}
		if ctx.Err() != nil {
			return Status{}, ctx.Err()
		}
		if !time.Now().Before(deadline) {
			return Status{State: StateOff}, nil
		}
	}
}

// unreachable reports whether a socket error says the console is not there
// rather than that something is wrong locally: an ICMP port/host unreachable
// while the console boots (its listener is not up yet) or a network that
// has no route. Those mean "off" for the caller, like a missing reply.
func unreachable(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH)
}

// Parse decodes a discovery reply.
func Parse(resp []byte) (Status, error) {
	raw := string(resp)
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return Status{Raw: raw}, errors.New("discovery: empty reply")
	}
	head := strings.Fields(lines[0])
	if len(head) < 2 || !strings.HasPrefix(head[0], "HTTP/") {
		return Status{Raw: raw}, fmt.Errorf("discovery: unexpected reply %q", lines[0])
	}
	code, err := strconv.Atoi(head[1])
	if err != nil {
		return Status{Raw: raw}, fmt.Errorf("discovery: bad status code in %q", lines[0])
	}
	st := Status{Code: code, Raw: raw, Fields: map[string]string{}}
	for _, l := range lines[1:] {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		st.Fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	switch code {
	case 200:
		st.State = StateOn
	case 620:
		st.State = StateStandby
	default:
		st.State = StateUnknown
	}
	return st, nil
}
