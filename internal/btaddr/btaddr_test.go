package btaddr

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestParseAndString(t *testing.T) {
	for _, in := range []string{"aa:bb:cc:dd:ee:ff", "AA-BB-CC-DD-EE-FF", "aabbccddeeff", " AA:BB:CC:DD:EE:FF "} {
		a, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if got := a.String(); got != "AA:BB:CC:DD:EE:FF" {
			t.Fatalf("Parse(%q).String() = %q", in, got)
		}
	}
	for _, bad := range []string{"", "AA:BB:CC:DD:EE", "ZZ:BB:CC:DD:EE:FF", "AA:BB:CC:DD:EE:FF:00"} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("Parse(%q) should fail", bad)
		}
	}
}

func TestLERoundTrip(t *testing.T) {
	a := MustParse("01:02:03:04:05:06")
	le := a.LE()
	if !bytes.Equal(le, []byte{6, 5, 4, 3, 2, 1}) {
		t.Fatalf("LE() = %x", le)
	}
	if FromLE(le) != a {
		t.Fatalf("FromLE(LE()) = %s", FromLE(le))
	}
}

func TestJSON(t *testing.T) {
	type cfg struct {
		A Addr `json:"a"`
	}
	var c cfg
	if err := json.Unmarshal([]byte(`{"a":"aa:bb:cc:dd:ee:ff"}`), &c); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(c)
	if string(out) != `{"a":"AA:BB:CC:DD:EE:FF"}` {
		t.Fatalf("json = %s", out)
	}
	if err := json.Unmarshal([]byte(`{"a":""}`), &c); err != nil || !c.A.IsZero() {
		t.Fatalf("empty string should unmarshal to zero addr: %v %s", err, c.A)
	}
}
