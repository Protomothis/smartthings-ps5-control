package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/protomothis/smartthings-ps5-control/internal/btaddr"
)

func TestOpenCreatesDefaults(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Get()
	if len(got.InstanceID) != 16 {
		t.Fatalf("instance id = %q", got.InstanceID)
	}
	got.InstanceID = ""
	if got != Default() {
		t.Fatalf("got %+v", got)
	}
	// the id is persisted: reopening keeps it, and Set cannot change it
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Get().InstanceID != s.Get().InstanceID {
		t.Fatal("instance id changed on reopen")
	}
	c := s2.Get()
	c.InstanceID = "tampered"
	if err := s2.Set(c); err != nil || s2.Get().InstanceID != s.Get().InstanceID {
		t.Fatal("Set must keep the instance id")
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Fatal("config file not created:", err)
	}
	if err := s.Get().StatusReady(); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("StatusReady = %v", err)
	}
}

func TestSetAndReload(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Get()
	c.PS5IP = "192.168.1.20"
	c.ControllerAddr = btaddr.MustParse("aa:bb:cc:dd:ee:ff")
	c.PS5Addr = btaddr.MustParse("11:22:33:44:55:66")
	if err := s.Set(c); err != nil {
		t.Fatal(err)
	}
	if err := s.Get().WakeReady(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Get(); got.InstanceID == "" || got.PS5IP != c.PS5IP || got.ControllerAddr != c.ControllerAddr || got.PS5Addr != c.PS5Addr {
		t.Fatalf("reloaded %+v, want %+v", got, c)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName+".tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temp file left behind")
	}
}

func TestValidate(t *testing.T) {
	c := Default()
	c.HCI = "bluetooth0"
	if err := c.Validate(); err == nil {
		t.Fatal("bad hci accepted")
	}
	c = Default()
	c.WakeAttempts = 0
	if err := c.Validate(); err == nil {
		t.Fatal("zero attempts accepted")
	}
	c = Default()
	c.PS5IP = "999.1.1.1"
	if err := c.Validate(); err == nil {
		t.Fatal("bad ip accepted")
	}
	c = Default()
	c.PS5IP = "192.168.1.20"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPartialFileKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{"ps5_ip":"10.0.0.5"}`), 0o644)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Get()
	if c.PS5IP != "10.0.0.5" || c.HCI != "hci0" || c.WakeAttempts != 3 || c.InstanceID == "" {
		t.Fatalf("got %+v", c)
	}
	// an older file without an id got one minted and written back
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !strings.Contains(string(data), `"instance_id": "`+c.InstanceID+`"`) {
		t.Fatalf("instance id not persisted: %s", data)
	}
}
