package remoteplay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"strconv"
	"time"
)

// clientType is the Client-Type a registration announces (PS4 10.0+ and PS5).
const clientType = "dabfa2ec873de5839bee8d3f4c0239c4282c07c25c6077a2931afcf0adc0d34f"

const (
	searchRequest  = "SRC3"
	searchResponse = "RES3"
	// searchResend is how often the search datagram is repeated while waiting.
	searchResend = time.Second
	// searchSettle gives the console a moment after RES3 before the request.
	searchSettle = 100 * time.Millisecond
)

// Registration is what the console hands out when it pairs with this client.
// RegistKey and RPKey are secrets: whoever holds them can wake the console
// and open a control session.
type Registration struct {
	AccountID    string    `json:"account_id"`
	RegistKey    string    `json:"regist_key"`
	RPKey        string    `json:"rp_key"`
	Nickname     string    `json:"nickname,omitempty"`
	Mac          string    `json:"mac,omitempty"`
	APName       string    `json:"ap_name,omitempty"`
	APBssid      string    `json:"ap_bssid,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
}

// Valid reports whether r holds a usable RP-Key (16 bytes as hex) and a
// registration key.
func (r Registration) Valid() bool {
	key, err := hex.DecodeString(r.RPKey)
	return err == nil && len(key) == NonceLength && r.RegistKey != ""
}

// Credential is the user-credential the WAKEUP message carries. The regist
// key is hex of an ASCII string that is itself a hex number; the credential
// is that number in decimal.
func (r Registration) Credential() (string, error) {
	raw, err := hex.DecodeString(r.RegistKey)
	if err != nil || len(raw) == 0 {
		return "", errors.New("remoteplay: registration key is not hex")
	}
	n, ok := new(big.Int).SetString(string(raw), 16)
	if !ok {
		return "", errors.New("remoteplay: registration key does not hold a hex number")
	}
	return n.String(), nil
}

// Register pairs with the console using the 8-digit PIN it shows under
// Settings > System > Remote Play > Link Device, for the PSN account whose
// base64 account ID is accountID.
func (c *Client) Register(ctx context.Context, accountID, pin string) (Registration, error) {
	pinNumber, err := parsePIN(pin)
	if err != nil {
		return Registration{}, err
	}
	if id, err := base64.StdEncoding.DecodeString(accountID); err != nil || len(id) != 8 {
		return Registration{}, errors.New("remoteplay: account ID must be 8 bytes in base64")
	}

	if err := c.search(ctx); err != nil {
		return Registration{}, err
	}
	if err := sleepCtx(ctx, searchSettle); err != nil {
		return Registration{}, err
	}

	nonce, err := c.clientNonce()
	if err != nil {
		return Registration{}, err
	}
	pc, err := newPinCrypto(pinNumber, nonce)
	if err != nil {
		return Registration{}, err
	}
	body, err := registrationBody(pc, accountID)
	if err != nil {
		return Registration{}, err
	}

	conn, err := dialREST(ctx, c)
	if err != nil {
		return Registration{}, err
	}
	defer conn.Close()
	stop := c.watch(ctx, conn)
	defer stop()

	c.logf("remoteplay: sending registration to %s", c.restAddr())
	resp, r, err := rawRequest(conn, "POST", c.restAddr(), "/sie/ps5/rp/sess/rgst", //nolint:bodyclose // body read from r; the conn is closed
		[][2]string{{"RP-Version", rpVersion}}, body)
	if err != nil {
		return Registration{}, ctxErr(ctx, err)
	}
	// resp.Body is left alone: the body is read straight from r, and closing
	// resp.Body would try to drain the same bytes again.
	if resp.StatusCode >= 300 {
		return Registration{}, reasonError(resp)
	}
	enc, err := readBody(resp, r)
	if err != nil {
		return Registration{}, ctxErr(ctx, err)
	}
	plain, err := pc.decrypt(enc)
	if err != nil {
		return Registration{}, err
	}
	rec := parseRecord(plain)
	reg := Registration{
		AccountID:    accountID,
		RegistKey:    rec["PS5-RegistKey"],
		RPKey:        rec["RP-Key"],
		Nickname:     rec["PS5-Nickname"],
		Mac:          rec["PS5-Mac"],
		APName:       rec["AP-Name"],
		APBssid:      rec["AP-Bssid"],
		RegisteredAt: time.Now().UTC(),
	}
	if !reg.Valid() {
		return Registration{}, errors.New("remoteplay: console answered without a valid RP-Key and registration key")
	}
	c.logf("remoteplay: registered with %q", reg.Nickname)
	return reg, nil
}

// registrationBody is the preface followed by the encrypted record.
func registrationBody(pc *pinCrypto, accountID string) ([]byte, error) {
	record := "Client-Type: " + clientType + "\r\nNp-AccountId: " + accountID + "\r\n"
	enc, err := pc.encrypt([]byte(record))
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(pc.preface)+len(enc))
	out = append(out, pc.preface...)
	return append(out, enc...), nil
}

func parsePIN(pin string) (uint32, error) {
	if len(pin) != 8 {
		return 0, errors.New("remoteplay: PIN must be 8 digits")
	}
	for _, ch := range pin {
		if ch < '0' || ch > '9' {
			return 0, errors.New("remoteplay: PIN must be 8 digits")
		}
	}
	n, err := strconv.ParseUint(pin, 10, 32)
	if err != nil {
		return 0, errors.New("remoteplay: PIN must be 8 digits")
	}
	return uint32(n), nil
}

func (c *Client) clientNonce() ([]byte, error) {
	if c.nonce != nil {
		return c.nonce()
	}
	nonce := make([]byte, NonceLength)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return nonce, nil
}

// search tells the console a registration is coming: without a SRC3 first,
// the registration request is rejected.
func (c *Client) search(ctx context.Context) error {
	host, portStr, err := net.SplitHostPort(c.restAddr())
	if err != nil {
		return err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("remoteplay: resolve %s: %w", host, err)
	}
	target := net.UDPAddrFromAddrPort(netip.AddrPortFrom(ips[0].Unmap(), uint16(port)))

	var lc net.ListenConfig
	pc, err := lc.ListenPacket(ctx, "udp4", net.JoinHostPort("", strconv.Itoa(c.SearchLocalPort)))
	if err != nil {
		return fmt.Errorf("remoteplay: bind search port %d: %w", c.SearchLocalPort, err)
	}
	defer pc.Close()
	stop := context.AfterFunc(ctx, func() { _ = pc.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	c.logf("remoteplay: searching for %s", target)
	deadline := time.Now().Add(c.timeout())
	buf := make([]byte, 1024)
	for time.Now().Before(deadline) {
		if _, err := pc.WriteTo([]byte(searchRequest), target); err != nil {
			return ctxErr(ctx, fmt.Errorf("remoteplay: send search: %w", err))
		}
		next := time.Now().Add(searchResend)
		if next.After(deadline) {
			next = deadline
		}
		if ctx.Err() == nil {
			_ = pc.SetReadDeadline(next)
		}
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					break
				}
				return fmt.Errorf("remoteplay: read search answer: %w", err)
			}
			if bytes.HasPrefix(buf[:n], []byte(searchResponse)) {
				return nil
			}
		}
	}
	return fmt.Errorf("remoteplay: console at %s did not answer the registration search; is Remote Play on and the console awake?", target)
}

// watch bounds conn by the client timeout and breaks it off when ctx ends.
// The returned func releases the context hook.
func (c *Client) watch(ctx context.Context, conn net.Conn) func() bool {
	_ = conn.SetDeadline(time.Now().Add(c.timeout()))
	return context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
}

// ctxErr prefers the context's error when the context ended the I/O.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// dialREST opens a TCP connection to the console's REST port. It is a
// variable so tests can run the fake console over an in-memory pipe where
// a local HTTP filter rewrites loopback traffic.
var dialREST = func(ctx context.Context, c *Client) (net.Conn, error) {
	return c.dial(ctx, "tcp", c.restAddr())
}
