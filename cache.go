package volgate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CachedSource wraps a BarSource with a per-UTC-day cache.
//
// Volatility estimation reads the same recent bars on every check, so the
// underlying HTTP source must not be hit each time. Days that are already over
// can never change, so they are cached for the lifetime of the process and, if
// a directory is configured, on disk as well. The current day is re-fetched
// after ttl so the newest bars are picked up.
type CachedSource struct {
	src BarSource
	dir string
	ttl time.Duration
	// Now is the clock used to decide whether a day is complete. Exported so
	// tests can make the current-day refresh behaviour deterministic.
	Now     func() time.Time
	mu      sync.Mutex
	entries map[string]*dayEntry
}

type dayEntry struct {
	bars    []Bar
	fetched time.Time
	// sealed days are complete and never refetched.
	sealed bool
}

// NewCachedSource wraps src. dir may be empty to use memory only.
func NewCachedSource(src BarSource, dir string, ttl time.Duration) *CachedSource {
	if ttl <= 0 {
		ttl = time.Minute
	}
	return &CachedSource{
		src:     src,
		dir:     dir,
		ttl:     ttl,
		Now:     time.Now,
		entries: make(map[string]*dayEntry),
	}
}

// Bars implements BarSource, assembling the requested range from day buckets.
func (c *CachedSource) Bars(ctx context.Context, symbol string, start, end time.Time) ([]Bar, error) {
	now := c.Now().UTC()
	first := dayStart(start.UTC())
	last := dayStart(end.UTC())

	var out []Bar
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		bars, err := c.dayBars(ctx, symbol, day, now)
		if err != nil {
			return nil, err
		}
		out = append(out, bars...)
	}
	return trimTo(out, start.UTC(), end.UTC()), nil
}

func (c *CachedSource) dayBars(ctx context.Context, symbol string, day, now time.Time) ([]Bar, error) {
	key := symbol + "|" + day.Format("2006-01-02")
	sealed := day.AddDate(0, 0, 1).Before(now)

	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		fresh := e.sealed || now.Sub(e.fetched) < c.ttl
		if fresh {
			c.mu.Unlock()
			return e.bars, nil
		}
	}
	c.mu.Unlock()

	if e := c.readDisk(key); e != nil && (e.sealed || now.Sub(e.fetched) < c.ttl) {
		c.mu.Lock()
		c.entries[key] = e
		c.mu.Unlock()
		return e.bars, nil
	}

	bars, err := c.src.Bars(ctx, symbol, day, day.AddDate(0, 0, 1))
	if err != nil {
		// Fall back to a stale cache entry rather than failing the check.
		c.mu.Lock()
		stale := c.entries[key]
		c.mu.Unlock()
		if stale != nil {
			return stale.bars, nil
		}
		if e := c.readDisk(key); e != nil {
			return e.bars, nil
		}
		return nil, err
	}

	e := &dayEntry{bars: bars, fetched: now, sealed: sealed}
	c.mu.Lock()
	c.entries[key] = e
	c.mu.Unlock()
	c.writeDisk(key, e)
	return bars, nil
}

func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// disk payload: [unix-seconds, close] pairs, compact enough for a day of 1m bars.
type diskPayload struct {
	Fetched int64        `json:"fetched"`
	Sealed  bool         `json:"sealed"`
	Bars    [][2]float64 `json:"bars"`
}

func (c *CachedSource) path(key string) string {
	if c.dir == "" {
		return ""
	}
	safe := strings.NewReplacer("|", "_", "/", "_", "\\", "_").Replace(key)
	return filepath.Join(c.dir, safe+".json")
}

func (c *CachedSource) readDisk(key string) *dayEntry {
	p := c.path(key)
	if p == "" {
		return nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var d diskPayload
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil
	}
	bars := make([]Bar, 0, len(d.Bars))
	for _, b := range d.Bars {
		bars = append(bars, Bar{Time: time.Unix(int64(b[0]), 0).UTC(), Close: b[1]})
	}
	return &dayEntry{bars: bars, fetched: time.Unix(d.Fetched, 0), sealed: d.Sealed}
}

func (c *CachedSource) writeDisk(key string, e *dayEntry) {
	p := c.path(key)
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	d := diskPayload{Fetched: e.fetched.Unix(), Sealed: e.sealed, Bars: make([][2]float64, 0, len(e.bars))}
	for _, b := range e.bars {
		d.Bars = append(d.Bars, [2]float64{float64(b.Time.Unix()), b.Close})
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return
	}
	tmp := fmt.Sprintf("%s.tmp%d", p, os.Getpid())
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}
