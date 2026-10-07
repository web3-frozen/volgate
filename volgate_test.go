package volgate

import (
	"math"
	"testing"
	"time"
)

func TestLogReturns(t *testing.T) {
	got := LogReturns([]float64{100, 110, 99})
	want := []float64{math.Log(1.1), math.Log(0.9)}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Fatalf("return[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestLogReturnsSkipsNonPositive(t *testing.T) {
	got := LogReturns([]float64{0, 100, 101})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
}

func TestStdev(t *testing.T) {
	if got := Stdev([]float64{2, 4, 4, 4, 5, 5, 7, 9}); math.Abs(got-2.138089935299395) > 1e-12 {
		t.Fatalf("Stdev = %v", got)
	}
	if got := Stdev([]float64{1}); got != 0 {
		t.Fatalf("Stdev(single) = %v, want 0", got)
	}
}

func TestMedian(t *testing.T) {
	for _, tc := range []struct {
		in   []float64
		want float64
	}{
		{[]float64{3, 1, 2}, 2},
		{[]float64{4, 1, 3, 2}, 2.5},
		{nil, 0},
	} {
		if got := Median(tc.in); got != tc.want {
			t.Fatalf("Median(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSigmaPerMinuteBasic(t *testing.T) {
	// Alternating +/-1 bps returns have a known sample stdev.
	rs := []float64{0.0001, -0.0001, 0.0001, -0.0001}
	got, err := SigmaPerMinute(rs, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := Stdev(rs) * 1e4
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("sigma = %v, want %v", got, want)
	}
}

// TestSigmaPerMinuteKeepsHeavyTails is a regression test. An earlier version
// clipped returns at 3 robust (MAD) sigmas. One-minute crypto returns are
// heavy-tailed, so that clip threw away real moves and reported roughly a third
// of the true volatility, which made the gate block almost everything.
func TestSigmaPerMinuteKeepsHeavyTails(t *testing.T) {
	rs := make([]float64, 0, 60)
	for i := 0; i < 59; i++ {
		rs = append(rs, []float64{0.00005, -0.00005}[i%2])
	}
	rs = append(rs, 0.006) // one 60 bps move
	got, err := SigmaPerMinute(rs, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	want := Stdev(rs) * 1e4
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("heavy tail was clipped: sigma = %v, want %v", got, want)
	}
	if got < 5 {
		t.Fatalf("sigma = %v bps, suspiciously low for a 60bps outlier", got)
	}
}

func TestSigmaPerMinuteDropsGarbage(t *testing.T) {
	rs := []float64{0.0001, -0.0001, 0.0001, -0.0001}
	withGarbage := append(append([]float64{}, rs...), 5.0) // 50000 bps "return"
	cleaned, err := SigmaPerMinute(withGarbage, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	clean, err := SigmaPerMinute(rs, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(cleaned-clean) > 1e-12 {
		t.Fatalf("garbage print was not dropped: %v vs %v", cleaned, clean)
	}
}

func TestSigmaPerMinuteErrors(t *testing.T) {
	if _, err := SigmaPerMinute([]float64{0.01}, 0); err != ErrNoData {
		t.Fatalf("err = %v, want ErrNoData", err)
	}
	if _, err := SigmaPerMinute([]float64{5, 5}, 0.5); err != ErrNoData {
		t.Fatalf("all-filtered err = %v, want ErrNoData", err)
	}
}

func TestExpectedMoveBps(t *testing.T) {
	if got := ExpectedMoveBps(2, 15*time.Minute); math.Abs(got-2*math.Sqrt(15)) > 1e-9 {
		t.Fatalf("ExpectedMoveBps = %v", got)
	}
	if got := ExpectedMoveBps(2, 0); got != 0 {
		t.Fatalf("zero window = %v, want 0", got)
	}
	if got := ExpectedMoveBps(0, time.Minute); got != 0 {
		t.Fatalf("zero sigma = %v, want 0", got)
	}
}

func TestExpectedAbsMoveShrinks(t *testing.T) {
	sig, win := 2.0, 15*time.Minute
	if ExpectedAbsMoveBps(sig, win) >= ExpectedMoveBps(sig, win) {
		t.Fatal("E|X| must be smaller than the 1-sigma move")
	}
}

func TestAnnualizedVolPct(t *testing.T) {
	if got := AnnualizedVolPct(0); got != 0 {
		t.Fatalf("zero sigma = %v", got)
	}
	// 1 bps per sqrt(minute) is about 7.25% annualised.
	if got := AnnualizedVolPct(1); math.Abs(got-7.249) > 0.01 {
		t.Fatalf("AnnualizedVolPct(1) = %v", got)
	}
}

func TestSigmaFromBars(t *testing.T) {
	bars := []Bar{
		{Time: time.Unix(0, 0), Close: 100},
		{Time: time.Unix(60, 0), Close: 101},
		{Time: time.Unix(120, 0), Close: 100},
	}
	got, err := SigmaFromBars(bars, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	want := Stdev(LogReturns(Closes(bars))) * 1e4
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("sigma = %v, want %v", got, want)
	}
}
