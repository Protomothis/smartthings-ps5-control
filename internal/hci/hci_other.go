//go:build !linux

package hci

import "time"

// Open is unavailable off Linux; the packet helpers still compile so the rest
// of the program (and its tests) build on a development machine.
func Open(dev int, timeout time.Duration) (Transport, error) { return nil, ErrUnsupported }

// DevUp is unavailable off Linux.
func DevUp(dev int) error { return ErrUnsupported }

// DevReset is unavailable off Linux.
func DevReset(dev int) error { return ErrUnsupported }
