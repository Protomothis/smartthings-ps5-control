// Package config persists the service settings as JSON in the data directory.
package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/btaddr"
	"github.com/protomothis/smartthings-ps5-control/internal/wake"
)

// FileName is the config file inside the data directory.
const FileName = "config.json"

// Config is everything the settings page edits. Durations are milliseconds
// so the JSON stays readable and editable by hand.
type Config struct {
	PS5IP          string      `json:"ps5_ip"`
	ControllerAddr btaddr.Addr `json:"controller_addr"`
	PS5Addr        btaddr.Addr `json:"ps5_addr"`
	HCI            string      `json:"hci"`

	StatusTimeoutMS int `json:"status_timeout_ms"`
	WakeTimeoutMS   int `json:"wake_timeout_ms"`
	WakeAttempts    int `json:"wake_attempts"`
	WakeRetryMS     int `json:"wake_retry_ms"`

	// APISecret, when set, is required in the X-PS5-Secret header of the
	// LAN actions (POST /wake, POST /standby). Status stays open.
	APISecret string `json:"api_secret"`

	// RemotePlay is the Remote Play registration. It is written only by the
	// registration flow, never by the settings form (Set keeps it).
	RemotePlay *RemotePlay `json:"remote_play,omitempty"`
}

// RemotePlay is the credential the console hands out when a PIN is accepted.
// RegistKey and RPKey let anyone start a Remote Play session, so the API
// never returns them (see Redacted).
type RemotePlay struct {
	AccountID    string    `json:"account_id"`
	RegistKey    string    `json:"regist_key"`
	RPKey        string    `json:"rp_key"`
	Nickname     string    `json:"nickname"`
	Mac          string    `json:"mac"`
	RegisteredAt time.Time `json:"registered_at"`
}

// Registered reports whether the registration carries both keys.
func (r *RemotePlay) Registered() bool {
	return r != nil && r.RegistKey != "" && r.RPKey != ""
}

// Redacted returns a copy safe to show: keys replaced by their presence.
func (c Config) Redacted() Config {
	if c.RemotePlay != nil {
		rp := *c.RemotePlay
		if rp.RegistKey != "" {
			rp.RegistKey = "***"
		}
		if rp.RPKey != "" {
			rp.RPKey = "***"
		}
		c.RemotePlay = &rp
	}
	return c
}

// RemotePlayReady reports whether a network wake or a standby can run.
func (c Config) RemotePlayReady() error {
	if err := c.StatusReady(); err != nil {
		return err
	}
	if !c.RemotePlay.Registered() {
		return fmt.Errorf("%w: remote_play (register on the settings page)", ErrNotConfigured)
	}
	return nil
}

// Default returns the settings a fresh install starts with.
func Default() Config {
	return Config{
		HCI:             "hci0",
		StatusTimeoutMS: 2000,
		WakeTimeoutMS:   6000,
		WakeAttempts:    3,
		WakeRetryMS:     2000,
	}
}

// StatusTimeout returns the status probe timeout.
func (c Config) StatusTimeout() time.Duration {
	return time.Duration(c.StatusTimeoutMS) * time.Millisecond
}

// WakeTimeout returns the per-attempt page timeout.
func (c Config) WakeTimeout() time.Duration { return time.Duration(c.WakeTimeoutMS) * time.Millisecond }

// WakeRetryDelay returns the pause between wake attempts.
func (c Config) WakeRetryDelay() time.Duration {
	return time.Duration(c.WakeRetryMS) * time.Millisecond
}

// ErrNotConfigured marks a request that needs a setting that is still empty.
var ErrNotConfigured = errors.New("not configured")

// StatusReady reports whether a status probe can run.
func (c Config) StatusReady() error {
	if c.PS5IP == "" {
		return fmt.Errorf("%w: ps5_ip", ErrNotConfigured)
	}
	return nil
}

// WakeReady reports whether a wake can run.
func (c Config) WakeReady() error {
	var missing []string
	if c.ControllerAddr.IsZero() {
		missing = append(missing, "controller_addr")
	}
	if c.PS5Addr.IsZero() {
		missing = append(missing, "ps5_addr")
	}
	if c.HCI == "" {
		missing = append(missing, "hci")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %v", ErrNotConfigured, missing)
	}
	return nil
}

// Validate rejects values that cannot work; empty addresses are allowed so a
// partially filled page can still be saved.
func (c Config) Validate() error {
	if c.PS5IP != "" {
		if ip := net.ParseIP(c.PS5IP); ip == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err := net.DefaultResolver.LookupHost(ctx, c.PS5IP); err != nil {
				return fmt.Errorf("ps5_ip: %q is neither an IP nor a resolvable name", c.PS5IP)
			}
		}
	}
	if c.HCI != "" {
		if _, err := wake.ParseAdapter(c.HCI); err != nil {
			return fmt.Errorf("hci: %w", err)
		}
	}
	if c.StatusTimeoutMS < 200 || c.StatusTimeoutMS > 30000 {
		return errors.New("status_timeout_ms: must be between 200 and 30000")
	}
	if c.WakeTimeoutMS < 1000 || c.WakeTimeoutMS > 30000 {
		return errors.New("wake_timeout_ms: must be between 1000 and 30000")
	}
	if c.WakeAttempts < 1 || c.WakeAttempts > 10 {
		return errors.New("wake_attempts: must be between 1 and 10")
	}
	if c.WakeRetryMS < 0 || c.WakeRetryMS > 30000 {
		return errors.New("wake_retry_ms: must be between 0 and 30000")
	}
	return nil
}

// Store loads and saves the config file with a mutex around the in-memory copy.
type Store struct {
	path string
	mu   sync.RWMutex
	cur  Config
}

// Open reads dir/config.json, creating it with defaults when missing.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("config: create data dir: %w", err)
	}
	s := &Store{path: filepath.Join(dir, FileName), cur: Default()}
	data, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := s.write(s.cur); err != nil {
			return nil, err
		}
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("config: read %s: %w", s.path, err)
	}
	c := Default()
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", s.path, err)
	}
	s.cur = c
	return s, nil
}

// Path returns the config file location.
func (s *Store) Path() string { return s.path }

// Get returns a copy of the current config.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Set validates, persists and swaps in a new config. The Remote Play
// registration is server-owned: whatever c carries, the stored one is kept.
func (s *Store) Set(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c.RemotePlay = s.cur.RemotePlay
	if err := s.write(c); err != nil {
		return err
	}
	s.cur = c
	return nil
}

// SetRemotePlay stores (or, with nil, clears) the Remote Play registration.
func (s *Store) SetRemotePlay(rp *RemotePlay) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cur
	c.RemotePlay = rp
	if err := s.write(c); err != nil {
		return err
	}
	s.cur = c
	return nil
}

func (s *Store) write(c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("config: replace %s: %w", s.path, err)
	}
	return nil
}
