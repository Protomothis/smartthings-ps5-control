// Package wake powers on a PlayStation over Bluetooth by paging it from the
// BD_ADDR of a controller it has paired with.
//
// The console checks the source address of an incoming page against its list
// of paired controllers and powers on when it matches, before any link-key
// authentication happens. So the sequence is:
//
//  1. detect the adapter chipset and pick a BD_ADDR spoof strategy
//  2. remember the adapter's real BD_ADDR
//  3. write the controller's BD_ADDR into the adapter
//  4. HCI_Create_Connection to the console and wait for the outcome
//  5. restore the real BD_ADDR
package wake

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/btaddr"
	"github.com/protomothis/smartthings-ps5-control/internal/hci"
	"github.com/protomothis/smartthings-ps5-control/internal/spoof"
)

// Options configures one wake run.
type Options struct {
	// Adapter is the HCI device name, e.g. "hci0".
	Adapter string
	// Controller is the BD_ADDR of a DualSense paired with the console.
	Controller btaddr.Addr
	// Console is the console's Bluetooth BD_ADDR (not its Wi-Fi MAC).
	Console btaddr.Addr
	// Timeout bounds the wait for a Connection Complete per attempt (default 6s;
	// the controller's own page timeout is ~5.12s).
	Timeout time.Duration
	// Attempts is the total number of page attempts (default 3).
	Attempts int
	// RetryDelay is the pause between attempts (default 2s).
	RetryDelay time.Duration
	// Log receives progress messages; nil discards them.
	Log func(format string, args ...any)

	// open overrides the HCI opener (tests). nil uses the real adapter.
	open hci.Opener
	// devUp overrides the adapter bring-up (tests).
	devUp func(dev int) error
}

// Outcome classifies a wake attempt.
type Outcome string

const (
	// OutcomeSuccess: the console answered the page. 0x00 means the ACL link
	// came up; 0x13/0x16 mean the console (or we) dropped it right after,
	// which is the normal result once it is awake.
	OutcomeSuccess Outcome = "success"
	// OutcomeTimeout: nothing answered the page (console off and unreachable,
	// out of range, or the spoofed address is not a paired controller).
	OutcomeTimeout Outcome = "timeout"
	// OutcomeError: the controller rejected the command or returned another
	// error status.
	OutcomeError Outcome = "error"
)

// Result is the outcome of Wake.
type Result struct {
	Adapter      string           `json:"adapter"`
	Version      hci.LocalVersion `json:"adapter_version"`
	Vendor       string           `json:"vendor"`
	OriginalAddr btaddr.Addr      `json:"original_addr"`
	Attempts     int              `json:"attempts"`
	Status       byte             `json:"status"`
	StatusText   string           `json:"status_text"`
	Outcome      Outcome          `json:"outcome"`
	// LocalTimeout is set when no HCI event arrived within Timeout (the
	// controller itself never reported a result).
	LocalTimeout bool `json:"local_timeout,omitempty"`
	Restored     bool `json:"restored"`
}

var adapterRe = regexp.MustCompile(`^hci(\d+)$`)

// ParseAdapter turns "hci0" into 0.
func ParseAdapter(name string) (int, error) {
	m := adapterRe.FindStringSubmatch(name)
	if m == nil {
		return 0, fmt.Errorf("wake: invalid adapter %q (expected hciN)", name)
	}
	return strconv.Atoi(m[1])
}

func classify(status byte) Outcome {
	switch status {
	case hci.StatusSuccess, hci.StatusRemoteUserTerminated, hci.StatusLocalHostTerminated:
		return OutcomeSuccess
	case hci.StatusPageTimeout, hci.StatusConnAcceptTimeout:
		return OutcomeTimeout
	default:
		return OutcomeError
	}
}

// Wake runs the full spoof → page → restore sequence with retries.
// The adapter's original BD_ADDR is restored before returning even on error,
// as long as a spoof was applied; Result.Restored reports whether that worked.
func Wake(ctx context.Context, o Options) (res Result, err error) {
	log := o.Log
	if log == nil {
		log = func(string, ...any) {}
	}
	if o.Timeout <= 0 {
		o.Timeout = 6 * time.Second
	}
	if o.Attempts <= 0 {
		o.Attempts = 3
	}
	if o.RetryDelay <= 0 {
		o.RetryDelay = 2 * time.Second
	}
	if o.Controller.IsZero() || o.Console.IsZero() {
		return Result{}, errors.New("wake: controller and console addresses are required")
	}
	dev, err := ParseAdapter(o.Adapter)
	if err != nil {
		return Result{}, err
	}
	open := o.open
	if open == nil {
		open = hci.OpenerFor(dev)
	}
	devUp := o.devUp
	if devUp == nil {
		devUp = hci.DevUp
	}
	// Nothing to restore until the address is actually rewritten: a failure
	// before that (unsupported chipset, permissions) must not read as a
	// dongle left with the wrong address.
	res = Result{Adapter: o.Adapter, Restored: true}

	if err := devUp(dev); err != nil {
		log("%s: bring-up: %v (continuing)", o.Adapter, err)
	}

	ver, strategy, err := spoof.Detect(open)
	res.Version = ver
	if err != nil {
		return res, err
	}
	res.Vendor = strategy.Vendor
	log("%s: %s (manufacturer 0x%04X, rating %d/4)", o.Adapter, strategy.Vendor, ver.Manufacturer, strategy.Rating)

	orig, err := readAddr(open)
	if err != nil {
		return res, fmt.Errorf("wake: read original BD_ADDR: %w", err)
	}
	res.OriginalAddr = orig
	log("%s: original BD_ADDR %s", o.Adapter, orig)

	spoofed := false
	defer func() {
		if !spoofed {
			res.Restored = true
			return
		}
		if err := strategy.Apply(open, orig, log); err != nil {
			log("%s: RESTORE FAILED, adapter still has %s: %v", o.Adapter, o.Controller, err)
			return
		}
		res.Restored = true
	}()

	var lastErr error
	for attempt := 1; attempt <= o.Attempts; attempt++ {
		res.Attempts = attempt
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(o.RetryDelay):
			}
		}

		if err := strategy.Apply(open, o.Controller, log); err != nil {
			lastErr = err
			log("attempt %d: spoof failed: %v", attempt, err)
			continue
		}
		spoofed = true

		status, localTimeout, err := page(open, o.Console, o.Timeout, log)
		if err != nil {
			lastErr = err
			log("attempt %d: page failed: %v", attempt, err)
			continue
		}
		res.Status, res.LocalTimeout = status, localTimeout
		if localTimeout {
			res.StatusText = "no Connection Complete event within " + o.Timeout.String()
			res.Outcome = OutcomeTimeout
		} else {
			res.StatusText = hci.StatusText(status)
			res.Outcome = classify(status)
		}
		log("attempt %d: %s (0x%02X %s)", attempt, res.Outcome, status, res.StatusText)
		lastErr = nil
		if res.Outcome == OutcomeSuccess {
			return res, nil
		}
	}
	if lastErr != nil {
		return res, lastErr
	}
	return res, nil
}

func readAddr(open hci.Opener) (btaddr.Addr, error) {
	// After an HCI_Reset the controller may need a moment before it answers
	// again, so allow one retry.
	var last error
	for i := 0; i < 2; i++ {
		if i > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		t, err := open(3 * time.Second)
		if err != nil {
			return btaddr.Addr{}, err
		}
		a, err := hci.ReadBDAddr(t)
		t.Close()
		if err == nil {
			return a, nil
		}
		last = err
	}
	return btaddr.Addr{}, last
}

// page sends HCI_Create_Connection and waits for the outcome.
func page(open hci.Opener, console btaddr.Addr, timeout time.Duration, log func(string, ...any)) (status byte, localTimeout bool, err error) {
	t, err := open(timeout)
	if err != nil {
		return 0, false, err
	}
	defer t.Close()

	log("paging %s", console)
	if err := t.Send(hci.CreateConnection(console)); err != nil {
		return 0, false, fmt.Errorf("wake: send Create_Connection: %w", err)
	}
	for {
		raw, err := t.Recv()
		if errors.Is(err, hci.ErrTimeout) {
			return 0, true, nil
		}
		if err != nil {
			return 0, false, fmt.Errorf("wake: receive: %w", err)
		}
		ev, ok := hci.ParseEvent(raw)
		if !ok {
			continue
		}
		if st, ok := ev.CommandStatus(hci.OpCreateConnection); ok {
			if st != 0 {
				// Rejected outright; no page goes on air.
				return st, false, nil
			}
			log("page accepted, waiting for Connection Complete")
			continue
		}
		if cc, ok := ev.ConnectionComplete(); ok {
			if cc.Status == hci.StatusSuccess {
				// The link is up; drop it ourselves. The console will not
				// authenticate us anyway, and the restore reset clears it too.
				_ = t.Send(hci.Disconnect(cc.Handle, hci.StatusRemoteUserTerminated))
			}
			return cc.Status, false, nil
		}
	}
}

// AdapterInfo describes the adapter without changing anything.
type AdapterInfo struct {
	Adapter      string           `json:"adapter"`
	Version      hci.LocalVersion `json:"version"`
	Manufacturer string           `json:"manufacturer"`
	Addr         btaddr.Addr      `json:"addr"`
	Supported    bool             `json:"supported"`
	Rating       int              `json:"rating"`
	Opcode       uint16           `json:"opcode,omitempty"`
	Notes        string           `json:"notes"`
}

// Inspect reports the adapter chipset and whether wake can work on it.
func Inspect(adapter string) (AdapterInfo, error) {
	dev, err := ParseAdapter(adapter)
	if err != nil {
		return AdapterInfo{}, err
	}
	if err := hci.DevUp(dev); err != nil && !errors.Is(err, hci.ErrUnsupported) {
		return AdapterInfo{}, err
	}
	open := hci.OpenerFor(dev)
	info := AdapterInfo{Adapter: adapter}
	ver, strategy, err := spoof.Detect(open)
	info.Version = ver
	info.Manufacturer = spoof.ManufacturerName(ver.Manufacturer)
	if err != nil && !errors.Is(err, spoof.ErrUnsupportedAdapter) {
		return info, err
	}
	if err == nil {
		info.Supported = true
		info.Rating = strategy.Rating
		info.Opcode = strategy.Opcode()
		info.Notes = strategy.Notes
	} else {
		info.Notes = "no BD_ADDR spoof strategy for this chipset"
	}
	addr, err := readAddr(open)
	if err != nil {
		return info, err
	}
	info.Addr = addr
	return info, nil
}
