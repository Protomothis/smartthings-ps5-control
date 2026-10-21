package remoteplay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Ports the console listens on.
const (
	// RESTPort serves registration and session set-up over HTTP, and the
	// pre-registration UDP search.
	RESTPort = 9295
	// WakePort takes the discovery protocol, including WAKEUP.
	WakePort = 9302
)

const (
	userAgent = "remoteplay Windows"
	rpVersion = "1.0" // PS5
)

// Client talks to one console.
type Client struct {
	// Host is the console's IP address or name.
	Host string
	// RESTPort and WakePort default to the console's fixed ports; tests
	// point them at fakes.
	RESTPort int
	WakePort int
	// SearchLocalPort is the local UDP port the registration search binds;
	// the console answers only a search sent from 9295. 0 means ephemeral.
	SearchLocalPort int
	// Timeout bounds each network step (default 10s).
	Timeout time.Duration
	// Log receives progress lines; nil discards them.
	Log func(format string, args ...any)

	// nonce overrides the random client nonce (tests).
	nonce func() ([]byte, error)
}

// New returns a Client for host with the console's real ports.
func New(host string) *Client {
	return &Client{Host: host, RESTPort: RESTPort, WakePort: WakePort, SearchLocalPort: RESTPort, Timeout: 10 * time.Second}
}

func (c *Client) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

func (c *Client) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 10 * time.Second
	}
	return c.Timeout
}

func (c *Client) restAddr() string {
	port := c.RESTPort
	if port == 0 {
		port = RESTPort
	}
	return net.JoinHostPort(c.Host, strconv.Itoa(port))
}

func (c *Client) wakeAddr() string {
	port := c.WakePort
	if port == 0 {
		port = WakePort
	}
	return net.JoinHostPort(c.Host, strconv.Itoa(port))
}

// ReasonError is a non-2xx answer from the console, with the reason code the
// console puts in RP-Application-Reason.
type ReasonError struct {
	Status int
	Code   string
}

var reasonText = map[string]string{
	"80108b09": "registration failed, probably an invalid PIN",
	"80108b02": "invalid PSN account ID",
	"80108b10": "Remote Play is already in use on the console",
	"80108b15": "Remote Play on the console crashed",
	"80108b11": "RP-Version mismatch",
	"80108bff": "unknown error",
}

func (e *ReasonError) Error() string {
	msg := fmt.Sprintf("remoteplay: console answered HTTP %d", e.Status)
	if e.Code != "" {
		msg += " (" + e.Code
		if t, ok := reasonText[strings.ToLower(e.Code)]; ok {
			msg += ": " + t
		}
		msg += ")"
	}
	return msg
}

// ErrPasscode is returned when the console user is protected by a passcode,
// which this client does not send.
var ErrPasscode = errors.New("remoteplay: the console user has a login passcode; remove it or use a user without one")

// rawRequest sends one HTTP/1.1 request on conn and reads the response head.
// The returned reader continues after the headers, which is how the session
// control request turns into the packet stream. The body is never read.
func rawRequest(conn net.Conn, method, host, path string, headers [][2]string, body []byte) (*http.Response, *bufio.Reader, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: %s\r\n", method, path, host, userAgent)
	for _, h := range headers {
		fmt.Fprintf(&b, "%s: %s\r\n", h[0], h[1])
	}
	// The console wants an explicit zero length even on GET.
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(conn, b.String()); err != nil {
		return nil, nil, err
	}
	if len(body) > 0 {
		if _, err := conn.Write(body); err != nil {
			return nil, nil, err
		}
	}
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, &http.Request{Method: method})
	if err != nil {
		return nil, nil, fmt.Errorf("remoteplay: read response: %w", err)
	}
	return resp, r, nil
}

func reasonError(resp *http.Response) error {
	return &ReasonError{Status: resp.StatusCode, Code: resp.Header.Get("RP-Application-Reason")}
}

// readBody reads a response body that has a Content-Length.
func readBody(resp *http.Response, r *bufio.Reader) ([]byte, error) {
	if resp.ContentLength < 0 {
		return nil, errors.New("remoteplay: response without Content-Length")
	}
	buf := make([]byte, resp.ContentLength)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("remoteplay: read body: %w", err)
	}
	return buf, nil
}

func (c *Client) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: c.timeout()}
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("remoteplay: connect %s: %w", addr, err)
	}
	return conn, nil
}

// parseRecord decodes "Key: value\r\n" lines.
func parseRecord(body []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}
