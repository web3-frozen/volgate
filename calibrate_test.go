package volgate

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLoadTrades(t *testing.T) {
	in := `
{"asset":"btc","entry_ts":"2026-05-01T12:00:00Z","window":"15m","payout":1,"shares":10,"cost":9}

# a comment
{"asset":"eth","entry_ts":"2026-05-01T13:00:00Z","window":900,"pnl":-4.5,"agreed":false}
`
	trades, err := LoadTrades(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(trades) != 2 {
		t.Fatalf("got %d trades", len(trades))
	}
	if trades[0].Window.Duration() != 15*time.Minute {
		t.Fatalf("window = %v", trades[0].Window)
	}
	if pnl, ok := trades[0].PnLOf(); !ok || pnl != 1*10-9 {
		t.Fatalf("pnl = %v ok=%v", pnl, ok)
	}
	if trades[1].PnL == nil || *trades[1].PnL != -4.5 {
		t.Fatalf("explicit pnl lost: %+v", trades[1].PnL)
	}
	if trades[1].Agreed == nil || *trades[1].Agreed {
		t.Fatal("agreed flag lost")
	}
}

func TestLoadTradesRejectsBadJSON(t *testing.T) {
	if _, err := LoadTrades(strings.NewReader("{not json}\n")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestCalibrateSweep(t *testing.T) {
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	gate := NewGate(&fakeSource{sigmaBpsPerMin: 2}, testConfig()) // ~7.75 bps over 15m
	one, zero := 1.0, 0.0
	trades := []Trade{
		{Asset: "btc", EntryTS: at, Window: Duration(15 * time.Minute), Payout: &one, Shares: 10, Cost: 9},
		{Asset: "btc", EntryTS: at, Window: Duration(15 * time.Minute), Payout: &one, Shares: 10, Cost: 9},
		{Asset: "btc", EntryTS: at, Window: Duration(15 * time.Minute), Payout: &zero, Shares: 10, Cost: 9},
	}
	rep, err := Calibrate(context.Background(), gate, trades, []float64{5, 10})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Trades != 3 || rep.Measured != 3 || rep.Failed != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.BaselinePnL != (1.0*10-9)*2+(0.0*10-9) {
		t.Fatalf("baseline = %v", rep.BaselinePnL)
	}
	if len(rep.Sweep) != 2 {
		t.Fatalf("sweep length = %d", len(rep.Sweep))
	}
	// At 5 bps everything with ~7.75 bps passes; at 10 bps nothing does.
	if rep.Sweep[0].Kept != 3 || rep.Sweep[0].Excluded != 0 {
		t.Fatalf("5bps stat = %+v", rep.Sweep[0])
	}
	if rep.Sweep[1].Kept != 0 || rep.Sweep[1].Excluded != 3 {
		t.Fatalf("10bps stat = %+v", rep.Sweep[1])
	}
	if rep.Sweep[1].ExcludedPnL != rep.BaselinePnL {
		t.Fatalf("excluded pnl = %v, want %v", rep.Sweep[1].ExcludedPnL, rep.BaselinePnL)
	}
}

func TestCalibrateCountsDisagreementAndZeroPayout(t *testing.T) {
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	gate := NewGate(&fakeSource{sigmaBpsPerMin: 0.5}, testConfig()) // ~1.9 bps: below 5
	no := false
	zero := 0.0
	trades := []Trade{{
		Asset: "btc", EntryTS: at, Window: Duration(15 * time.Minute),
		Payout: &zero, Shares: 10, Cost: 9, Agreed: &no,
	}}
	rep, err := Calibrate(context.Background(), gate, trades, []float64{5})
	if err != nil {
		t.Fatal(err)
	}
	st := rep.Sweep[0]
	if st.ExcludedZeroPayout != 1 || st.ExcludedDisagreeRate != 1 {
		t.Fatalf("stat = %+v", st)
	}
	if st.Kept != 0 {
		t.Fatalf("kept = %d, want 0", st.Kept)
	}
}

func TestCalibrateReportsMeasurementFailures(t *testing.T) {
	gate := NewGate(&fakeSource{err: context.DeadlineExceeded}, testConfig())
	trades := []Trade{{Asset: "btc", EntryTS: time.Now(), Window: Duration(15 * time.Minute)}}
	rep, err := Calibrate(context.Background(), gate, trades, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed != 1 || rep.Measured != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Rows[0].Err == "" {
		t.Fatal("the per-trade error should be recorded")
	}
	if len(rep.Sweep) == 0 {
		t.Fatal("sweep should still be produced (all excluded)")
	}
}
