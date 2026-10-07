package volgate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultConfigIsValid(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.MinExpectedMoveBps != DefaultThresholdBps {
		t.Fatalf("default threshold = %v", cfg.MinExpectedMoveBps)
	}
	if !cfg.FailClosed {
		t.Fatal("fail-closed must default to true: the gate exists to avoid a known loss mode")
	}
}

func TestWithDefaults(t *testing.T) {
	got := Config{}.WithDefaults()
	if got.Lookback != Duration(60*time.Minute) || got.DefaultWindow != Duration(15*time.Minute) || got.MinBars != 30 {
		t.Fatalf("WithDefaults = %+v", got)
	}
	if got.MaxAbsReturn != 0.5 {
		t.Fatalf("MaxAbsReturn = %v", got.MaxAbsReturn)
	}
}

func TestValidateRejectsZeroThresholds(t *testing.T) {
	cfg := Config{MinExpectedMoveBps: 0, MinSigmaPerMinuteBps: 0}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an error when both thresholds are zero")
	}
	if err := (Config{MinExpectedMoveBps: -1}).Validate(); err == nil {
		t.Fatal("expected an error for a negative threshold")
	}
}

func TestSymbolFor(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.SymbolFor("BTC"); got != "BTCUSDT" {
		t.Fatalf("SymbolFor(BTC) = %q", got)
	}
	if got := cfg.SymbolFor(" sol "); got != "SOLUSDT" {
		t.Fatalf("SymbolFor(sol) = %q", got)
	}
	if got := cfg.SymbolFor("xyz"); got != "XYZUSDT" {
		t.Fatalf("fallback = %q", got)
	}
	cfg.Assets = map[string]AssetConfig{"btc": {Symbol: "BTCUSDC"}}
	if got := cfg.SymbolFor("btc"); got != "BTCUSDC" {
		t.Fatalf("override = %q", got)
	}
}

func TestEffectiveOverrides(t *testing.T) {
	ten := 10.0
	win := Duration(5 * time.Minute)
	cfg := DefaultConfig()
	cfg.Assets = map[string]AssetConfig{"eth": {MinExpectedMoveBps: &ten, Window: &win}}
	base := cfg.Effective("btc")
	if base.MinExpectedMoveBps != DefaultThresholdBps || base.Window != Duration(15*time.Minute) {
		t.Fatalf("btc thresholds = %+v", base)
	}
	eth := cfg.Effective("ETH")
	if eth.MinExpectedMoveBps != 10 || eth.Window != Duration(5*time.Minute) {
		t.Fatalf("eth thresholds = %+v", eth)
	}
	if eth.MinSigmaPerMinuteBps != cfg.MinSigmaPerMinuteBps {
		t.Fatalf("unset field should inherit: %+v", eth)
	}
}

func TestApplyEnv(t *testing.T) {
	env := map[string]string{
		"VOLGATE_LOOKBACK":              "30m",
		"VOLGATE_MIN_EXPECTED_MOVE_BPS": "7.5",
		"VOLGATE_FAIL_CLOSED":           "false",
		"VOLGATE_BASE_URL":              "https://example.test",
		"VOLGATE_DEFAULT_WINDOW":        "5m",
		"VOLGATE_CACHE_DIR":             "/tmp/x",
		"VOLGATE_MIN_SIGMA_PER_MIN_BPS": "1.25",
		"VOLGATE_TIMEOUT":               "3s",
	}
	cfg, err := DefaultConfig().ApplyEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Lookback != Duration(30*time.Minute) || cfg.DefaultWindow != Duration(5*time.Minute) {
		t.Fatalf("durations = %+v", cfg)
	}
	if cfg.MinExpectedMoveBps != 7.5 || cfg.MinSigmaPerMinuteBps != 1.25 {
		t.Fatalf("thresholds = %+v", cfg)
	}
	if cfg.FailClosed || cfg.BaseURL != "https://example.test" || cfg.Timeout != Duration(3*time.Second) {
		t.Fatalf("cfg = %+v", cfg)
	}
	if _, err := DefaultConfig().ApplyEnv(func(k string) string {
		if k == "VOLGATE_LOOKBACK" {
			return "bogus"
		}
		return ""
	}); err == nil {
		t.Fatal("expected an error for a bad duration")
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	if err := os.WriteFile(good, []byte(`{"min_expected_move_bps":4.5,"default_window":"5m"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(good)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinExpectedMoveBps != 4.5 || cfg.DefaultWindow != Duration(5*time.Minute) {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.Lookback != Duration(60*time.Minute) {
		t.Fatalf("unset fields must keep defaults: %+v", cfg)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"min_expected_move_bs":4.5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(bad); err == nil {
		t.Fatal("expected an error for an unknown field (typo guard)")
	}
}

func TestConfigJSONShape(t *testing.T) {
	raw, err := json.Marshal(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Lookback != Duration(60*time.Minute) {
		t.Fatalf("round trip lost lookback: %s", raw)
	}
}
