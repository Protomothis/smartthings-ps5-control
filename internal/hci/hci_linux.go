//go:build linux

package hci

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// Constants from <bluetooth/hci.h> that golang.org/x/sys/unix does not export.
const (
	hciChannelRaw = 0 // HCI_CHANNEL_RAW
	solHCI        = 0 // SOL_HCI
	hciFilter     = 2 // HCI_FILTER socket option

	// ioctl requests: _IOW('H', n, int)
	ioctlHCIDevUp    = 0x400448C9 // HCIDEVUP    = _IOW('H', 201, int)
	ioctlHCIDevDown  = 0x400448CA // HCIDEVDOWN  = _IOW('H', 202, int)
	ioctlHCIDevReset = 0x400448CB // HCIDEVRESET = _IOW('H', 203, int)
)

type conn struct {
	fd int
}

func rawSocket() (int, error) {
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.BTPROTO_HCI)
	if err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			return -1, fmt.Errorf("%w: %v", ErrPermission, err)
		}
		return -1, fmt.Errorf("hci: open raw socket: %w", err)
	}
	return fd, nil
}

// Open binds a raw HCI socket to adapter dev (0 for hci0) and sets a receive
// filter that delivers every HCI event. timeout bounds each Recv call.
func Open(dev int, timeout time.Duration) (Transport, error) {
	fd, err := rawSocket()
	if err != nil {
		return nil, err
	}
	c := &conn{fd: fd}
	if err := unix.Bind(fd, &unix.SockaddrHCI{Dev: uint16(dev), Channel: hciChannelRaw}); err != nil {
		c.Close()
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			return nil, fmt.Errorf("%w: bind hci%d: %v", ErrPermission, dev, err)
		}
		return nil, fmt.Errorf("hci: bind hci%d: %w", dev, err)
	}
	// Without a filter the kernel delivers nothing.
	if err := unix.SetsockoptString(fd, solHCI, hciFilter, string(EventFilter())); err != nil {
		c.Close()
		return nil, fmt.Errorf("hci: set filter on hci%d: %w", dev, err)
	}
	if timeout > 0 {
		tv := unix.NsecToTimeval(timeout.Nanoseconds())
		if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
			c.Close()
			return nil, fmt.Errorf("hci: set receive timeout: %w", err)
		}
	}
	return c, nil
}

func (c *conn) Send(pkt []byte) error {
	for {
		n, err := unix.Write(c.fd, pkt)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n != len(pkt) {
			return fmt.Errorf("hci: short write (%d of %d bytes)", n, len(pkt))
		}
		return nil
	}
}

func (c *conn) Recv() ([]byte, error) {
	buf := make([]byte, 260) // 3-byte header + max 255-byte event payload, with slack
	for {
		n, err := unix.Read(c.fd, buf)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrTimeout
		}
		if err != nil {
			return nil, err
		}
		return buf[:n], nil
	}
}

func (c *conn) Close() error {
	if c.fd < 0 {
		return nil
	}
	err := unix.Close(c.fd)
	c.fd = -1
	return err
}

// DevUp brings adapter dev up (what "hciconfig hciN up" does). An adapter that
// is already up is not an error.
func DevUp(dev int) error {
	return devIoctl(dev, ioctlHCIDevUp, "up")
}

// DevReset performs a driver-level reset of the adapter (HCIDEVRESET).
func DevReset(dev int) error {
	return devIoctl(dev, ioctlHCIDevReset, "reset")
}

func devIoctl(dev int, req uint, what string) error {
	fd, err := rawSocket()
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	err = unix.IoctlSetInt(fd, req, dev)
	if errors.Is(err, unix.EALREADY) {
		return nil
	}
	if err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			return fmt.Errorf("%w: hci%d %s", ErrPermission, dev, what)
		}
		return fmt.Errorf("hci: hci%d %s: %w", dev, what, err)
	}
	return nil
}
