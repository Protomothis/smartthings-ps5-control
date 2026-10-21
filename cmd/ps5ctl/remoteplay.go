package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/config"
	"github.com/protomothis/smartthings-ps5-control/internal/remoteplay"
	"github.com/protomothis/smartthings-ps5-control/internal/server"
)

// rpAdapter bridges the Remote Play protocol package to the server's
// config-typed interface.
type rpAdapter struct {
	client *remoteplay.Client
}

func newRemotePlay(logf func(string, ...any)) func(host string) server.RemotePlayClient {
	return func(host string) server.RemotePlayClient {
		c := remoteplay.New(host)
		c.Log = logf
		return rpAdapter{client: c}
	}
}

func toRegistration(rp config.RemotePlay) remoteplay.Registration {
	return remoteplay.Registration{
		AccountID:    rp.AccountID,
		RegistKey:    rp.RegistKey,
		RPKey:        rp.RPKey,
		Nickname:     rp.Nickname,
		Mac:          rp.Mac,
		RegisteredAt: rp.RegisteredAt,
	}
}

func fromRegistration(reg remoteplay.Registration) config.RemotePlay {
	return config.RemotePlay{
		AccountID:    reg.AccountID,
		RegistKey:    reg.RegistKey,
		RPKey:        reg.RPKey,
		Nickname:     reg.Nickname,
		Mac:          reg.Mac,
		RegisteredAt: reg.RegisteredAt,
	}
}

func (a rpAdapter) Register(ctx context.Context, accountID, pin string) (config.RemotePlay, error) {
	reg, err := a.client.Register(ctx, accountID, pin)
	if err != nil {
		return config.RemotePlay{}, err
	}
	return fromRegistration(reg), nil
}

func (a rpAdapter) Wake(ctx context.Context, rp config.RemotePlay) error {
	return a.client.Wake(ctx, toRegistration(rp))
}

func (a rpAdapter) Standby(ctx context.Context, rp config.RemotePlay) error {
	return a.client.Standby(ctx, toRegistration(rp))
}

// wireRemotePlay fills the Remote Play fields of the server deps.
func wireRemotePlay(deps *server.Deps, logf func(string, ...any)) {
	deps.RemotePlay = newRemotePlay(logf)
	deps.AccountIDFromRedirect = remoteplay.AccountIDFromRedirectURL
	deps.AccountIDFromUserID = remoteplay.AccountIDFromUserID
	deps.LoginURL = remoteplay.LoginURL
}

// cmdRP is the CLI for hardware validation of the Remote Play path without
// the settings page. It reads and writes the same config.json the server uses.
//
//	ps5ctl rp account  --user-id 1234567890123 | --redirect-url <url>
//	ps5ctl rp register --data /data --account <id> --pin 12345678
//	ps5ctl rp wake     --data /data
//	ps5ctl rp standby  --data /data
func cmdRP(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ps5ctl rp <account|register|wake|standby> [flags]")
		return 1
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("rp "+sub, flag.ExitOnError)
	dataDir := fs.String("data", envOr("PS5_DATA", "/data"), "data directory with config.json (env PS5_DATA)")
	ip := fs.String("ip", "", "PS5 IP (default: ps5_ip from config.json)")
	userID := fs.String("user-id", "", "account: numeric PSN user id")
	redirect := fs.String("redirect-url", "", "account: the URL the PSN login redirected to")
	account := fs.String("account", "", "register: base64 account ID (from `rp account`)")
	pin := fs.String("pin", "", "register: 8-digit PIN from Settings > System > Remote Play > Link Device")
	_ = fs.Parse(rest) // ExitOnError: never returns an error

	logf := func(f string, a ...any) { fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05.000 ")+f+"\n", a...) }

	if sub == "account" {
		switch {
		case *userID != "":
			id, err := remoteplay.AccountIDFromUserID(*userID)
			if err != nil {
				return fail(err)
			}
			fmt.Println(id)
			return 0
		case *redirect != "":
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			id, err := remoteplay.AccountIDFromRedirectURL(cctx, *redirect)
			if err != nil {
				return fail(err)
			}
			fmt.Println(id)
			return 0
		default:
			fmt.Fprintln(os.Stderr, "open this URL in a browser, sign in, and paste the final URL with --redirect-url:")
			fmt.Println(remoteplay.LoginURL)
			return 1
		}
	}

	store, err := config.Open(*dataDir)
	if err != nil {
		return fail(err)
	}
	c := store.Get()
	host := *ip
	if host == "" {
		host = c.PS5IP
	}
	if host == "" {
		return fail(fmt.Errorf("no PS5 IP: pass --ip or set ps5_ip in %s", store.Path()))
	}
	client := remoteplay.New(host)
	client.Log = logf

	switch sub {
	case "register":
		if *account == "" || *pin == "" {
			fs.Usage()
			return 1
		}
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		reg, err := client.Register(cctx, *account, *pin)
		if err != nil {
			return fail(err)
		}
		rp := fromRegistration(reg)
		if err := store.SetRemotePlay(&rp); err != nil {
			return fail(err)
		}
		fmt.Printf("registered with %q (%s); saved to %s\n", rp.Nickname, rp.Mac, store.Path())
		return 0
	case "wake", "standby":
		if !c.RemotePlay.Registered() {
			return fail(fmt.Errorf("not registered: run `ps5ctl rp register` first"))
		}
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		if sub == "wake" {
			if err := client.Wake(cctx, toRegistration(*c.RemotePlay)); err != nil {
				return fail(err)
			}
			fmt.Println("WAKEUP sent; check `ps5ctl status` in a few seconds")
			return 0
		}
		if err := client.Standby(cctx, toRegistration(*c.RemotePlay)); err != nil {
			return fail(err)
		}
		fmt.Println("standby sent; check `ps5ctl status` in a few seconds")
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown rp command %q\n", sub)
		return 1
	}
}
