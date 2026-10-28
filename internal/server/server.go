// Package server exposes the status probe and wake over HTTP for the LAN and
// serves the settings page.
//
//	GET  /healthz        liveness
//	GET  /status         PS5 power state (unauthenticated, LAN only)
//	POST /wake           wake the PS5 (unauthenticated, LAN only, one at a time)
//	GET  /api/adapter    Bluetooth adapter chipset / spoof support
//	GET  /                settings page              (basic auth)
//	GET  /api/config     current settings            (basic auth)
//	PUT  /api/config     replace settings            (basic auth)
//	GET  /api/info       version and runtime hints   (basic auth)
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/config"
	"github.com/protomothis/smartthings-ps5-control/internal/discovery"
	"github.com/protomothis/smartthings-ps5-control/internal/hci"
	"github.com/protomothis/smartthings-ps5-control/internal/logx"
	"github.com/protomothis/smartthings-ps5-control/internal/spoof"
	"github.com/protomothis/smartthings-ps5-control/internal/ssdp"
	"github.com/protomothis/smartthings-ps5-control/internal/wake"
)

//go:embed static
var static embed.FS

// Deps are the hardware-facing functions, injectable for tests.
type Deps struct {
	Probe   func(ctx context.Context, host string, timeout time.Duration) (discovery.Status, error)
	Wake    func(ctx context.Context, o wake.Options) (wake.Result, error)
	Inspect func(adapter string) (wake.AdapterInfo, error)

	// RemotePlay returns a client for the console at host; nil disables the
	// network wake, standby and registration routes.
	RemotePlay func(host string) RemotePlayClient
	// AccountID turns what the settings page sends (base64 account ID or
	// numeric PSN user ID) into the base64 form the registration needs.
	AccountID func(s string) (string, error)
	// HTTPPort is the plain HTTP port the SSDP description advertises.
	HTTPPort int
}

// RealDeps talks to the network and the adapter. The Remote Play fields are
// wired by cmd/ps5ctl, which adapts the protocol package.
func RealDeps() Deps {
	return Deps{Probe: discovery.Probe, Wake: wake.Wake, Inspect: wake.Inspect}
}

// Server holds the handlers' shared state.
type Server struct {
	cfg     *config.Store
	deps    Deps
	auth    Auth
	log     *slog.Logger
	hub     *logx.Hub
	version string

	wakeMu  sync.Mutex
	lastMu  sync.Mutex
	lastRes *wakeRecord
	timing  timing
	adapter adapterCache
	actions actions
}

type wakeRecord struct {
	At     time.Time   `json:"at"`
	Result wake.Result `json:"result"`
	Error  string      `json:"error,omitempty"`
}

// New builds a Server. logger may be nil; hub (optional) backs GET /api/logs.
func New(cfg *config.Store, deps Deps, auth Auth, version string, logger *slog.Logger, hub *logx.Hub) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{cfg: cfg, deps: deps, auth: auth, log: logger, hub: hub, version: version, timing: defaultTiming()}
}

// Handler returns the routed http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
	})
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("POST /wake", s.secretRequired(s.handleWake))
	mux.HandleFunc("POST /standby", s.secretRequired(s.handleStandby))
	mux.HandleFunc("GET /api/adapter", s.handleAdapter)
	mux.HandleFunc("GET /api/wake/last", s.handleLastWake)
	mux.Handle("GET /api/remoteplay", s.auth.Wrap(http.HandlerFunc(s.handleGetRemotePlay)))
	mux.Handle("POST /api/remoteplay/register", s.auth.Wrap(http.HandlerFunc(s.handleRegister)))
	mux.Handle("DELETE /api/remoteplay", s.auth.Wrap(http.HandlerFunc(s.handleUnregister)))

	// http.FileServer would redirect /index.html to "./", so serve the bytes.
	page, err := fs.ReadFile(static, "static/index.html")
	if err != nil {
		panic("server: embedded settings page missing: " + err.Error())
	}
	mux.Handle("GET /{$}", s.auth.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(page)
	})))
	mux.Handle("GET /api/config", s.auth.Wrap(http.HandlerFunc(s.handleGetConfig)))
	mux.Handle("PUT /api/config", s.auth.Wrap(http.HandlerFunc(s.handlePutConfig)))
	mux.Handle("GET /api/info", s.auth.Wrap(http.HandlerFunc(s.handleInfo)))
	mux.HandleFunc("GET "+ssdp.DescriptionPath, s.handleSSDPDescription)
	mux.Handle("GET /api/logs", s.auth.Wrap(http.HandlerFunc(s.handleLogs)))
	mux.Handle("GET /api/logs/download", s.auth.Wrap(http.HandlerFunc(s.handleLogsDownload)))
	return s.logging(mux)
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path != "/healthz" && !strings.HasPrefix(r.URL.Path, "/api/logs") {
			s.accessLog(r, rec.code, time.Since(start))
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(c int) { r.code = c; r.ResponseWriter.WriteHeader(c) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// statusResponse is what GET /status returns.
type statusResponse struct {
	discovery.Status
	CheckedAt time.Time  `json:"checked_at"`
	Host      string     `json:"host"`
	Server    serverInfo `json:"server"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	if err := c.StatusReady(); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), c.StatusTimeout()+2*time.Second)
	defer cancel()
	st, err := s.deps.Probe(ctx, c.PS5IP, c.StatusTimeout())
	if err != nil {
		// The access log only carries the status code; the reason matters here.
		s.logc("status").Warn("probe failed", "host", c.PS5IP, "err", err)
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: st, CheckedAt: time.Now(), Host: c.PS5IP, Server: s.info(c, st.State == discovery.StateOn)})
}

// bluetoothErrorCode maps a failed Bluetooth wake to an HTTP status.
func bluetoothErrorCode(err error) int {
	switch {
	case errors.Is(err, spoof.ErrUnsupportedAdapter):
		return http.StatusNotImplemented
	case errors.Is(err, hci.ErrPermission):
		return http.StatusInternalServerError
	default:
		return http.StatusBadGateway
	}
}

func (s *Server) handleLastWake(w http.ResponseWriter, r *http.Request) {
	s.lastMu.Lock()
	rec := s.lastRes
	s.lastMu.Unlock()
	if rec == nil {
		writeJSON(w, http.StatusOK, map[string]any{"result": nil})
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleAdapter(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	if c.HCI == "" {
		writeError(w, http.StatusConflict, errors.New("hci not configured"))
		return
	}
	info, err := s.deps.Inspect(c.HCI)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "adapter": info})
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Get().Redacted())
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var c config.Config
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.cfg.Set(c); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.logc("config").Info("settings saved", "ps5_ip", c.PS5IP, "controller", c.ControllerAddr.String(), "ps5_bt", c.PS5Addr.String(), "hci", c.HCI, "secret_set", c.APISecret != "")
	writeJSON(w, http.StatusOK, s.cfg.Get().Redacted())
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":     s.version,
		"config_path": s.cfg.Path(),
		"tls":         r.TLS != nil,
		"instance_id": s.cfg.Get().InstanceID,
	})
}

// SSDPInfo is what the discovery responder advertises: the instance id, a
// label for the new device (the console's Remote Play nickname once
// registered) and the HTTP port.
func (s *Server) SSDPInfo() ssdp.Info {
	c := s.cfg.Get()
	name := "PS5"
	if c.RemotePlay.Registered() && c.RemotePlay.Nickname != "" {
		name = c.RemotePlay.Nickname
	}
	return ssdp.Info{ID: c.InstanceID, Name: name, Version: s.version, Port: s.deps.HTTPPort}
}

// handleSSDPDescription is the LOCATION of the SSDP answer: unauthenticated
// on purpose, it carries only what the driver needs to create the device.
func (s *Server) handleSSDPDescription(w http.ResponseWriter, r *http.Request) {
	info := s.SSDPInfo()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      info.ID,
		"name":    info.Name,
		"version": info.Version,
		"port":    info.Port,
		"st":      ssdp.ST,
	})
}
