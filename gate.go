package volgate

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Request describes one gating decision.
type Request struct {
	// Asset is the market's asset code, e.g. "btc".
	Asset string
	// Window is the market's resolution window. Zero falls back to the
	// configured default.
	Window time.Duration
	// At is the moment to evaluate at. Zero means "now". Historical values
	// are supported as long as the source can serve bars that far back.
	At time.Time
}

// Signal is the volatility measurement behind a decision.
type Signal struct {
	Asset              string    `json:"asset"`
	Symbol             string    `json:"symbol"`
	At                 time.Time `json:"at"`
	Lookback           Duration  `json:"lookback"`
	Window             Duration  `json:"window"`
	Bars               int       `json:"bars"`
	Returns            int       `json:"returns"`
	SigmaPerMinuteBps  float64   `json:"sigma_per_minute_bps"`
	ExpectedMoveBps    float64   `json:"expected_move_bps"`
	ExpectedAbsMoveBps float64   `json:"expected_abs_move_bps"`
	AnnualizedVolPct   float64   `json:"annualized_vol_pct"`
	LastPrice          float64   `json:"last_price"`
}

// Verdict is the gate's answer.
type Verdict struct {
	Allow      bool       `json:"allow"`
	Reason     string     `json:"reason"`
	Signal     Signal     `json:"signal"`
	Thresholds Thresholds `json:"thresholds"`
}

// Gate combines a price source with a Config to answer "is the underlying
// volatile enough for this market's window to be worth trading?".
type Gate struct {
	src BarSource
	cfg Config
	now func() time.Time
}

// NewGate builds a Gate. src is wrapped in a CachedSource when the
// configuration enables caching.
func NewGate(src BarSource, cfg Config) *Gate {
	cfg = cfg.WithDefaults()
	return &Gate{
		src: NewCachedSource(src, cfg.CacheDir, cfg.CacheTTL.Duration()),
		cfg: cfg,
		now: time.Now,
	}
}

// Config returns the effective configuration.
func (g *Gate) Config() Config { return g.cfg }

// SetClock overrides the clock used to resolve Request.At == zero. Intended
// for tests.
func (g *Gate) SetClock(fn func() time.Time) {
	if fn != nil {
		g.now = fn
	}
}

// Check evaluates the request and returns the raw measurement plus the
// decision. A non-nil error means volatility could not be estimated.
func (g *Gate) Check(ctx context.Context, req Request) (Verdict, error) {
	sig, err := g.Measure(ctx, req)
	if err != nil {
		return Verdict{}, err
	}
	th := g.cfg.Effective(req.Asset)
	v := Verdict{Allow: true, Reason: "ok", Signal: sig, Thresholds: th}
	if th.MinSigmaPerMinuteBps > 0 && sig.SigmaPerMinuteBps < th.MinSigmaPerMinuteBps {
		v.Allow = false
		v.Reason = fmt.Sprintf("sigma %.2f bps/min < min %.2f", sig.SigmaPerMinuteBps, th.MinSigmaPerMinuteBps)
		return v, nil
	}
	if sig.ExpectedMoveBps < th.MinExpectedMoveBps {
		v.Allow = false
		v.Reason = fmt.Sprintf("expected %.2f bps move over %s < min %.2f bps",
			sig.ExpectedMoveBps, sig.Window, th.MinExpectedMoveBps)
		return v, nil
	}
	return v, nil
}

// Allow never fails: when the measurement cannot be taken it returns a verdict
// according to the FailClosed setting. Use Check when you want the error.
func (g *Gate) Allow(ctx context.Context, req Request) Verdict {
	v, err := g.Check(ctx, req)
	if err == nil {
		return v
	}
	th := g.cfg.Effective(req.Asset)
	window := req.Window
	if window <= 0 {
		window = th.Window.Duration()
	}
	return Verdict{
		Allow:      !g.cfg.FailClosed,
		Reason:     "volatility unavailable: " + err.Error(),
		Signal:     Signal{Asset: req.Asset, Symbol: g.cfg.SymbolFor(req.Asset), Window: Duration(window)},
		Thresholds: th,
	}
}

// Measure computes the volatility signal without applying thresholds.
func (g *Gate) Measure(ctx context.Context, req Request) (Signal, error) {
	asset := normalizeAsset(req.Asset)
	if asset == "" {
		return Signal{}, errors.New("volgate: asset is required")
	}
	symbol := g.cfg.SymbolFor(asset)
	if symbol == "" {
		return Signal{}, fmt.Errorf("volgate: no symbol configured for asset %q", req.Asset)
	}
	th := g.cfg.Effective(asset)
	window := req.Window
	if window <= 0 {
		window = th.Window.Duration()
	}
	now := req.At
	if now.IsZero() {
		now = g.now()
	}
	now = now.UTC()

	ctx, cancel := context.WithTimeout(ctx, g.cfg.Timeout.Duration())
	defer cancel()

	bars, err := g.src.Bars(ctx, symbol, now.Add(-g.cfg.Lookback.Duration()), now)
	if err != nil {
		return Signal{}, err
	}
	prices := Closes(bars)
	returns := LogReturns(prices)
	sig := Signal{
		Asset:    asset,
		Symbol:   symbol,
		At:       now,
		Lookback: g.cfg.Lookback,
		Window:   Duration(window),
		Bars:     len(bars),
		Returns:  len(returns),
	}
	if len(bars) > 0 {
		sig.LastPrice = bars[len(bars)-1].Close
	}
	if len(returns) < g.cfg.MinBars {
		return sig, fmt.Errorf("%w: got %d returns, need %d", ErrNoData, len(returns), g.cfg.MinBars)
	}
	sigma, err := SigmaPerMinute(returns, g.cfg.MaxAbsReturn)
	if err != nil {
		return sig, err
	}
	sig.SigmaPerMinuteBps = round(sigma, 4)
	sig.ExpectedMoveBps = round(ExpectedMoveBps(sigma, window), 4)
	sig.ExpectedAbsMoveBps = round(ExpectedAbsMoveBps(sigma, window), 4)
	sig.AnnualizedVolPct = round(AnnualizedVolPct(sigma), 3)
	return sig, nil
}

func normalizeAsset(a string) string {
	s := ""
	for _, r := range a {
		switch {
		case r >= 'A' && r <= 'Z':
			s += string(r + 32)
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			s += string(r)
		}
	}
	return s
}

func round(v float64, places int) float64 {
	pow := 1.0
	for i := 0; i < places; i++ {
		pow *= 10
	}
	return float64(int64(v*pow+sign(v)*0.5)) / pow
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}
