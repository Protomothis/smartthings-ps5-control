package remoteplay

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"
)

// Control packet types. Outgoing and incoming types share the 16-bit field
// but not the numbering.
const (
	cmdLogin     uint16 = 0x05
	cmdStandby   uint16 = 0x50
	cmdHeartbeat uint16 = 0x1fe

	respPasscode  uint16 = 0x04
	respLogin     uint16 = 0x05
	respHeartbeat uint16 = 0xfe
)

const (
	packetHeaderSize = 8
	// maxPacketPayload guards the length field; control packets are tiny.
	maxPacketPayload = 1 << 16
	osType           = "Win10.0.0"
)

// standbyLinger is how long Standby waits for the console to hang up after
// the standby command. The console normally closes the connection as it goes
// down; if it does not, the command was still sent.
var standbyLinger = 10 * time.Second

// didPrefix starts the RP-Did ("device ID") header; 16 zero bytes and a
// 6-byte zero suffix follow.
var didPrefix = []byte{0x00, 0x18, 0x00, 0x00, 0x00, 0x07, 0x00, 0x40, 0x00, 0x80}

// Standby opens a control session with reg and puts the console into rest
// mode. It returns once the console hangs up, or after a grace period.
func (c *Client) Standby(ctx context.Context, reg Registration) error {
	if !reg.Valid() {
		return errors.New("remoteplay: registration has no valid RP-Key")
	}
	rpKey, _ := hex.DecodeString(reg.RPKey)

	nonce, err := c.sessionNonce(ctx, reg)
	if err != nil {
		return err
	}
	sc, err := newSessionCrypto(rpKey, nonce)
	if err != nil {
		return err
	}
	headers, err := controlHeaders(sc, reg)
	if err != nil {
		return err
	}

	conn, err := dialREST(ctx, c)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := c.watch(ctx, conn)
	defer stop()

	c.logf("remoteplay: opening control session with %s", c.restAddr())
	resp, r, err := rawRequest(conn, "GET", c.restAddr(), "/sie/ps5/rp/sess/ctrl", headers, nil) //nolint:bodyclose // r becomes the packet stream
	if err != nil {
		return ctxErr(ctx, err)
	}
	// The response has no body; r carries on into the packet stream.
	if resp.StatusCode >= 300 {
		return reasonError(resp)
	}
	st, err := base64.StdEncoding.DecodeString(resp.Header.Get("RP-Server-Type"))
	if err != nil || len(st) == 0 {
		return errors.New("remoteplay: control response without RP-Server-Type")
	}
	serverType, err := sc.decrypt(st)
	if err != nil {
		return err
	}
	c.logf("remoteplay: control session open (server type %x)", serverType)

	s := &session{conn: conn, r: r, sc: sc}
	if err := s.login(); err != nil {
		return ctxErr(ctx, err)
	}
	c.logf("remoteplay: logged in, sending standby")
	if err := s.send(cmdStandby, nil); err != nil {
		return ctxErr(ctx, fmt.Errorf("remoteplay: send standby: %w", err))
	}
	if ctx.Err() == nil {
		_ = conn.SetDeadline(time.Now().Add(standbyLinger))
	}
	err = s.drain()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	c.logf("remoteplay: standby sent")
	return nil
}

// sessionNonce asks the console for the session nonce over its own
// connection.
func (c *Client) sessionNonce(ctx context.Context, reg Registration) ([]byte, error) {
	conn, err := dialREST(ctx, c)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := c.watch(ctx, conn)
	defer stop()

	resp, _, err := rawRequest(conn, "GET", c.restAddr(), "/sie/ps5/rp/sess/init", //nolint:bodyclose // only headers matter; the conn is closed
		[][2]string{{"RP-RegistKey", reg.RegistKey}, {"RP-Version", rpVersion}}, nil)
	if err != nil {
		return nil, ctxErr(ctx, err)
	}
	// The connection is closed with whatever body there is.
	if resp.StatusCode >= 300 {
		return nil, reasonError(resp)
	}
	nonce, err := base64.StdEncoding.DecodeString(resp.Header.Get("RP-Nonce"))
	if err != nil || len(nonce) != NonceLength {
		return nil, fmt.Errorf("remoteplay: session init answered without a %d-byte RP-Nonce", NonceLength)
	}
	return nonce, nil
}

// controlHeaders builds the session control request headers. The encrypted
// values use the session counters 0..4 in this order.
func controlHeaders(sc *sessionCrypto, reg Registration) ([][2]string, error) {
	registKey, err := hex.DecodeString(reg.RegistKey)
	if err != nil || len(registKey) > NonceLength {
		return nil, errors.New("remoteplay: registration key is not up to 16 bytes of hex")
	}
	auth := make([]byte, NonceLength)
	copy(auth, registKey)

	did := make([]byte, 0, len(didPrefix)+16+6)
	did = append(did, didPrefix...)
	did = append(did, make([]byte, 16+6)...)

	streamingType := make([]byte, 4)
	binary.LittleEndian.PutUint32(streamingType, 1)

	var encErr error
	enc := func(b []byte) string {
		out, err := sc.encrypt(b)
		if err != nil && encErr == nil {
			encErr = err
		}
		return base64.StdEncoding.EncodeToString(out)
	}
	headers := [][2]string{
		{"RP-Auth", enc(auth)},
		{"RP-Version", rpVersion},
		{"RP-Did", enc(did)},
		{"RP-ControllerType", "3"},
		{"RP-ClientType", "11"},
		{"RP-OSType", enc([]byte(osType))},
		{"RP-ConPath", "1"},
		{"RP-StartBitrate", enc(make([]byte, 4))},
		{"RP-StreamingType", enc(streamingType)},
	}
	if encErr != nil {
		return nil, encErr
	}
	return headers, nil
}

// encodePacket frames one control packet: payload length (u32 BE), type
// (u16 BE), u16 zero, then the payload encrypted with the next counter.
// Header-only packets are sent in the clear and use no counter.
func encodePacket(sc *sessionCrypto, typ uint16, payload []byte) ([]byte, error) {
	out := make([]byte, packetHeaderSize, packetHeaderSize+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(payload)))
	binary.BigEndian.PutUint16(out[4:6], typ)
	if len(payload) == 0 {
		return out, nil
	}
	enc, err := sc.encrypt(payload)
	if err != nil {
		return nil, err
	}
	return append(out, enc...), nil
}

// readPacket reads one control packet and decrypts its payload.
func readPacket(r io.Reader, sc *sessionCrypto) (uint16, []byte, error) {
	var hdr [packetHeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[0:4])
	typ := binary.BigEndian.Uint16(hdr[4:6])
	if n == 0 {
		return typ, nil, nil
	}
	if n > maxPacketPayload {
		return 0, nil, fmt.Errorf("remoteplay: control packet of %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	plain, err := sc.decrypt(buf)
	if err != nil {
		return 0, nil, err
	}
	return typ, plain, nil
}

type session struct {
	conn net.Conn
	r    *bufio.Reader
	sc   *sessionCrypto
}

func (s *session) send(typ uint16, payload []byte) error {
	pkt, err := encodePacket(s.sc, typ, payload)
	if err != nil {
		return err
	}
	_, err = s.conn.Write(pkt)
	return err
}

// next reads the next packet that is not a heartbeat, answering heartbeats
// on the way.
func (s *session) next() (uint16, []byte, error) {
	for {
		typ, payload, err := readPacket(s.r, s.sc)
		if err != nil {
			return 0, nil, err
		}
		if typ == respHeartbeat {
			if err := s.send(cmdHeartbeat, nil); err != nil {
				return 0, nil, err
			}
			continue
		}
		return typ, payload, nil
	}
}

func (s *session) login() error {
	if err := s.send(cmdLogin, nil); err != nil {
		return fmt.Errorf("remoteplay: send login: %w", err)
	}
	for {
		typ, payload, err := s.next()
		if err != nil {
			return fmt.Errorf("remoteplay: waiting for login result: %w", err)
		}
		switch typ {
		case respPasscode:
			return ErrPasscode
		case respLogin:
			if len(payload) == 0 {
				return errors.New("remoteplay: login result without a result code")
			}
			if payload[0] != 0 {
				return fmt.Errorf("remoteplay: console refused login (result %d)", payload[0])
			}
			return nil
		}
	}
}

// drain answers heartbeats until the console hangs up or the linger
// deadline passes. Both mean the standby went out.
func (s *session) drain() error {
	for {
		_, _, err := s.next()
		if err == nil {
			continue
		}
		var ne net.Error
		switch {
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF),
			errors.Is(err, syscall.ECONNRESET), errors.Is(err, net.ErrClosed):
			return nil
		case errors.As(err, &ne) && ne.Timeout():
			return nil
		}
		return fmt.Errorf("remoteplay: after standby: %w", err)
	}
}
