package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/protomothis/smartthings-ps5-control/internal/config"
	"github.com/protomothis/smartthings-ps5-control/internal/discovery"
	"github.com/protomothis/smartthings-ps5-control/internal/wake"
)

// The adapter check repeats every ten minutes; an unchanged result must not
// add an info line each time (on the first install it was one line in 50 of
// a log otherwise filled with routine traffic).
func TestAdapterCheckLogsChangesOnly(t *testing.T) {
	store, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	deps := (&fakeDeps{}).deps()
	supported := true
	deps.Inspect = func(adapter string) (wake.AdapterInfo, error) {
		return wake.AdapterInfo{Adapter: adapter, Manufacturer: "Broadcom", Supported: supported, Rating: 4}, nil
	}
	var buf bytes.Buffer
	s := New(store, deps, Auth{}, "test", slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), nil)
	ctx := context.Background()
	s.CheckAdapterNow(ctx)
	s.CheckAdapterNow(ctx)
	supported = false
	s.CheckAdapterNow(ctx)

	out := buf.String()
	if info, debug := strings.Count(out, `level=INFO msg="adapter check"`), strings.Count(out, `level=DEBUG msg="adapter check unchanged"`); info != 2 || debug != 1 {
		t.Fatalf("info=%d debug=%d\n%s", info, debug, out)
	}
}

// srvOf returns the Server behind a fixture's test server.
func srvOf(t *testing.T, ts *httptest.Server) *Server {
	t.Helper()
	s := serversByURL[ts.URL]
	if s == nil {
		t.Fatal("no Server registered for", ts.URL)
	}
	return s
}

func TestStatusCarriesServerInfo(t *testing.T) {
	// probes: first /status, the wake's own probe, the wake's wait, the last /status
	ts, store, _, _ := rpFixture(t, true, discovery.StateStandby, discovery.StateStandby, discovery.StateOn, discovery.StateOn)
	// The adapter check runs in the background on the first /status; force it
	// so the assertion does not race.
	srvOf(t, ts).CheckAdapterNow(context.Background())

	code, m := call(t, ts, "GET", "/status", "", nil, false)
	if code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	srv := m["server"].(map[string]any)
	if srv["version"] != "test" || srv["instance_id"] != store.Get().InstanceID {
		t.Fatalf("server block = %v", srv)
	}
	bt := srv["bluetooth"].(map[string]any)
	if bt["checked"] != true || bt["supported"] != true || bt["manufacturer"] != "Broadcom" || bt["configured"] != false {
		t.Fatalf("bluetooth = %v", bt)
	}
	rp := srv["remote_play"].(map[string]any)
	if rp["registered"] != true || rp["nickname"] != "PS5-123" {
		t.Fatalf("remote_play = %v", rp)
	}
	if srv["last_wake"] != nil || srv["waking"] != false {
		t.Fatalf("before any wake: %v", srv)
	}

	// a network wake leaves a record and a waking flag until discovery says on
	if code, m := call(t, ts, "POST", "/wake", "", nil, false); code != 200 {
		t.Fatalf("wake = %d %v", code, m)
	}
	_, m = call(t, ts, "GET", "/status", "", nil, false)
	srv = m["server"].(map[string]any)
	lw := srv["last_wake"].(map[string]any)
	if lw["method"] != "network" || lw["outcome"] != "success" {
		t.Fatalf("last_wake = %v", lw)
	}
	// discovery reported on, so the waking window is already closed
	if srv["waking"] != false {
		t.Fatalf("waking should clear once the console is on: %v", srv)
	}
	if _, err := time.Parse(time.RFC3339Nano, lw["at"].(string)); err != nil {
		t.Fatalf("at = %v", lw["at"])
	}
}

func TestStatusWakingFlag(t *testing.T) {
	ts, _, _, _ := rpFixture(t, true, discovery.StateStandby, discovery.StateOn, discovery.StateStandby, discovery.StateStandby)
	if code, _ := call(t, ts, "POST", "/wake", "", nil, false); code != 200 {
		t.Fatal("wake failed")
	}
	_, m := call(t, ts, "GET", "/status", "", nil, false)
	srv := m["server"].(map[string]any)
	if srv["waking"] != true || srv["waking_since"] == nil {
		t.Fatalf("waking should be reported while discovery still says standby: %v", srv)
	}
}

func TestStandbyRecord(t *testing.T) {
	ts, _, _, _ := rpFixture(t, true, discovery.StateOn, discovery.StateStandby, discovery.StateStandby)
	if code, _ := call(t, ts, "POST", "/standby", "", nil, false); code != 200 {
		t.Fatal("standby failed")
	}
	_, m := call(t, ts, "GET", "/status", "", nil, false)
	ls := m["server"].(map[string]any)["last_standby"].(map[string]any)
	if ls["outcome"] != "success" {
		t.Fatalf("last_standby = %v", ls)
	}
}
