package volgate

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"15m", 15 * time.Minute, false},
		{"900s", 900 * time.Second, false},
		{"1h30m", 90 * time.Minute, false},
		{"900", 900 * time.Second, false},
		{"0", 0, false},
		{"nope", 0, true},
		{"", 0, true},
	} {
		got, err := ParseDuration(tc.in)
		if tc.err {
			if err == nil {
				t.Fatalf("ParseDuration(%q) expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseDuration(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseDuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestDurationJSONRoundTrip(t *testing.T) {
	type wrap struct {
		D Duration `json:"d"`
	}
	raw, err := json.Marshal(wrap{D: Duration(15 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"d":"15m0s"}` {
		t.Fatalf("marshal = %s", raw)
	}
	for _, in := range []string{`{"d":"15m"}`, `{"d":900}`, `{"d":"900s"}`} {
		var w wrap
		if err := json.Unmarshal([]byte(in), &w); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		if w.D.Duration() != 15*time.Minute {
			t.Fatalf("unmarshal %s = %v", in, w.D)
		}
	}
	var w wrap
	if err := json.Unmarshal([]byte(`{"d":null}`), &w); err != nil {
		t.Fatal(err)
	}
	if w.D != 0 {
		t.Fatalf("null = %v", w.D)
	}
	if err := json.Unmarshal([]byte(`{"d":"bogus"}`), &w); err == nil {
		t.Fatal("expected error for bogus duration")
	}
}
