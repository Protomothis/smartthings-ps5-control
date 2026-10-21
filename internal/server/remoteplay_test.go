package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/config"
	"github.com/protomothis/smartthings-ps5-control/internal/discovery"
	"github.com/protomothis/smartthings-ps5-control/internal/wake"
)

// fakeRP scripts the Remote Play client. states is the sequence of
// discovery states the probe reports, advanced on every probe.
type fakeRP struct {
	mu         sync.Mutex
	wakes      int
	standbys   int
	registers  int
	wakeErr    error
	standbyErr error
	regErr     error
	lastPIN    string
}

func (f *fakeRP) Register(ctx context.Context, accountID, pin string) (config.RemotePlay, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registers++
	f.lastPIN = pin
	if f.regErr != nil {
		return config.RemotePlay{}, f.regErr
	}
	return config.RemotePlay{AccountID: accountID, RegistKey: "3031323334353637", RPKey: strings.Repeat("ab", 16), Nickname: "PS5-123", Mac: "aabbccddeeff", RegisteredAt: time.Now()}, nil
}

func (f *fakeRP) Wake(ctx context.Context, rp config.RemotePlay) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wakes++
	return f.wakeErr
}

func (f *fakeRP) Standby(ctx context.Context, rp config.RemotePlay) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.standbys++
	return f.standbyErr
}

type stateScript struct {
	mu     sync.Mutex
	states []discovery.State
	i      int
}

func (s *stateScript) probe(ctx context.Context, host string, timeout time.Duration) (discovery.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[min(s.i, len(s.states)-1)]
	s.i++
	return discovery.Status{State: st}, nil
}

func rpFixture(t *testing.T, registered bool, states ...discovery.State) (*httptest.Server, *config.Store, *fakeRP, *fakeDeps) {
	t.Helper()
	store, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := store.Get()
	c.PS5IP = "192.168.1.20"
	if err := store.Set(c); err != nil {
		t.Fatal(err)
	}
	if registered {
		if err := store.SetRemotePlay(&config.RemotePlay{AccountID: "AAAAAAAAAEI=", RegistKey: "3031323334353637", RPKey: strings.Repeat("ab", 16), Nickname: "PS5-123"}); err != nil {
			t.Fatal(err)
		}
	}
	rp := &fakeRP{}
	fd := &fakeDeps{wakeRes: wake.Result{Outcome: wake.OutcomeSuccess, Status: 0x13, Restored: true}}
	deps := fd.deps()
	script := &stateScript{states: states}
	deps.Probe = script.probe
	deps.RemotePlay = func(host string) RemotePlayClient { return rp }
	deps.AccountIDFromUserID = func(userID string) (string, error) {
		if userID != "1234567890123" {
			return "", errors.New("bad user id")
		}
		return "ywT7cR8BAAA=", nil
	}
	deps.AccountIDFromRedirect = func(ctx context.Context, u string) (string, error) {
		if !strings.Contains(u, "code=") {
			return "", errors.New("no code")
		}
		return "ywT7cR8BAAA=", nil
	}
	deps.LoginURL = "https://example.invalid/login"
	s := New(store, deps, Auth{User: "admin", Pass: "secret"}, "test", log.New(io.Discard, "", 0))
	s.timing = timing{pollInterval: 10 * time.Millisecond, networkWakeTimeout: 200 * time.Millisecond, standbyTimeout: 200 * time.Millisecond}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, store, rp, fd
}

func call(t *testing.T, ts *httptest.Server, method, path, body string, headers map[string]string, auth bool) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if auth {
		req.SetBasicAuth("admin", "secret")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var m map[string]any
	data, _ := io.ReadAll(res.Body)
	if len(data) > 0 {
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("bad json %q: %v", data, err)
		}
	}
	return res.StatusCode, m
}

func TestWakeAlreadyOn(t *testing.T) {
	ts, _, rp, fd := rpFixture(t, true, discovery.StateOn)
	code, m := call(t, ts, "POST", "/wake", "", nil, false)
	if code != 200 || m["method"] != "none" || m["outcome"] != "success" {
		t.Fatalf("%d %v", code, m)
	}
	if rp.wakes != 0 || fd.wakes != 0 {
		t.Fatal("nothing should have been sent")
	}
}

func TestWakeNetworkFromStandby(t *testing.T) {
	ts, _, rp, fd := rpFixture(t, true, discovery.StateStandby, discovery.StateOn)
	code, m := call(t, ts, "POST", "/wake", "", nil, false)
	if code != 200 || m["method"] != "network" || m["state"] != "on" {
		t.Fatalf("%d %v", code, m)
	}
	if rp.wakes != 1 || fd.wakes != 0 {
		t.Fatalf("network wakes=%d bluetooth wakes=%d", rp.wakes, fd.wakes)
	}
}

func TestWakeNetworkFailsOverToBluetooth(t *testing.T) {
	ts, store, rp, fd := rpFixture(t, true, discovery.StateStandby)
	rp.wakeErr = errors.New("udp blocked")
	c := store.Get()
	c.ControllerAddr.UnmarshalText([]byte("aa:bb:cc:dd:ee:ff"))
	c.PS5Addr.UnmarshalText([]byte("11:22:33:44:55:66"))
	if err := store.Set(c); err != nil {
		t.Fatal(err)
	}
	code, m := call(t, ts, "POST", "/wake", "", nil, false)
	if code != 200 || m["method"] != "bluetooth" || m["bluetooth"] == nil {
		t.Fatalf("%d %v", code, m)
	}
	if rp.wakes != 1 || fd.wakes != 1 {
		t.Fatalf("network wakes=%d bluetooth wakes=%d", rp.wakes, fd.wakes)
	}
}

func TestWakeOffWithoutBluetoothConfigIs409(t *testing.T) {
	ts, _, rp, _ := rpFixture(t, true, discovery.StateOff)
	code, m := call(t, ts, "POST", "/wake", "", nil, false)
	if code != 409 || !strings.Contains(m["error"].(string), "controller_addr") {
		t.Fatalf("%d %v", code, m)
	}
	if rp.wakes != 0 {
		t.Fatal("a console that is off cannot be woken over the network")
	}
}

func TestWakeMethodNetworkForced(t *testing.T) {
	ts, _, rp, _ := rpFixture(t, true, discovery.StateOff, discovery.StateOff)
	code, m := call(t, ts, "POST", "/wake?method=network", "", nil, false)
	if code != 504 || m["method"] != "network" || rp.wakes != 1 {
		t.Fatalf("%d %v wakes=%d", code, m, rp.wakes)
	}
	if code, _ := call(t, ts, "POST", "/wake?method=bogus", "", nil, false); code != 400 {
		t.Fatalf("bogus method = %d", code)
	}
}

func TestStandby(t *testing.T) {
	ts, _, rp, _ := rpFixture(t, true, discovery.StateOn, discovery.StateStandby)
	code, m := call(t, ts, "POST", "/standby", "", nil, false)
	if code != 200 || m["state"] != "standby" || rp.standbys != 1 {
		t.Fatalf("%d %v standbys=%d", code, m, rp.standbys)
	}
}

func TestStandbyAlreadyOff(t *testing.T) {
	ts, _, rp, _ := rpFixture(t, true, discovery.StateStandby)
	code, m := call(t, ts, "POST", "/standby", "", nil, false)
	if code != 200 || m["outcome"] != "success" || rp.standbys != 0 {
		t.Fatalf("%d %v standbys=%d", code, m, rp.standbys)
	}
}

func TestStandbyUnregistered(t *testing.T) {
	ts, _, _, _ := rpFixture(t, false, discovery.StateOn)
	code, m := call(t, ts, "POST", "/standby", "", nil, false)
	if code != 409 || !strings.Contains(m["error"].(string), "remote_play") {
		t.Fatalf("%d %v", code, m)
	}
}

func TestStandbySessionError(t *testing.T) {
	ts, _, rp, _ := rpFixture(t, true, discovery.StateOn)
	rp.standbyErr = errors.New("passcode")
	code, m := call(t, ts, "POST", "/standby", "", nil, false)
	if code != 502 || !strings.Contains(m["error"].(string), "passcode") {
		t.Fatalf("%d %v", code, m)
	}
}

func TestSecretGuardsActionsOnly(t *testing.T) {
	ts, store, _, _ := rpFixture(t, true, discovery.StateOn)
	c := store.Get()
	c.APISecret = "s3cret"
	if err := store.Set(c); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, ts, "GET", "/status", "", nil, false); code != 200 {
		t.Fatalf("status should stay open: %d", code)
	}
	if code, _ := call(t, ts, "POST", "/wake", "", nil, false); code != 401 {
		t.Fatalf("wake without secret = %d", code)
	}
	if code, _ := call(t, ts, "POST", "/standby", "", map[string]string{SecretHeader: "wrong"}, false); code != 401 {
		t.Fatalf("standby with wrong secret = %d", code)
	}
	if code, _ := call(t, ts, "POST", "/wake", "", map[string]string{SecretHeader: "s3cret"}, false); code != 200 {
		t.Fatalf("wake with secret = %d", code)
	}
}

func TestRegisterFlowAndRedaction(t *testing.T) {
	ts, store, rp, _ := rpFixture(t, false, discovery.StateOn)
	code, m := call(t, ts, "GET", "/api/remoteplay", "", nil, true)
	if code != 200 || m["registered"] != false || m["login_url"] != "https://example.invalid/login" || m["available"] != true {
		t.Fatalf("%d %v", code, m)
	}
	code, m = call(t, ts, "POST", "/api/remoteplay/account", `{"user_id":"1234567890123"}`, nil, true)
	if code != 200 || m["account_id"] != "ywT7cR8BAAA=" {
		t.Fatalf("%d %v", code, m)
	}
	code, m = call(t, ts, "POST", "/api/remoteplay/account", `{"redirect_url":"https://remoteplay.dl.playstation.net/remoteplay/redirect?code=abc"}`, nil, true)
	if code != 200 || m["account_id"] != "ywT7cR8BAAA=" {
		t.Fatalf("%d %v", code, m)
	}
	if code, _ := call(t, ts, "POST", "/api/remoteplay/account", `{}`, nil, true); code != 400 {
		t.Fatalf("empty account request = %d", code)
	}
	code, m = call(t, ts, "POST", "/api/remoteplay/register", `{"account_id":"ywT7cR8BAAA=","pin":"12345678"}`, nil, true)
	if code != 200 || m["registered"] != true || m["nickname"] != "PS5-123" || rp.lastPIN != "12345678" {
		t.Fatalf("%d %v", code, m)
	}
	if !store.Get().RemotePlay.Registered() {
		t.Fatal("registration not stored")
	}
	// the settings form must neither see the keys nor be able to clobber them
	code, m = call(t, ts, "GET", "/api/config", "", nil, true)
	rpv := m["remote_play"].(map[string]any)
	if code != 200 || rpv["rp_key"] != "***" || rpv["regist_key"] != "***" {
		t.Fatalf("%d %v", code, m)
	}
	body := `{"ps5_ip":"192.168.1.20","controller_addr":"","ps5_addr":"","hci":"hci0","status_timeout_ms":2000,"wake_timeout_ms":6000,"wake_attempts":3,"wake_retry_ms":2000,"api_secret":"x","remote_play":{"account_id":"zzz","regist_key":"***","rp_key":"***","nickname":"nope","mac":"","registered_at":"0001-01-01T00:00:00Z"}}`
	if code, m := call(t, ts, "PUT", "/api/config", body, nil, true); code != 200 {
		t.Fatalf("put config = %d %v", code, m)
	}
	if got := store.Get().RemotePlay; got.RPKey != strings.Repeat("ab", 16) || got.Nickname != "PS5-123" {
		t.Fatalf("registration clobbered: %+v", got)
	}
	if store.Get().APISecret != "x" {
		t.Fatal("api secret not saved")
	}
	rp.regErr = errors.New("80108b09")
	if code, _ := call(t, ts, "POST", "/api/remoteplay/register", `{"account_id":"a","pin":"00000000"}`, nil, true); code != 502 {
		t.Fatalf("failed registration = %d", code)
	}
	code, m = call(t, ts, "DELETE", "/api/remoteplay", "", nil, true)
	if code != 200 || m["registered"] != false || store.Get().RemotePlay != nil {
		t.Fatalf("%d %v", code, m)
	}
	// registration routes are behind basic auth
	if code, _ := call(t, ts, "POST", "/api/remoteplay/register", `{"account_id":"a","pin":"00000000"}`, nil, false); code != 401 {
		t.Fatalf("register without auth = %d", code)
	}
}

func TestRemotePlayUnavailable(t *testing.T) {
	store, _ := config.Open(t.TempDir())
	c := store.Get()
	c.PS5IP = "192.168.1.20"
	_ = store.Set(c)
	_ = store.SetRemotePlay(&config.RemotePlay{RegistKey: "x", RPKey: "y"})
	fd := &fakeDeps{status: discovery.Status{State: discovery.StateOn}}
	s := New(store, fd.deps(), Auth{User: "admin", Pass: "secret"}, "test", log.New(io.Discard, "", 0))
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	if code, _ := call(t, ts, "POST", "/standby", "", nil, false); code != 501 {
		t.Fatalf("standby without remote play = %d", code)
	}
	if code, m := call(t, ts, "GET", "/api/remoteplay", "", nil, true); code != 200 || m["available"] != false {
		t.Fatalf("%d %v", code, m)
	}
}
