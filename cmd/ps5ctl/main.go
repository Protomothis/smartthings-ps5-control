// Command ps5ctl is the CLI used to validate each building block of the PS5
// power control service before the HTTP server exists:
//
//	ps5ctl status  --ip 192.168.1.20
//	ps5ctl adapter --hci hci0
//	ps5ctl wake    --hci hci0 --controller AA:BB:CC:DD:EE:FF --ps5 11:22:33:44:55:66
//
// adapter and wake need Linux with CAP_NET_RAW + CAP_NET_ADMIN (or root).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/btaddr"
	"github.com/protomothis/smartthings-ps5-control/internal/discovery"
	"github.com/protomothis/smartthings-ps5-control/internal/wake"
)

var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `ps5ctl %s - PS5 power control building blocks

Usage:
  ps5ctl status  --ip <ps5-ip> [--timeout 2s] [--json]
  ps5ctl adapter --hci hci0 [--json]
  ps5ctl wake    --hci hci0 --controller <bdaddr> --ps5 <bdaddr>
                 [--timeout 6s] [--attempts 3] [--retry-delay 2s] [--json]
  ps5ctl serve   [--data /data] [--http :8580] [--https :8543] [--auth-user U --auth-pass P]

Exit codes:
  0  on / success        1  usage or local error
  2  standby             3  off / timeout / unsupported adapter
`, version)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var code int
	switch os.Args[1] {
	case "status":
		code = cmdStatus(ctx, os.Args[2:])
	case "adapter":
		code = cmdAdapter(os.Args[2:])
	case "wake":
		code = cmdWake(ctx, os.Args[2:])
	case "serve":
		code = cmdServe(ctx, os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		code = 1
	}
	os.Exit(code)
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}

func cmdStatus(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	ip := fs.String("ip", "", "PS5 IP address or hostname (required)")
	timeout := fs.Duration("timeout", discovery.DefaultTimeout, "how long to wait for a reply")
	asJSON := fs.Bool("json", false, "print the full result as JSON")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	if *ip == "" {
		fs.Usage()
		return 1
	}
	st, err := discovery.Probe(ctx, *ip, *timeout)
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		printJSON(st)
	} else {
		fmt.Println(st.State)
		if name := st.Fields["host-name"]; name != "" {
			fmt.Fprintf(os.Stderr, "%s (%s) replied %d\n", name, st.Fields["host-type"], st.Code)
		}
	}
	switch st.State {
	case discovery.StateOn:
		return 0
	case discovery.StateStandby:
		return 2
	default:
		return 3
	}
}

func cmdAdapter(args []string) int {
	fs := flag.NewFlagSet("adapter", flag.ExitOnError)
	dev := fs.String("hci", "hci0", "HCI adapter name")
	asJSON := fs.Bool("json", false, "print the full result as JSON")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	info, err := wake.Inspect(*dev)
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		printJSON(info)
	} else {
		fmt.Printf("%s  BD_ADDR %s\n", info.Adapter, info.Addr)
		fmt.Printf("manufacturer: %s (0x%04X)  hci v%d rev 0x%04X  lmp v%d sub 0x%04X\n",
			info.Manufacturer, info.Version.Manufacturer, info.Version.HCIVersion, info.Version.HCIRevision,
			info.Version.LMPVersion, info.Version.LMPSubversion)
		if info.Supported {
			fmt.Printf("spoof: supported, opcode 0x%04X, rating %d/4\n", info.Opcode, info.Rating)
		} else {
			fmt.Println("spoof: NOT supported")
		}
		fmt.Println("notes:", info.Notes)
	}
	if !info.Supported {
		return 3
	}
	return 0
}

func cmdWake(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("wake", flag.ExitOnError)
	dev := fs.String("hci", "hci0", "HCI adapter name")
	ctrlS := fs.String("controller", "", "BD_ADDR of a DualSense paired with the PS5 (required)")
	ps5S := fs.String("ps5", "", "PS5 Bluetooth BD_ADDR (required)")
	timeout := fs.Duration("timeout", 6*time.Second, "wait for Connection Complete per attempt")
	attempts := fs.Int("attempts", 3, "total page attempts")
	retryDelay := fs.Duration("retry-delay", 2*time.Second, "pause between attempts")
	asJSON := fs.Bool("json", false, "print the full result as JSON")
	quiet := fs.Bool("quiet", false, "suppress progress on stderr")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	if *ctrlS == "" || *ps5S == "" {
		fs.Usage()
		return 1
	}
	ctrl, err := btaddr.Parse(*ctrlS)
	if err != nil {
		return fail(fmt.Errorf("--controller: %w", err))
	}
	ps5, err := btaddr.Parse(*ps5S)
	if err != nil {
		return fail(fmt.Errorf("--ps5: %w", err))
	}
	logf := func(format string, a ...any) {
		if !*quiet {
			fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05.000 ")+format+"\n", a...)
		}
	}
	res, err := wake.Wake(ctx, wake.Options{
		Adapter: *dev, Controller: ctrl, Console: ps5,
		Timeout: *timeout, Attempts: *attempts, RetryDelay: *retryDelay, Log: logf,
	})
	if *asJSON {
		printJSON(res)
	} else if err == nil {
		fmt.Printf("%s: 0x%02X %s after %d attempt(s); adapter restored: %v\n",
			res.Outcome, res.Status, res.StatusText, res.Attempts, res.Restored)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		if !res.Restored && !res.OriginalAddr.IsZero() {
			fmt.Fprintf(os.Stderr, "WARNING: adapter %s may still carry the spoofed address; original was %s\n", *dev, res.OriginalAddr)
		}
		if errors.Is(err, context.Canceled) {
			return 1
		}
		return 3
	}
	if res.Outcome == wake.OutcomeSuccess {
		return 0
	}
	return 3
}
