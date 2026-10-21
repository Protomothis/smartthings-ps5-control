package remoteplay

import (
	"context"
	"fmt"
)

// discoveryVersionPS5 is the device-discovery-protocol-version of a PS5.
const discoveryVersionPS5 = "00030010"

// WakeMessage is the WAKEUP datagram for a registered client, in the field
// order playactor sends.
func WakeMessage(credential string) []byte {
	return []byte("WAKEUP * HTTP/1.1\n" +
		"app-type:r\n" +
		"auth-type:R\n" +
		"client-type:vr\n" +
		"model:w\n" +
		"user-credential:" + credential + "\n" +
		"device-discovery-protocol-version:" + discoveryVersionPS5 + "\n")
}

// Wake sends one WAKEUP to the console. It does not wait for the console to
// come up; poll its status for that.
func (c *Client) Wake(ctx context.Context, reg Registration) error {
	cred, err := reg.Credential()
	if err != nil {
		return err
	}
	conn, err := c.dial(ctx, "udp", c.wakeAddr())
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := c.watch(ctx, conn)
	defer stop()

	c.logf("remoteplay: sending WAKEUP to %s", c.wakeAddr())
	if _, err := conn.Write(WakeMessage(cred)); err != nil {
		return ctxErr(ctx, fmt.Errorf("remoteplay: send WAKEUP: %w", err))
	}
	return nil
}
