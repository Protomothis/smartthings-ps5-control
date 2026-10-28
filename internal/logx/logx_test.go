package logx

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandlerFormatsAndRecords(t *testing.T) {
	var stderr bytes.Buffer
	now := time.Date(2026, 10, 21, 15, 4, 5, 123000000, time.UTC)
	h := New(Options{Stderr: &stderr, RingSize: 3, Now: func() time.Time { return now }})
	log := h.Logger()
	log.With(ComponentKey, "wake").Info("network wake", "state", "standby")
	log.Warn("plain line")
	log.Debug("dropped at info level")
	log.WithGroup("rp").Error("bad thing", "code", "80108b09", "text", "two words")

	entries, next := h.Entries(0, 0)
	if next != 3 || len(entries) != 3 {
		t.Fatalf("next=%d entries=%d", next, len(entries))
	}
	if e := entries[0]; e.Component != "wake" || e.Level != "INFO" || e.Message != "network wake" || e.Attrs != "state=standby" {
		t.Fatalf("entry 0 = %+v", e)
	}
	if e := entries[2]; e.Level != "ERROR" || e.Attrs != `rp.code=80108b09 rp.text="two words"` {
		t.Fatalf("entry 2 = %+v", e)
	}
	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "2026-10-21 15:04:05.123 INFO  wake: network wake state=standby") {
		t.Fatalf("stderr = %q", stderr.String())
	}

	// ring wraps: a fourth entry pushes the first out
	log.Info("fourth")
	entries, next = h.Entries(0, 0)
	if next != 4 || len(entries) != 3 || entries[0].Seq != 2 {
		t.Fatalf("after wrap: next=%d entries=%+v", next, entries)
	}
	// after/limit
	entries, _ = h.Entries(3, 0)
	if len(entries) != 1 || entries[0].Message != "fourth" {
		t.Fatalf("after=3: %+v", entries)
	}
	entries, _ = h.Entries(0, 2)
	if len(entries) != 2 || entries[0].Seq != 3 {
		t.Fatalf("limit=2 should keep the newest: %+v", entries)
	}
}

func TestLevelOption(t *testing.T) {
	h := New(Options{Level: slog.LevelDebug, RingSize: 10})
	h.Logger().Debug("kept")
	if e, _ := h.Entries(0, 0); len(e) != 1 || e[0].Level != "DEBUG" {
		t.Fatalf("%+v", e)
	}
	if _, err := ParseLevel("warning"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Fatal("bad level accepted")
	}
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "ps5ctl.log")
	rf, err := OpenRotating(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 39) + "\n" // 40 bytes
	for i := 0; i < 12; i++ {              // 480 bytes: rotations at 120, 240, 360, 480
		if _, err := rf.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	files := rf.Files()
	if len(files) != 3 || !strings.HasSuffix(files[0], ".2") || !strings.HasSuffix(files[1], ".1") || files[2] != path {
		t.Fatalf("files = %v", files)
	}
	var total int64
	for _, f := range files {
		st, _ := os.Stat(f)
		if st.Size() > 100 {
			t.Fatalf("%s is %d bytes", f, st.Size())
		}
		total += st.Size()
	}
	if total > 300 {
		t.Fatalf("total %d exceeds the cap", total)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatal("a third backup exists")
	}
	var dump bytes.Buffer
	if err := rf.Dump(&dump); err != nil {
		t.Fatal(err)
	}
	if int64(dump.Len()) != total {
		t.Fatalf("dump %d bytes, files %d", dump.Len(), total)
	}
	// a write larger than the cap still lands whole
	big := strings.Repeat("y", 150)
	if n, err := rf.Write([]byte(big)); err != nil || n != 150 {
		t.Fatalf("big write n=%d err=%v", n, err)
	}
	if err := rf.Close(); err != nil {
		t.Fatal(err)
	}
	// reopening continues the same file and knows its size
	rf2, err := OpenRotating(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer rf2.Close()
	if rf2.size != 150 {
		t.Fatalf("reopened size = %d", rf2.size)
	}
}

func TestHubWritesFileAndDump(t *testing.T) {
	dir := t.TempDir()
	rf, err := OpenRotating(filepath.Join(dir, "a.log"), 1<<20, 1)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Options{File: rf})
	h.Logger().Info("hello", ComponentKey, "server")
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.log"))
	if !strings.Contains(string(data), "INFO  server: hello") {
		t.Fatalf("file = %q", data)
	}
	if err := New(Options{}).Dump(&bytes.Buffer{}); err == nil {
		t.Fatal("dump without a file should fail")
	}
}
