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
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/config"
	"github.com/protomothis/smartthings-ps5-control/internal/discovery"
	"github.com/protomothis/smartthings-ps5-control/internal/hci"
	"github.com/protomothis/smartthings-ps5-control/internal/spoof"
	"github.com/protomothis/smartthings-ps5-control/internal/wake"
)

//go:embed static
var static embed.FS

// Deps are the hardware-facing functions, injectable for tests.
type Deps struct {
	Probe   func(ctx context.Context, host string, timeout time.Duration) (discovery.Status, error)
	Wake    func(ctx context.Context, o wake.Options) (wake.Result, error)
	Inspect func(adapter string) (wake.AdapterInfo, error)
}

// RealDeps talks to the network and the adapter.
func RealDeps() Deps {
	return Deps{Probe: discovery.Probe, Wake: wake.Wake, Inspect: wake.Inspect}
}

// Server holds the handlers' shared state.
type Server struct {
	cfg     *config.Store
	deps    Deps
	auth    Auth
	log     *log.Logger
	version string

	wakeMu  sync.Mutex
	lastMu  sync.Mutex
	lastRes *wakeRecord
}

type wakeRecord struct {
	At     time.Time   `json:"at"`
	Result wake.Result `json:"result"`
	Error  string      `json:"error,omitempty"`
}

// New builds a Server. logger may be nil.
func New(cfg *config.Store, deps Deps, auth Auth, version string, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	return &Server{cfg: cfg, deps: deps, auth: auth, log: logger, version: version}
}

// Handler returns the routed http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
	})
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("POST /wake", s.handleWake)
	mux.HandleFunc("GET /api/adapter", s.handleAdapter)
	mux.HandleFunc("GET /api/wake/last", s.handleLastWake)

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
	return s.logging(mux)
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path != "/healthz" {
			s.log.Printf("%s %s %d %s %s", r.Method, r.URL.Path, rec.code, time.Since(start).Round(time.Millisecond), r.RemoteAddr)
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
	CheckedAt time.Time `json:"checked_at"`
	Host      string    `json:"host"`
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
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: st, CheckedAt: time.Now(), Host: c.PS5IP})
}

// wakeResponse is what POST /wake returns.
type wakeResponse struct {
	wake.Result
	Error string `json:"error,omitempty"`
}

func (s *Server) handleWake(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	if err := c.WakeReady(); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if !s.wakeMu.TryLock() {
		writeError(w, http.StatusLocked, errors.New("a wake is already in progress"))
		return
	}
	defer s.wakeMu.Unlock()

	// Finish the spoof/page/restore sequence even if the client goes away;
	// an aborted sequence could leave the adapter with the spoofed address.
	budget := time.Duration(c.WakeAttempts)*(c.WakeTimeout()+c.WakeRetryDelay()) + 15*time.Second
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), budget)
	defer cancel()

	res, err := s.deps.Wake(ctx, wake.Options{
		Adapter:    c.HCI,
		Controller: c.ControllerAddr,
		Console:    c.PS5Addr,
		Timeout:    c.WakeTimeout(),
		Attempts:   c.WakeAttempts,
		RetryDelay: c.WakeRetryDelay(),
		Log:        func(f string, a ...any) { s.log.Printf("wake: "+f, a...) },
	})
	rec := &wakeRecord{At: time.Now(), Result: res}
	if err != nil {
		rec.Error = err.Error()
	}
	s.lastMu.Lock()
	s.lastRes = rec
	s.lastMu.Unlock()

	resp := wakeResponse{Result: res, Error: rec.Error}
	switch {
	case errors.Is(err, spoof.ErrUnsupportedAdapter):
		writeJSON(w, http.StatusNotImplemented, resp)
	case errors.Is(err, hci.ErrPermission):
		writeJSON(w, http.StatusInternalServerError, resp)
	case err != nil:
		writeJSON(w, http.StatusBadGateway, resp)
	case res.Outcome == wake.OutcomeSuccess:
		writeJSON(w, http.StatusOK, resp)
	case res.Outcome == wake.OutcomeTimeout:
		writeJSON(w, http.StatusGatewayTimeout, resp)
	default:
		writeJSON(w, http.StatusBadGateway, resp)
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
	writeJSON(w, http.StatusOK, s.cfg.Get())
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
	s.log.Printf("config saved: ps5=%s ctrl=%s ps5bt=%s hci=%s", c.PS5IP, c.ControllerAddr, c.PS5Addr, c.HCI)
	writeJSON(w, http.StatusOK, s.cfg.Get())
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":     s.version,
		"config_path": s.cfg.Path(),
		"tls":         r.TLS != nil,
	})
}
