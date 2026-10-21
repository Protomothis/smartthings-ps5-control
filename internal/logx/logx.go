// Package logx is the service's logging: one slog handler that writes the
// same line to stderr (container logs) and to a size-capped rotating file in
// the data directory, and keeps the most recent entries in memory for the
// settings page.
//
// Lines look like
//
//	2026-10-21 15:04:05.123 INFO  wake: network wake state=standby
//
// where "wake" is the `component` attribute. Every logger handed out is a
// *slog.Logger, so call sites use Info/Warn/Error with key=value attrs.
package logx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// ComponentKey is the attribute that names the subsystem a line comes from.
const ComponentKey = "component"

// Entry is one log line as the API returns it.
type Entry struct {
	Seq       uint64    `json:"seq"`
	Time      time.Time `json:"time"`
	Level     string    `json:"level"`
	Component string    `json:"component,omitempty"`
	Message   string    `json:"message"`
	Attrs     string    `json:"attrs,omitempty"`
}

// Line renders the entry the way the file and stderr carry it.
func (e Entry) Line() string {
	var b strings.Builder
	b.WriteString(e.Time.Format("2006-01-02 15:04:05.000"))
	b.WriteByte(' ')
	fmt.Fprintf(&b, "%-5s ", e.Level)
	if e.Component != "" {
		b.WriteString(e.Component)
		b.WriteString(": ")
	}
	b.WriteString(e.Message)
	if e.Attrs != "" {
		b.WriteByte(' ')
		b.WriteString(e.Attrs)
	}
	b.WriteByte('\n')
	return b.String()
}

// Options configures a Hub.
type Options struct {
	// Level is the minimum level recorded anywhere (default Info).
	Level slog.Level
	// Stderr receives every line; nil disables it.
	Stderr io.Writer
	// File receives every line and serves Dump; nil disables it.
	File *RotatingFile
	// RingSize is how many recent entries Entries can return (default 500).
	RingSize int
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Hub fans one log stream out to its sinks and remembers recent entries.
type Hub struct {
	level slog.Level
	now   func() time.Time

	mu     sync.Mutex
	stderr io.Writer
	file   *RotatingFile
	ring   []Entry
	next   int // index the next entry goes to
	count  int
	seq    uint64
}

// New builds a Hub.
func New(opts Options) *Hub {
	size := opts.RingSize
	if size <= 0 {
		size = 500
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Hub{level: opts.Level, now: now, stderr: opts.Stderr, file: opts.File, ring: make([]Entry, size)}
}

// Logger returns a slog.Logger writing into the hub.
func (h *Hub) Logger() *slog.Logger {
	return slog.New(&handler{hub: h})
}

// Close flushes and closes the file sink.
func (h *Hub) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.file != nil {
		return h.file.Close()
	}
	return nil
}

// Entries returns up to limit entries with Seq > after, oldest first, and the
// Seq of the newest entry the hub holds (pass it back as `after` next time).
func (h *Hub) Entries(after uint64, limit int) ([]Entry, uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if limit <= 0 || limit > len(h.ring) {
		limit = len(h.ring)
	}
	var out []Entry
	start := (h.next - h.count + len(h.ring)) % len(h.ring)
	for i := 0; i < h.count; i++ {
		e := h.ring[(start+i)%len(h.ring)]
		if e.Seq > after {
			out = append(out, e)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, h.seq
}

// HasFile reports whether a file sink is configured (Dump can work).
func (h *Hub) HasFile() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.file != nil
}

// Dump writes the whole file log, oldest rotation first, then the current
// file. It is the download the settings page offers.
func (h *Hub) Dump(w io.Writer) error {
	h.mu.Lock()
	f := h.file
	h.mu.Unlock()
	if f == nil {
		return os.ErrNotExist
	}
	return f.Dump(w)
}

func (h *Hub) record(e Entry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	e.Seq = h.seq
	h.ring[h.next] = e
	h.next = (h.next + 1) % len(h.ring)
	if h.count < len(h.ring) {
		h.count++
	}
	line := e.Line()
	if h.stderr != nil {
		_, _ = io.WriteString(h.stderr, line)
	}
	if h.file != nil {
		_, _ = io.WriteString(h.file, line)
	}
}

// handler is the slog.Handler. Attributes added with With are kept as a
// prefix; groups flatten to dotted keys.
type handler struct {
	hub   *Hub
	attrs []slog.Attr
	group string
}

func (hd *handler) Enabled(_ context.Context, l slog.Level) bool { return l >= hd.hub.level }

func (hd *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := &handler{hub: hd.hub, group: hd.group}
	n.attrs = append(append([]slog.Attr(nil), hd.attrs...), prefixed(hd.group, attrs)...)
	return n
}

func (hd *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return hd
	}
	g := name
	if hd.group != "" {
		g = hd.group + "." + name
	}
	return &handler{hub: hd.hub, attrs: hd.attrs, group: g}
}

func prefixed(group string, attrs []slog.Attr) []slog.Attr {
	if group == "" {
		return attrs
	}
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, slog.Attr{Key: group + "." + a.Key, Value: a.Value})
	}
	return out
}

func (hd *handler) Handle(_ context.Context, r slog.Record) error {
	// The hub's clock, not r.Time, so tests can pin timestamps.
	e := Entry{Time: hd.hub.now(), Level: levelName(r.Level), Message: r.Message}
	var b strings.Builder
	add := func(a slog.Attr) {
		a.Value = a.Value.Resolve()
		if a.Key == ComponentKey {
			e.Component = a.Value.String()
			return
		}
		if a.Equal(slog.Attr{}) {
			return
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(a.Key)
		b.WriteByte('=')
		s := a.Value.String()
		if strings.ContainsAny(s, " \t\n\"") || s == "" {
			s = fmt.Sprintf("%q", s)
		}
		b.WriteString(s)
	}
	for _, a := range hd.attrs {
		add(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		for _, p := range prefixed(hd.group, []slog.Attr{a}) {
			add(p)
		}
		return true
	})
	e.Attrs = b.String()
	hd.hub.record(e)
	return nil
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARN"
	case l >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}

// ParseLevel reads "debug", "info", "warn" or "error" (any case).
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("logx: unknown level %q", s)
}
