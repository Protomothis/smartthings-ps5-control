package server

import (
	"context"
	"sync"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/config"
	"github.com/protomothis/smartthings-ps5-control/internal/wake"
)

// serverInfo rides along in GET /status so the Edge driver can show more
// than on/off: whether the Bluetooth path and the Remote Play path are
// usable, what the last wake and standby did, and the versions.
type serverInfo struct {
	Version    string         `json:"version"`
	InstanceID string         `json:"instance_id"`
	Bluetooth  bluetoothInfo  `json:"bluetooth"`
	RemotePlay remotePlayInfo `json:"remote_play"`
	LastWake   *actionRecord  `json:"last_wake,omitempty"`
	LastStand  *actionRecord  `json:"last_standby,omitempty"`
	Waking     bool           `json:"waking"`
	WakingFrom *time.Time     `json:"waking_since,omitempty"`
}

// bluetoothInfo is the adapter check, cached (see adapterCache).
type bluetoothInfo struct {
	// Configured: controller and console addresses are set.
	Configured bool `json:"configured"`
	// Checked: the adapter has been inspected at least once.
	Checked bool `json:"checked"`
	// Supported: the chipset can rewrite its BD_ADDR.
	Supported    bool       `json:"supported"`
	Adapter      string     `json:"adapter,omitempty"`
	Manufacturer string     `json:"manufacturer,omitempty"`
	Rating       int        `json:"rating,omitempty"`
	Error        string     `json:"error,omitempty"`
	CheckedAt    *time.Time `json:"checked_at,omitempty"`
}

type remotePlayInfo struct {
	Registered bool   `json:"registered"`
	Nickname   string `json:"nickname,omitempty"`
}

// actionRecord is the outcome of the last wake or standby.
type actionRecord struct {
	At         time.Time `json:"at"`
	Method     string    `json:"method,omitempty"` // wake: none / network / bluetooth
	Outcome    string    `json:"outcome"`
	StatusText string    `json:"status_text,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// adapterTTL is how long an adapter check is trusted before /status asks
// again (in the background, so the poll stays fast).
const adapterTTL = 10 * time.Minute

// adapterCache remembers the last wake.Inspect result.
type adapterCache struct {
	mu         sync.Mutex
	info       bluetoothInfo
	adapter    string
	refreshing bool
}

// bluetooth returns the cached check for the configured adapter and starts a
// refresh when it is missing or stale.
func (s *Server) bluetooth(c config.Config) bluetoothInfo {
	s.adapter.mu.Lock()
	defer s.adapter.mu.Unlock()
	info := s.adapter.info
	stale := !info.Checked || s.adapter.adapter != c.HCI || info.CheckedAt == nil || time.Since(*info.CheckedAt) > adapterTTL
	if stale && !s.adapter.refreshing && s.deps.Inspect != nil && c.HCI != "" {
		s.adapter.refreshing = true
		go s.refreshAdapter(c.HCI)
	}
	info.Configured = c.WakeReady() == nil
	return info
}

func (s *Server) refreshAdapter(adapter string) {
	// Inspect talks to the adapter (bring-up, version, address); during a
	// wake the address is spoofed and a reset is in flight, so wait for the
	// next poll instead of racing it.
	if !s.wakeMu.TryLock() {
		s.adapter.mu.Lock()
		s.adapter.refreshing = false
		s.adapter.mu.Unlock()
		return
	}
	ai, err := s.deps.Inspect(adapter)
	s.wakeMu.Unlock()
	now := time.Now()
	info := bluetoothInfo{Checked: true, Adapter: adapter, CheckedAt: &now}
	if err != nil {
		info.Error = err.Error()
	} else {
		info.Supported = ai.Supported
		info.Manufacturer = ai.Manufacturer
		info.Rating = ai.Rating
	}
	s.adapter.mu.Lock()
	s.adapter.info = info
	s.adapter.adapter = adapter
	s.adapter.refreshing = false
	s.adapter.mu.Unlock()
	if err != nil {
		s.logc("wake").Warn("adapter check failed", "adapter", adapter, "err", err.Error())
	} else {
		s.logc("wake").Info("adapter check", "adapter", adapter, "manufacturer", ai.Manufacturer, "supported", ai.Supported, "rating", ai.Rating)
	}
}

// CheckAdapterNow runs the adapter check synchronously (startup, tests).
func (s *Server) CheckAdapterNow(ctx context.Context) {
	c := s.cfg.Get()
	if s.deps.Inspect == nil || c.HCI == "" {
		return
	}
	done := make(chan struct{})
	go func() { s.refreshAdapter(c.HCI); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// actions remembers the last wake and standby.
type actions struct {
	mu          sync.Mutex
	lastWake    *actionRecord
	lastStandby *actionRecord
	wakingSince *time.Time
}

func (s *Server) noteWake(method string, outcome wake.Outcome, statusText string, err error) {
	rec := &actionRecord{At: time.Now(), Method: method, Outcome: string(outcome), StatusText: statusText}
	if err != nil {
		rec.Error = err.Error()
	}
	s.actions.mu.Lock()
	s.actions.lastWake = rec
	if outcome == wake.OutcomeSuccess && method != "none" {
		now := rec.At
		s.actions.wakingSince = &now
	} else {
		s.actions.wakingSince = nil
	}
	s.actions.mu.Unlock()
}

func (s *Server) noteStandby(outcome wake.Outcome, err error) {
	rec := &actionRecord{At: time.Now(), Outcome: string(outcome)}
	if err != nil {
		rec.Error = err.Error()
	}
	s.actions.mu.Lock()
	s.actions.lastStandby = rec
	s.actions.wakingSince = nil
	s.actions.mu.Unlock()
}

// wakingWindow is how long after a successful wake the console is reported
// as "waking" while discovery still says it is not on.
const wakingWindow = 90 * time.Second

// info assembles the server block of /status. state is what discovery said.
func (s *Server) info(c config.Config, stateOn bool) serverInfo {
	s.actions.mu.Lock()
	lw, ls, ws := s.actions.lastWake, s.actions.lastStandby, s.actions.wakingSince
	if ws != nil && (stateOn || time.Since(*ws) > wakingWindow) {
		s.actions.wakingSince = nil
		ws = nil
	}
	s.actions.mu.Unlock()
	out := serverInfo{
		Version:    s.version,
		InstanceID: c.InstanceID,
		Bluetooth:  s.bluetooth(c),
		RemotePlay: remotePlayInfo{Registered: c.RemotePlay.Registered()},
		LastWake:   lw,
		LastStand:  ls,
		Waking:     ws != nil,
		WakingFrom: ws,
	}
	if c.RemotePlay.Registered() {
		out.RemotePlay.Nickname = c.RemotePlay.Nickname
	}
	return out
}
