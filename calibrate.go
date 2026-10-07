package volgate

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Trade is one historical trade to calibrate against. Only Asset, EntryTS and
// either PnL or (Payout, Shares, Cost) are required; everything else is
// optional metadata used for richer reporting.
type Trade struct {
	Asset   string    `json:"asset"`
	Slug    string    `json:"slug,omitempty"`
	EntryTS time.Time `json:"entry_ts"`
	Window  Duration  `json:"window,omitempty"`
	// Payout is the number of legs that settled in the money (0 = both lost).
	Payout *float64 `json:"payout,omitempty"`
	Shares float64  `json:"shares,omitempty"`
	Cost   float64  `json:"cost,omitempty"`
	// PnL overrides Payout*Shares-Cost when present.
	PnL *float64 `json:"pnl,omitempty"`
	// Agreed records whether the venues resolved the same way. Nil = unknown.
	Agreed *bool `json:"agreed,omitempty"`
}

// PnLOf resolves the trade's PnL, preferring an explicit value.
func (t Trade) PnLOf() (float64, bool) {
	if t.PnL != nil {
		return *t.PnL, true
	}
	if t.Payout != nil && t.Shares != 0 {
		return *t.Payout*t.Shares - t.Cost, true
	}
	return 0, false
}

// LoadTrades reads newline-delimited JSON trades, skipping blank lines.
func LoadTrades(r io.Reader) ([]Trade, error) {
	var out []Trade
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		var t Trade
		if err := json.Unmarshal([]byte(s), &t); err != nil {
			return nil, fmt.Errorf("volgate: trades line %d: %w", line, err)
		}
		out = append(out, t)
	}
	return out, sc.Err()
}

// CalibratedTrade is a trade joined with its measured signal.
type CalibratedTrade struct {
	Trade
	Signal Signal  `json:"signal"`
	PnL    float64 `json:"pnl"`
	HasPnL bool    `json:"has_pnl"`
	Err    string  `json:"error,omitempty"`
}

// ThresholdStat summarises the sample at one candidate threshold.
type ThresholdStat struct {
	ThresholdBps         float64 `json:"threshold_bps"`
	Kept                 int     `json:"kept"`
	Excluded             int     `json:"excluded"`
	KeptPnL              float64 `json:"kept_pnl"`
	ExcludedPnL          float64 `json:"excluded_pnl"`
	KeptAvgPnL           float64 `json:"kept_avg_pnl"`
	KeptZeroPayout       int     `json:"kept_zero_payout"`
	ExcludedZeroPayout   int     `json:"excluded_zero_payout"`
	KeptDisagreeRate     float64 `json:"kept_disagree_rate"`
	ExcludedDisagreeRate float64 `json:"excluded_disagree_rate"`
}

// Report is the reproducible calibration output.
type Report struct {
	Trades      int               `json:"trades"`
	Measured    int               `json:"measured"`
	Failed      int               `json:"failed"`
	Window      Duration          `json:"window"`
	Lookback    Duration          `json:"lookback"`
	BaselinePnL float64           `json:"baseline_pnl"`
	Sweep       []ThresholdStat   `json:"sweep"`
	Rows        []CalibratedTrade `json:"rows,omitempty"`
}

// Calibrate measures every trade's signal at its entry time and sweeps
// candidate thresholds. thresholds are expected-move thresholds in bps; when
// empty a default ladder is used.
func Calibrate(ctx context.Context, g *Gate, trades []Trade, thresholds []float64) (Report, error) {
	if len(thresholds) == 0 {
		thresholds = []float64{1, 2, 3, 4, 5, 6, 7, 8, 10, 12}
	}
	sort.Float64s(thresholds)
	rep := Report{Trades: len(trades), Lookback: g.cfg.Lookback}
	var rows []CalibratedTrade
	for _, t := range trades {
		req := Request{Asset: t.Asset, Window: t.Window.Duration(), At: t.EntryTS}
		v, err := g.Check(ctx, req)
		pnl, hasPnL := t.PnLOf()
		row := CalibratedTrade{Trade: t, PnL: pnl, HasPnL: hasPnL}
		if err != nil {
			row.Err = err.Error()
			rep.Failed++
		} else {
			row.Signal = v.Signal
			rep.Measured++
		}
		rows = append(rows, row)
	}
	measurable := make([]CalibratedTrade, 0, len(rows))
	for _, r := range rows {
		if r.Err == "" {
			measurable = append(measurable, r)
		}
	}
	for _, r := range measurable {
		if r.HasPnL {
			rep.BaselinePnL += r.PnL
		}
	}
	if n := len(measurable); n > 0 {
		rep.Window = measurable[0].Signal.Window
	}
	rep.Rows = rows

	for _, th := range thresholds {
		stat := ThresholdStat{ThresholdBps: th}
		for _, r := range measurable {
			if r.Signal.ExpectedMoveBps >= th {
				stat.Kept++
				if r.HasPnL {
					stat.KeptPnL += r.PnL
				}
				if r.Trade.Payout != nil && *r.Trade.Payout == 0 {
					stat.KeptZeroPayout++
				}
				if r.Trade.Agreed != nil && !*r.Trade.Agreed {
					stat.KeptDisagreeRate++
				}
			} else {
				stat.Excluded++
				if r.HasPnL {
					stat.ExcludedPnL += r.PnL
				}
				if r.Trade.Payout != nil && *r.Trade.Payout == 0 {
					stat.ExcludedZeroPayout++
				}
				if r.Trade.Agreed != nil && !*r.Trade.Agreed {
					stat.ExcludedDisagreeRate++
				}
			}
		}
		if stat.Kept > 0 {
			stat.KeptAvgPnL = stat.KeptPnL / float64(stat.Kept)
			stat.KeptDisagreeRate = stat.KeptDisagreeRate / float64(stat.Kept)
		}
		if stat.Excluded > 0 {
			stat.ExcludedDisagreeRate = stat.ExcludedDisagreeRate / float64(stat.Excluded)
		}
		rep.Sweep = append(rep.Sweep, stat)
	}
	return rep, nil
}
