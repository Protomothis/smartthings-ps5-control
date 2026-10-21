package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/config"
	"github.com/protomothis/smartthings-ps5-control/internal/server"
)

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func cmdServe(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dataDir := fs.String("data", envOr("PS5_DATA", "/data"), "data directory for config.json and tls/ (env PS5_DATA)")
	httpAddr := fs.String("http", envOr("PS5_HTTP_ADDR", ":8580"), "plain HTTP listen address, empty to disable (env PS5_HTTP_ADDR)")
	httpsAddr := fs.String("https", envOr("PS5_HTTPS_ADDR", ":8543"), "HTTPS listen address with a self-signed cert, empty to disable (env PS5_HTTPS_ADDR)")
	tlsHosts := fs.String("tls-hosts", envOr("PS5_TLS_HOSTS", ""), "extra comma-separated names/IPs for the self-signed cert (env PS5_TLS_HOSTS)")
	authUser := fs.String("auth-user", envOr("PS5_AUTH_USER", ""), "basic auth user for the settings page (env PS5_AUTH_USER)")
	authPass := fs.String("auth-pass", envOr("PS5_AUTH_PASS", ""), "basic auth password (env PS5_AUTH_PASS)")
	_ = fs.Parse(args) // ExitOnError: never returns an error

	if *httpAddr == "" && *httpsAddr == "" {
		return fail(errors.New("nothing to listen on: set --http and/or --https"))
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)

	store, err := config.Open(*dataDir)
	if err != nil {
		return fail(err)
	}
	logger.Printf("config: %s", store.Path())

	auth := server.Auth{User: *authUser, Pass: *authPass}
	if !auth.Enabled() {
		logger.Printf("WARNING: PS5_AUTH_USER/PS5_AUTH_PASS not set; settings page and config API are disabled")
	}
	deps := server.RealDeps()
	wireRemotePlay(&deps, func(f string, a ...any) { logger.Printf("remoteplay: "+f, a...) })
	srv := server.New(store, deps, auth, version, logger)
	handler := srv.Handler()

	var servers []*http.Server
	errc := make(chan error, 2)

	if *httpAddr != "" {
		hs := &http.Server{Addr: *httpAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, hs)
		go func() {
			logger.Printf("http: listening on %s", *httpAddr)
			if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("http: %w", err)
			}
		}()
	}
	if *httpsAddr != "" {
		var hosts []string
		for _, h := range strings.Split(*tlsHosts, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hosts = append(hosts, h)
			}
		}
		cert, created, err := server.EnsureCert(*dataDir, hosts)
		if err != nil {
			return fail(err)
		}
		if created {
			logger.Printf("tls: generated self-signed certificate in %s/tls", *dataDir)
		}
		hs := &http.Server{
			Addr: *httpsAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second,
			TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		}
		servers = append(servers, hs)
		go func() {
			logger.Printf("https: listening on %s (self-signed; needed for WebHID in the settings page)", *httpsAddr)
			if err := hs.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("https: %w", err)
			}
		}()
	}

	code := 0
	select {
	case <-ctx.Done():
		logger.Printf("shutting down")
	case err := <-errc:
		logger.Printf("error: %v", err)
		code = 1
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdownCtx)
	}
	return code
}
