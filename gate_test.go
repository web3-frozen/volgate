package volgate

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

// fakeSource serves a synthetic 1-minute series at a fixed volatility.
// sigmaBpsPerMin is the per-sqrt-minute volatility in bps.
type fakeSource struct {
	symbol         string
	startPrice     float64
	sigmaBpsPerMin float64
	err            error
	calls          int
	window         time.Duration
}

func (f *fakeSource) Bars(_ context.Context, symbol string, start, end time.Time) ([]Bar, error) {
	f.calls++
	f.symbol = symbol
	if f.err != nil {
		return nil, f.err
	}
	// Deterministic pseudo-random walk with the requested per-minute sigma.
	n := int(end.Sub(start)/time.Minute) + 1
	bars := make([]Bar, 0, n)
	price := f.startPrice
	if price == 0 {
		price = 100
	}
	step := f.sigmaBpsPerMin / Bps
	for i := 0; i < n; i++ {
		// Alternating direction keeps the realised stdev close to the target
		// while staying fully deterministic.
		dir := 1.0
		if i%2 == 1 {
			dir = -1.0
		}
		price *= math.Exp(dir * step)
		bars = append(bars, Bar{Time: start.Add(time.Duration(i) * time.Minute), Close: price})
	}
	f.window = end.Sub(start)
	return bars, nil
}

func testConfig() Config {
	cfg := DefaultConfig()
	cfg.CacheDir = "" // memory only
	return cfg
}

func TestGateAllowAndBlock(t *testing.T) {
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	// sigma 2 bps/min over 15m => expected move ~7.75 bps, above the 5 bps floor.
	gate := NewGate(&fakeSource{sigmaBpsPerMin: 2}, testConfig())
	v, err := gate.Check(context.Background(), Request{Asset: "btc", Window: 15 * time.Minute, At: at})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Allow {
		t.Fatalf("expected allow, reason=%q signal=%+v", v.Reason, v.Signal)
	}
	if math.Abs(v.Signal.ExpectedMoveBps-2*math.Sqrt(15)) > 0.5 {
		t.Fatalf("expected move = %v", v.Signal.ExpectedMoveBps)
	}

	// sigma 0.5 bps/min => ~1.9 bps, below the floor.
	gate = NewGate(&fakeSource{sigmaBpsPerMin: 0.5}, testConfig())
	v, err = gate.Check(context.Background(), Request{Asset: "btc", Window: 15 * time.Minute, At: at})
	if err != nil {
		t.Fatal(err)
	}
	if v.Allow {
		t.Fatalf("expected block, signal=%+v", v.Signal)
	}
	if v.Reason == "" || v.Reason == "ok" {
		t.Fatalf("blocked verdict needs an explanatory reason, got %q", v.Reason)
	}
}

func TestGateUsesDefaultWindow(t *testing.T) {
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	gate := NewGate(&fakeSource{sigmaBpsPerMin: 2}, testConfig())
	v, err := gate.Check(context.Background(), Request{Asset: "btc", At: at})
	if err != nil {
		t.Fatal(err)
	}
	if v.Signal.Window != Duration(15*time.Minute) {
		t.Fatalf("window = %v, want 15m", v.Signal.Window)
	}
}

func TestGateMapsAssetToSymbol(t *testing.T) {
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	src := &fakeSource{sigmaBpsPerMin: 2}
	gate := NewGate(src, testConfig())
	if _, err := gate.Check(context.Background(), Request{Asset: "eth", At: at}); err != nil {
		t.Fatal(err)
	}
	if src.symbol != "ETHUSDT" {
		t.Fatalf("source saw symbol %q", src.symbol)
	}
}

func TestGateSigmaFloor(t *testing.T) {
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.MinExpectedMoveBps = 0.1 // low bar
	cfg.MinSigmaPerMinuteBps = 3 // but raw volatility must be high
	gate := NewGate(&fakeSource{sigmaBpsPerMin: 2}, cfg)
	v, err := gate.Check(context.Background(), Request{Asset: "btc", Window: 15 * time.Minute, At: at})
	if err != nil {
		t.Fatal(err)
	}
	if v.Allow {
		t.Fatalf("expected the sigma floor to block, signal=%+v", v.Signal)
	}
}

func TestGatePerAssetOverride(t *testing.T) {
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	tight := 20.0
	cfg := testConfig()
	cfg.Assets = map[string]AssetConfig{"btc": {MinExpectedMoveBps: &tight}}
	gate := NewGate(&fakeSource{sigmaBpsPerMin: 2}, cfg)

	if v, _ := gate.Check(context.Background(), Request{Asset: "eth", Window: 15 * time.Minute, At: at}); !v.Allow {
		t.Fatalf("eth should pass the default threshold: %+v", v)
	}
	if v, _ := gate.Check(context.Background(), Request{Asset: "btc", Window: 15 * time.Minute, At: at}); v.Allow {
		t.Fatalf("btc should be blocked by its 20 bps override: %+v", v)
	}
}

func TestGateFailClosed(t *testing.T) {
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	gate := NewGate(&fakeSource{err: errors.New("upstream down")}, cfg)
	v := gate.Allow(context.Background(), Request{Asset: "btc", At: at})
	if v.Allow {
		t.Fatal("fail-closed must block when volatility is unavailable")
	}
	if _, err := gate.Check(context.Background(), Request{Asset: "btc", At: at}); err == nil {
		t.Fatal("Check should surface the underlying error")
	}

	cfg.FailClosed = false
	gate = NewGate(&fakeSource{err: errors.New("upstream down")}, cfg)
	if v := gate.Allow(context.Background(), Request{Asset: "btc", At: at}); !v.Allow {
		t.Fatal("fail-open must allow when volatility is unavailable")
	}
}

func TestGateMinBars(t *testing.T) {
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.Lookback = Duration(10 * time.Minute) // only ~10 returns, below MinBars=30
	gate := NewGate(&fakeSource{sigmaBpsPerMin: 5}, cfg)
	if _, err := gate.Check(context.Background(), Request{Asset: "btc", At: at}); err == nil {
		t.Fatal("expected an error when there are too few bars")
	}
	// And fail-closed turns that into a block, not a silent allow.
	if v := gate.Allow(context.Background(), Request{Asset: "btc", At: at}); v.Allow {
		t.Fatal("too few bars must not allow a trade")
	}
}

func TestGateRequiresAsset(t *testing.T) {
	gate := NewGate(&fakeSource{sigmaBpsPerMin: 2}, testConfig())
	if _, err := gate.Check(context.Background(), Request{}); err == nil {
		t.Fatal("expected an error when the asset is empty")
	}
}

func TestGateDoesNotBlockOnTrivialNoise(t *testing.T) {
	// A flat series has ~0 volatility and must be blocked, not crash.
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	gate := NewGate(&fakeSource{sigmaBpsPerMin: 0}, testConfig())
	v, err := gate.Check(context.Background(), Request{Asset: "btc", At: at})
	if err != nil {
		t.Fatalf("flat market should still produce a verdict: %v", err)
	}
	if v.Allow {
		t.Fatal("a flat market must not allow a trade")
	}
}
