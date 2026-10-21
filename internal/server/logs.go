package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/logx"
)

// logc returns the server logger tagged with a component name.
func (s *Server) logc(component string) *slog.Logger {
	return s.log.With(logx.ComponentKey, component)
}

// accessLog records one request. Routine traffic (the driver's polls, the
// settings page) stays at debug; errors are worth seeing.
func (s *Server) accessLog(r *http.Request, code int, took time.Duration) {
	l := s.logc("http")
	attrs := []any{"method", r.Method, "path", r.URL.Path, "status", code, "took", took.Round(time.Millisecond).String(), "from", r.RemoteAddr}
	switch {
	case code >= 500:
		l.Error("request failed", attrs...)
	case code >= 400 && code != http.StatusUnauthorized:
		l.Warn("request rejected", attrs...)
	default:
		l.Debug("request", attrs...)
	}
}

// logsResponse is what GET /api/logs returns.
type logsResponse struct {
	Entries []logx.Entry `json:"entries"`
	Next    uint64       `json:"next"`
}

// handleLogs returns recent entries newer than ?after=<seq>, at most
// ?limit=<n> (default 200). The client passes `next` back as `after`.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("log buffer is not enabled"))
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 200
	}
	entries, next := s.hub.Entries(after, limit)
	if entries == nil {
		entries = []logx.Entry{}
	}
	writeJSON(w, http.StatusOK, logsResponse{Entries: entries, Next: next})
}

// handleLogsDownload streams the whole file log (rotations first).
func (s *Server) handleLogsDownload(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil || !s.hub.HasFile() {
		writeError(w, http.StatusServiceUnavailable, errors.New("file log is not enabled"))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ps5ctl-`+time.Now().Format("20060102-150405")+`.log"`)
	w.Header().Set("Cache-Control", "no-store")
	if err := s.hub.Dump(w); err != nil {
		s.logc("server").Warn("log download failed", "err", err.Error())
	}
}
