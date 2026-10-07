package volgate

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type countingSource struct {
	calls atomic.Int64
	bars  func(symbol string, start, end time.Time) []Bar
	err   error
}

func (c *countingSource) Bars(_ context.Context, symbol string, start, end time.Time) ([]Bar, error) {
	c.calls.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	return c.bars(symbol, start, end), nil
}

func hourlyDay(day time.Time) []Bar {
	out := make([]Bar, 0, 24)
	for i := 0; i < 24; i++ {
		out = append(out, Bar{Time: day.Add(time.Duration(i) * time.Hour), Close: 100 + float64(i)})
	}
	return out
}

func TestCacheReusesCompletedDays(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	src := &countingSource{bars: func(_ string, start, _ time.Time) []Bar { return hourlyDay(start) }}
	c := NewCachedSource(src, "", time.Minute)
	c.Now = func() time.Time { return now }

	start := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 8, 23, 59, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if _, err := c.Bars(context.Background(), "BTCUSDT", start, end); err != nil {
			t.Fatal(err)
		}
	}
	if got := src.calls.Load(); got != 1 {
		t.Fatalf("completed day fetched %d times, want 1", got)
	}
}

func TestCacheRefreshesCurrentDayAfterTTL(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	src := &countingSource{bars: func(_ string, start, _ time.Time) []Bar { return hourlyDay(start) }}
	c := NewCachedSource(src, "", 30*time.Second)
	clock := now
	c.Now = func() time.Time { return clock }

	start := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 10, 11, 59, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if _, err := c.Bars(context.Background(), "BTCUSDT", start, end); err != nil {
			t.Fatal(err)
		}
	}
	if got := src.calls.Load(); got != 1 {
		t.Fatalf("within TTL: fetched %d times, want 1", got)
	}
	clock = clock.Add(time.Minute)
	if _, err := c.Bars(context.Background(), "BTCUSDT", start, end); err != nil {
		t.Fatal(err)
	}
	if got := src.calls.Load(); got != 2 {
		t.Fatalf("after TTL: fetched %d times, want 2", got)
	}
}

func TestCacheServesStaleOnUpstreamFailure(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	clock := now
	failing := false
	src := &countingSource{bars: func(_ string, start, _ time.Time) []Bar { return hourlyDay(start) }}
	wrapped := SourceFunc(func(ctx context.Context, symbol string, start, end time.Time) ([]Bar, error) {
		if failing {
			return nil, errors.New("boom")
		}
		return src.Bars(ctx, symbol, start, end)
	})
	c := NewCachedSource(wrapped, "", 10*time.Second)
	c.Now = func() time.Time { return clock }

	start := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 10, 11, 59, 0, 0, time.UTC)
	if _, err := c.Bars(context.Background(), "BTCUSDT", start, end); err != nil {
		t.Fatal(err)
	}
	failing = true
	clock = clock.Add(time.Minute) // expire the current-day entry
	got, err := c.Bars(context.Background(), "BTCUSDT", start, end)
	if err != nil {
		t.Fatalf("stale fallback failed: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected stale bars")
	}
}

func TestCacheDiskRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	src := &countingSource{bars: func(_ string, start, _ time.Time) []Bar { return hourlyDay(start) }}

	c1 := NewCachedSource(src, dir, time.Minute)
	c1.Now = func() time.Time { return now }
	start := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 8, 23, 59, 0, 0, time.UTC)
	want, err := c1.Bars(context.Background(), "BTCUSDT", start, end)
	if err != nil {
		t.Fatal(err)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("expected one cache file, got %v", files)
	}

	// A fresh cache with a dead source must be able to serve from disk.
	src.err = errors.New("offline")
	c2 := NewCachedSource(src, dir, time.Minute)
	c2.Now = func() time.Time { return now }
	got, err := c2.Bars(context.Background(), "BTCUSDT", start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("disk restore returned %d bars, want %d", len(got), len(want))
	}
	if got[5].Close != want[5].Close {
		t.Fatalf("disk restore mismatch: %v vs %v", got[5], want[5])
	}
}

func TestCacheSplitsAcrossDays(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	src := &countingSource{bars: func(_ string, start, _ time.Time) []Bar { return hourlyDay(start) }}
	c := NewCachedSource(src, "", time.Minute)
	c.Now = func() time.Time { return now }

	start := time.Date(2026, 5, 8, 22, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 9, 2, 0, 0, 0, time.UTC)
	bars, err := c.Bars(context.Background(), "BTCUSDT", start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 5 {
		t.Fatalf("got %d bars, want 5 spanning the day boundary", len(bars))
	}
	for i := 1; i < len(bars); i++ {
		if !bars[i].Time.After(bars[i-1].Time) {
			t.Fatalf("bars not in ascending order at %d", i)
		}
	}
}

func TestCacheMemoryOnlyWhenDirEmpty(t *testing.T) {
	src := &countingSource{bars: func(_ string, start, _ time.Time) []Bar { return hourlyDay(start) }}
	c := NewCachedSource(src, "", time.Minute)
	if c.dir != "" {
		t.Fatalf("dir = %q, want empty", c.dir)
	}
}
