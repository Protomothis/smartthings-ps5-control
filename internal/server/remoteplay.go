package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/config"
	"github.com/protomothis/smartthings-ps5-control/internal/discovery"
	"github.com/protomothis/smartthings-ps5-control/internal/wake"
)

// RemotePlayClient is what the server needs from the Remote Play protocol,
// expressed in config types so this package does not depend on the protocol
// package; cmd/ps5ctl adapts the real client.
type RemotePlayClient interface {
	// Register pairs with the console using the PIN it shows and returns the
	// credential to store.
	Register(ctx context.Context, accountID, pin string) (config.RemotePlay, error)
	// Wake sends the network wake datagram (rest mode only).
	Wake(ctx context.Context, rp config.RemotePlay) error
	// Standby asks a console that is on to enter rest mode.
	Standby(ctx context.Context, rp config.RemotePlay) error
}

// SecretHeader carries the API secret on the LAN actions.
const SecretHeader = "X-PS5-Secret" //nolint:gosec // a header name, not a credential

// secretRequired guards an action with the configured API secret. With no
// secret configured the action is open (LAN only, as documented).
func (s *Server) secretRequired(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		want := s.cfg.Get().APISecret
		if want != "" {
			got := r.Header.Get(SecretHeader)
			hw, hg := sha256.Sum256([]byte(want)), sha256.Sum256([]byte(got))
			if subtle.ConstantTimeCompare(hw[:], hg[:]) != 1 {
				writeError(w, http.StatusUnauthorized, errors.New("missing or wrong "+SecretHeader))
				return
			}
		}
		next(w, r)
	}
}

// waitForState polls discovery until the console reports want (or any of
// wants) or the deadline passes. It returns the last state seen.
func (s *Server) waitForState(ctx context.Context, c config.Config, timeout time.Duration, wants ...discovery.State) (discovery.State, bool) {
	deadline := time.Now().Add(timeout)
	last := discovery.StateUnknown
	for {
		st, err := s.deps.Probe(ctx, c.PS5IP, c.StatusTimeout())
		if err == nil {
			last = st.State
			for _, w := range wants {
				if st.State == w {
					return st.State, true
				}
			}
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return last, false
		}
		select {
		case <-ctx.Done():
			return last, false
		case <-time.After(s.timing.pollInterval):
		}
	}
}

// Timing of the state polls after a network wake or a standby. A rest-mode
// PS5 usually answers discovery within ten seconds of a WAKEUP; tests
// shorten all three.
type timing struct {
	pollInterval       time.Duration
	networkWakeTimeout time.Duration
	standbyTimeout     time.Duration
}

func defaultTiming() timing {
	return timing{pollInterval: 2 * time.Second, networkWakeTimeout: 30 * time.Second, standbyTimeout: 30 * time.Second}
}

// wakeResponse is what POST /wake returns.
type wakeResponse struct {
	Method  string          `json:"method"` // "none" (already on), "network", "bluetooth"
	State   discovery.State `json:"state,omitempty"`
	Outcome wake.Outcome    `json:"outcome"`
	Error   string          `json:"error,omitempty"`
	// Bluetooth carries the HCI details when the Bluetooth path ran.
	Bluetooth *wake.Result `json:"bluetooth,omitempty"`
}

// handleWake wakes the console: a console that is on is left alone; one in
// rest mode gets the network wake when registered; otherwise (or when that
// fails) the Bluetooth page. ?method=network|bluetooth forces one path.
func (s *Server) handleWake(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	method := r.URL.Query().Get("method")
	if method != "" && method != "auto" && method != "network" && method != "bluetooth" {
		writeError(w, http.StatusBadRequest, errors.New("method must be auto, network or bluetooth"))
		return
	}
	if !s.wakeMu.TryLock() {
		writeError(w, http.StatusLocked, errors.New("a wake or standby is already in progress"))
		return
	}
	defer s.wakeMu.Unlock()

	// Finish the sequence even if the client goes away; an aborted
	// Bluetooth sequence could leave the adapter with the spoofed address.
	budget := time.Duration(c.WakeAttempts)*(c.WakeTimeout()+c.WakeRetryDelay()) + s.timing.networkWakeTimeout + 15*time.Second
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), budget)
	defer cancel()

	state := discovery.StateUnknown
	if c.StatusReady() == nil {
		if st, err := s.deps.Probe(ctx, c.PS5IP, c.StatusTimeout()); err == nil {
			state = st.State
		}
	}
	if state == discovery.StateOn && method != "bluetooth" {
		writeJSON(w, http.StatusOK, wakeResponse{Method: "none", State: state, Outcome: wake.OutcomeSuccess})
		return
	}

	var netErr error
	canNetwork := method != "bluetooth" && c.RemotePlayReady() == nil && s.deps.RemotePlay != nil
	if canNetwork && (state == discovery.StateStandby || method == "network") {
		s.logc("wake").Info("network wake", "state", string(state))
		if err := s.deps.RemotePlay(c.PS5IP).Wake(ctx, *c.RemotePlay); err != nil {
			netErr = err
			s.logc("wake").Warn("network wake failed", "err", err.Error())
		} else if st, ok := s.waitForState(ctx, c, s.timing.networkWakeTimeout, discovery.StateOn); ok {
			writeJSON(w, http.StatusOK, wakeResponse{Method: "network", State: st, Outcome: wake.OutcomeSuccess})
			return
		} else {
			netErr = errors.New("console did not answer discovery after the network wake")
			s.logc("wake").Warn(netErr.Error(), "last_state", string(st))
		}
	}
	if method == "network" {
		if netErr == nil {
			netErr = c.RemotePlayReady()
		}
		if netErr == nil {
			netErr = errors.New("network wake needs the console in rest mode")
		}
		writeJSON(w, http.StatusGatewayTimeout, wakeResponse{Method: "network", State: state, Outcome: wake.OutcomeTimeout, Error: netErr.Error()})
		return
	}

	if err := c.WakeReady(); err != nil {
		if netErr != nil {
			err = errors.Join(netErr, err)
		}
		writeJSON(w, http.StatusConflict, wakeResponse{Method: "bluetooth", State: state, Outcome: wake.OutcomeError, Error: err.Error()})
		return
	}
	s.logc("wake").Info("bluetooth wake", "state", string(state))
	res, err := s.deps.Wake(ctx, wake.Options{
		Adapter:    c.HCI,
		Controller: c.ControllerAddr,
		Console:    c.PS5Addr,
		Timeout:    c.WakeTimeout(),
		Attempts:   c.WakeAttempts,
		RetryDelay: c.WakeRetryDelay(),
		Log:        func(f string, a ...any) { s.logc("wake").Info(fmt.Sprintf(f, a...)) },
	})
	rec := &wakeRecord{At: time.Now(), Result: res}
	if err != nil {
		rec.Error = err.Error()
	}
	s.lastMu.Lock()
	s.lastRes = rec
	s.lastMu.Unlock()

	// The page was answered; the console boots for a while longer. The
	// caller (the Edge driver) watches /status for it, so do not wait here.
	resp := wakeResponse{Method: "bluetooth", State: state, Outcome: res.Outcome, Error: rec.Error, Bluetooth: &res}
	writeJSON(w, bluetoothStatusCode(err, res), resp)
}

func bluetoothStatusCode(err error, res wake.Result) int {
	switch {
	case err != nil:
		return bluetoothErrorCode(err)
	case res.Outcome == wake.OutcomeSuccess:
		return http.StatusOK
	case res.Outcome == wake.OutcomeTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusBadGateway
	}
}

// standbyResponse is what POST /standby returns.
type standbyResponse struct {
	State   discovery.State `json:"state,omitempty"`
	Outcome wake.Outcome    `json:"outcome"`
	Error   string          `json:"error,omitempty"`
}

// handleStandby puts a console that is on into rest mode over Remote Play.
func (s *Server) handleStandby(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	if err := c.RemotePlayReady(); err != nil {
		writeJSON(w, http.StatusConflict, standbyResponse{Outcome: wake.OutcomeError, Error: err.Error()})
		return
	}
	if s.deps.RemotePlay == nil {
		writeJSON(w, http.StatusNotImplemented, standbyResponse{Outcome: wake.OutcomeError, Error: "remote play is not available in this build"})
		return
	}
	if !s.wakeMu.TryLock() {
		writeError(w, http.StatusLocked, errors.New("a wake or standby is already in progress"))
		return
	}
	defer s.wakeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), s.timing.standbyTimeout+30*time.Second)
	defer cancel()

	st, err := s.deps.Probe(ctx, c.PS5IP, c.StatusTimeout())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, standbyResponse{Outcome: wake.OutcomeError, Error: err.Error()})
		return
	}
	if st.State != discovery.StateOn {
		// Rest mode or off: nothing to do, and no session to open.
		writeJSON(w, http.StatusOK, standbyResponse{State: st.State, Outcome: wake.OutcomeSuccess})
		return
	}
	s.logc("standby").Info("opening remote play session")
	if err := s.deps.RemotePlay(c.PS5IP).Standby(ctx, *c.RemotePlay); err != nil {
		s.logc("standby").Warn("standby failed", "err", err.Error())
		writeJSON(w, http.StatusBadGateway, standbyResponse{State: st.State, Outcome: wake.OutcomeError, Error: err.Error()})
		return
	}
	state, ok := s.waitForState(ctx, c, s.timing.standbyTimeout, discovery.StateStandby, discovery.StateOff)
	if !ok {
		writeJSON(w, http.StatusGatewayTimeout, standbyResponse{State: state, Outcome: wake.OutcomeTimeout, Error: "standby was sent but the console still reports on"})
		return
	}
	writeJSON(w, http.StatusOK, standbyResponse{State: state, Outcome: wake.OutcomeSuccess})
}

// remotePlayView is what the settings page sees of the registration.
type remotePlayView struct {
	Registered   bool      `json:"registered"`
	AccountID    string    `json:"account_id,omitempty"`
	Nickname     string    `json:"nickname,omitempty"`
	Mac          string    `json:"mac,omitempty"`
	RegisteredAt time.Time `json:"registered_at,omitempty"`
	LoginURL     string    `json:"login_url"`
	Available    bool      `json:"available"`
}

func (s *Server) remotePlayView() remotePlayView {
	v := remotePlayView{LoginURL: s.deps.LoginURL, Available: s.deps.RemotePlay != nil}
	if rp := s.cfg.Get().RemotePlay; rp.Registered() {
		v.Registered = true
		v.AccountID = rp.AccountID
		v.Nickname = rp.Nickname
		v.Mac = rp.Mac
		v.RegisteredAt = rp.RegisteredAt
	}
	return v
}

func (s *Server) handleGetRemotePlay(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.remotePlayView())
}

// handleAccountID turns a PSN login redirect URL (or a numeric user id) into
// the base64 account ID the registration needs. The login happens in the
// user's own browser; only the one-time code from the redirect reaches here.
func (s *Server) handleAccountID(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RedirectURL string `json:"redirect_url"`
		UserID      string `json:"user_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch {
	case strings.TrimSpace(in.UserID) != "":
		if s.deps.AccountIDFromUserID == nil {
			writeError(w, http.StatusNotImplemented, errors.New("remote play is not available in this build"))
			return
		}
		id, err := s.deps.AccountIDFromUserID(strings.TrimSpace(in.UserID))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"account_id": id})
	case strings.TrimSpace(in.RedirectURL) != "":
		if s.deps.AccountIDFromRedirect == nil {
			writeError(w, http.StatusNotImplemented, errors.New("remote play is not available in this build"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		id, err := s.deps.AccountIDFromRedirect(ctx, strings.TrimSpace(in.RedirectURL))
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"account_id": id})
	default:
		writeError(w, http.StatusBadRequest, errors.New("redirect_url or user_id is required"))
	}
}

// handleRegister pairs with the console and stores the credential.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AccountID string `json:"account_id"`
		PIN       string `json:"pin"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	c := s.cfg.Get()
	if err := c.StatusReady(); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if s.deps.RemotePlay == nil {
		writeError(w, http.StatusNotImplemented, errors.New("remote play is not available in this build"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	rp, err := s.deps.RemotePlay(c.PS5IP).Register(ctx, strings.TrimSpace(in.AccountID), strings.TrimSpace(in.PIN))
	if err != nil {
		s.logc("remoteplay").Warn("registration failed", "err", err.Error())
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if err := s.cfg.SetRemotePlay(&rp); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logc("remoteplay").Info("registered", "nickname", rp.Nickname, "mac", rp.Mac)
	writeJSON(w, http.StatusOK, s.remotePlayView())
}

func (s *Server) handleUnregister(w http.ResponseWriter, r *http.Request) {
	if err := s.cfg.SetRemotePlay(nil); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logc("remoteplay").Info("registration removed")
	writeJSON(w, http.StatusOK, s.remotePlayView())
}
