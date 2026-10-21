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

	"github.com/protomothis/smartthings-ps5-control/internal/btaddr"
	"github.com/protomothis/smartthings-ps5-control/internal/config"
	"github.com/protomothis/smartthings-ps5-control/internal/discovery"
	"github.com/protomothis/smartthings-ps5-control/internal/spoof"
	"github.com/protomothis/smartthings-ps5-control/internal/wake"
)

type fixture struct {
	srv   *httptest.Server
	store *config.Store
	deps  *fakeDeps
}

type fakeDeps struct {
	mu       sync.Mutex
	status   discovery.Status
	statusEr error
	wakeRes  wake.Result
	wakeErr  error
	wakeHold chan struct{} // when non-nil, Wake blocks until closed
	wakes    int
	lastOpts wake.Options
}

func (f *fakeDeps) deps() Deps {
	return Deps{
		Probe: func(ctx context.Context, host string, timeout time.Duration) (discovery.Status, error) {
			return f.status, f.statusEr
		},
		Wake: func(ctx context.Context, o wake.Options) (wake.Result, error) {
			f.mu.Lock()
			f.wakes++
			f.lastOpts = o
			hold := f.wakeHold
			f.mu.Unlock()
			if hold != nil {
				<-hold
			}
			return f.wakeRes, f.wakeErr
		},
		Inspect: func(adapter string) (wake.AdapterInfo, error) {
			return wake.AdapterInfo{Adapter: adapter, Manufacturer: "Broadcom", Supported: true, Rating: 4}, nil
		},
	}
}

func newFixture(t *testing.T, configured bool) *fixture {
	t.Helper()
	store, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if configured {
		c := store.Get()
		c.PS5IP = "192.168.1.20"
		c.ControllerAddr = btaddr.MustParse("aa:bb:cc:dd:ee:ff")
		c.PS5Addr = btaddr.MustParse("11:22:33:44:55:66")
		if err := store.Set(c); err != nil {
			t.Fatal(err)
		}
	}
	fd := &fakeDeps{status: discovery.Status{State: discovery.StateStandby, Code: 620, Fields: map[string]string{"host-type": "PS5"}}}
	s := New(store, fd.deps(), Auth{User: "admin", Pass: "secret"}, "test", log.New(io.Discard, "", 0))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &fixture{srv: ts, store: store, deps: fd}
}

func (f *fixture) do(t *testing.T, method, path, body string, auth bool) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
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
	if len(data) > 0 && strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("bad json %q: %v", data, err)
		}
	}
	return res.StatusCode, m
}

func TestStatus(t *testing.T) {
	f := newFixture(t, true)
	code, m := f.do(t, "GET", "/status", "", false)
	if code != 200 || m["state"] != "standby" || m["host"] != "192.168.1.20" {
		t.Fatalf("%d %v", code, m)
	}
	f.deps.statusEr = errors.New("boom")
	if code, _ := f.do(t, "GET", "/status", "", false); code != 502 {
		t.Fatalf("code = %d", code)
	}
}

func TestStatusUnconfigured(t *testing.T) {
	f := newFixture(t, false)
	code, m := f.do(t, "GET", "/status", "", false)
	if code != 409 || !strings.Contains(m["error"].(string), "ps5_ip") {
		t.Fatalf("%d %v", code, m)
	}
}

func TestWakeOutcomes(t *testing.T) {
	f := newFixture(t, true)
	f.deps.wakeRes = wake.Result{Outcome: wake.OutcomeSuccess, Status: 0x13, StatusText: "x", Attempts: 1, Restored: true}
	code, m := f.do(t, "POST", "/wake", "", false)
	if code != 200 || m["outcome"] != "success" {
		t.Fatalf("%d %v", code, m)
	}
	if o := f.deps.lastOpts; o.Adapter != "hci0" || o.Controller.String() != "AA:BB:CC:DD:EE:FF" || o.Attempts != 3 || o.Timeout != 6*time.Second {
		t.Fatalf("opts = %+v", o)
	}

	f.deps.wakeRes = wake.Result{Outcome: wake.OutcomeTimeout, Status: 0x04, Attempts: 3, Restored: true}
	if code, _ := f.do(t, "POST", "/wake", "", false); code != 504 {
		t.Fatalf("timeout code = %d", code)
	}

	f.deps.wakeErr = &spoof.UnsupportedError{Manufacturer: spoof.MfrRealtek}
	code, m = f.do(t, "POST", "/wake", "", false)
	if code != 501 || !strings.Contains(m["error"].(string), "Realtek") {
		t.Fatalf("%d %v", code, m)
	}

	code, m = f.do(t, "GET", "/api/wake/last", "", false)
	if code != 200 || m["error"] == nil {
		t.Fatalf("last: %d %v", code, m)
	}
}

func TestWakeBusy(t *testing.T) {
	f := newFixture(t, true)
	f.deps.wakeHold = make(chan struct{})
	f.deps.wakeRes = wake.Result{Outcome: wake.OutcomeSuccess}
	done := make(chan int)
	go func() { c, _ := f.do(t, "POST", "/wake", "", false); done <- c }()
	// wait until the first wake is inside the fake
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.deps.mu.Lock()
		n := f.deps.wakes
		f.deps.mu.Unlock()
		if n == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code, _ := f.do(t, "POST", "/wake", "", false); code != 423 {
		t.Fatalf("second wake code = %d", code)
	}
	close(f.deps.wakeHold)
	if c := <-done; c != 200 {
		t.Fatalf("first wake code = %d", c)
	}
}

func TestWakeUnconfigured(t *testing.T) {
	f := newFixture(t, false)
	if code, _ := f.do(t, "POST", "/wake", "", false); code != 409 {
		t.Fatalf("code = %d", code)
	}
}

func TestAuth(t *testing.T) {
	f := newFixture(t, false)
	for _, p := range []string{"/", "/api/config", "/api/info"} {
		if code, _ := f.do(t, "GET", p, "", false); code != 401 {
			t.Fatalf("%s without auth = %d", p, code)
		}
	}
	req, _ := http.NewRequest("GET", f.srv.URL+"/", nil)
	req.SetBasicAuth("admin", "wrong")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(res.Header.Get("WWW-Authenticate"), "Basic") {
		t.Fatalf("wrong password = %d", res.StatusCode)
	}
	req.SetBasicAuth("admin", "secret")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "navigator.hid") {
		t.Fatalf("page = %d %.80q", res.StatusCode, body)
	}
	// public routes never challenge
	if code, _ := f.do(t, "GET", "/healthz", "", false); code != 200 {
		t.Fatal("healthz should be public")
	}
}

func TestAuthDisabled(t *testing.T) {
	store, _ := config.Open(t.TempDir())
	s := New(store, (&fakeDeps{}).deps(), Auth{}, "test", log.New(io.Discard, "", 0))
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	res, err := http.Get(ts.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("code = %d", res.StatusCode)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	f := newFixture(t, false)
	body := `{"ps5_ip":"192.168.1.20","controller_addr":"aa:bb:cc:dd:ee:ff","ps5_addr":"11:22:33:44:55:66","hci":"hci1","status_timeout_ms":1500,"wake_timeout_ms":6000,"wake_attempts":2,"wake_retry_ms":1000}`
	code, m := f.do(t, "PUT", "/api/config", body, true)
	if code != 200 || m["controller_addr"] != "AA:BB:CC:DD:EE:FF" || m["hci"] != "hci1" {
		t.Fatalf("%d %v", code, m)
	}
	if got := f.store.Get(); got.WakeAttempts != 2 || got.PS5IP != "192.168.1.20" {
		t.Fatalf("store = %+v", got)
	}
	code, m = f.do(t, "PUT", "/api/config", `{"hci":"nope"}`, true)
	if code != 400 || !strings.Contains(m["error"].(string), "hci") {
		t.Fatalf("%d %v", code, m)
	}
	code, m = f.do(t, "PUT", "/api/config", `{"bogus":1}`, true)
	if code != 400 {
		t.Fatalf("unknown field accepted: %d %v", code, m)
	}
	code, m = f.do(t, "PUT", "/api/config", `{"controller_addr":"zz"}`, true)
	if code != 400 {
		t.Fatalf("bad addr accepted: %d %v", code, m)
	}
}

func TestAdapter(t *testing.T) {
	f := newFixture(t, false)
	code, m := f.do(t, "GET", "/api/adapter", "", false)
	if code != 200 || m["supported"] != true || m["adapter"] != "hci0" {
		t.Fatalf("%d %v", code, m)
	}
}

func TestEnsureCert(t *testing.T) {
	dir := t.TempDir()
	c1, created, err := EnsureCert(dir, []string{"192.168.1.50", "ps5.lan"})
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	c2, created, err := EnsureCert(dir, nil)
	if err != nil || created {
		t.Fatalf("second call created=%v err=%v", created, err)
	}
	if string(c1.Certificate[0]) != string(c2.Certificate[0]) {
		t.Fatal("certificate changed between calls")
	}
}
