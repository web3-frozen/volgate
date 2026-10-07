// Package volgate turns short-horizon realized volatility into a single,
// comparable number — the expected magnitude of the price move over a
// prediction market's resolution window — and gates trading on it.
//
// # Why this exists
//
// Short-horizon "up or down" prediction markets on different venues often use
// different reference prices to settle (for example a Chainlink TWAP versus a
// spot exchange price). When the underlying barely moves over the resolution
// window, that measurement difference — not the market's directional view —
// decides the outcome, and the two venues can resolve in opposite directions.
// A bet that needs the venues to agree loses on both legs in that regime.
// Realized volatility is the ex-ante proxy for "how big is the move likely to
// be relative to that measurement difference", so this package exposes it as
// an entry filter.
//
// # Units
//
// Everything is expressed in basis points (bps; 1e-4 = 0.01%):
//
//   - SigmaPerMinute  volatility of 1-minute log returns, bps per sqrt(minute)
//   - ExpectedMoveBps SigmaPerMinute * sqrt(window minutes)
//
// ExpectedMoveBps is the one-standard-deviation expected absolute log move over
// the window, which is directly comparable to the bps-scale difference between
// two venues' settlement sources.
package volgate

import (
	"errors"
	"math"
	"sort"
	"time"
)

// Bps is one basis point in decimal log-return units (1e-4 = 0.01%).
const Bps = 1e4

// MinutesPerYear is used to annualise the per-sqrt-minute volatility.
const MinutesPerYear = 365.0 * 24.0 * 60.0

// ErrNoData is returned when a series is too short to estimate volatility.
var ErrNoData = errors.New("volgate: not enough bars to estimate volatility")

// Bar is a single close-to-close sample. Time must be monotonically increasing
// and evenly spaced by the source's interval (one minute for the default
// source).
type Bar struct {
	Time  time.Time
	Close float64
}

// Closes extracts the close prices from a bar series.
func Closes(bars []Bar) []float64 {
	out := make([]float64, 0, len(bars))
	for _, b := range bars {
		out = append(out, b.Close)
	}
	return out
}

// LogReturns converts a price series into log returns. Non-positive prices are
// skipped, so the result can be shorter than the input.
func LogReturns(prices []float64) []float64 {
	out := make([]float64, 0, len(prices))
	for i := 1; i < len(prices); i++ {
		if prices[i-1] <= 0 || prices[i] <= 0 {
			continue
		}
		out = append(out, math.Log(prices[i]/prices[i-1]))
	}
	return out
}

// Stdev is the sample standard deviation of a series (n-1 denominator).
func Stdev(xs []float64) float64 {
	n := len(xs)
	if n < 2 {
		return 0
	}
	mean := 0.0
	for _, x := range xs {
		mean += x
	}
	mean /= float64(n)
	sum := 0.0
	for _, x := range xs {
		d := x - mean
		sum += d * d
	}
	return math.Sqrt(sum / float64(n-1))
}

// Median returns the median of a series. It does not modify the input.
func Median(xs []float64) float64 {
	n := len(xs)
	if n == 0 {
		return 0
	}
	cp := make([]float64, n)
	copy(cp, xs)
	sort.Float64s(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return 0.5 * (cp[n/2-1] + cp[n/2])
}

// SigmaPerMinute estimates the volatility of 1-minute log returns in basis
// points per sqrt(minute).
//
// maxAbsReturn is a sanity bound: returns larger than it (in absolute value)
// are dropped before estimating. Exchange data occasionally contains a stale or
// crossed print whose one-minute "return" is pure garbage, and a plain standard
// deviation is very sensitive to those. The bound must stay generous though —
// 1-minute crypto returns are genuinely heavy-tailed, and tighter or
// distribution-relative bounds (for example a MAD-based clip) discard real moves
// and bias the estimate far too low. Pass 0 to keep every return.
func SigmaPerMinute(returns []float64, maxAbsReturn float64) (float64, error) {
	if len(returns) < 2 {
		return 0, ErrNoData
	}
	filtered := returns
	if maxAbsReturn > 0 {
		filtered = returns[:0:0]
		for _, r := range returns {
			if math.Abs(r) <= maxAbsReturn {
				filtered = append(filtered, r)
			}
		}
	}
	if len(filtered) < 2 {
		return 0, ErrNoData
	}
	return Stdev(filtered) * Bps, nil
}

// SigmaFromBars is a convenience wrapper: it derives log returns from a bar
// series and estimates the per-sqrt-minute volatility.
func SigmaFromBars(bars []Bar, maxAbsReturn float64) (float64, error) {
	return SigmaPerMinute(LogReturns(Closes(bars)), maxAbsReturn)
}

// ExpectedMoveBps is the 1-sigma expected absolute log move over window, in
// basis points, given a per-sqrt-minute volatility.
func ExpectedMoveBps(sigmaPerMinute float64, window time.Duration) float64 {
	if sigmaPerMinute <= 0 || window <= 0 {
		return 0
	}
	return sigmaPerMinute * math.Sqrt(window.Minutes())
}

// ExpectedAbsMoveBps is E|X| = sigma*sqrt(2/pi) for a zero-mean normal X. It is
// the more literal "expected magnitude" and slightly smaller than the 1-sigma
// move; the gate uses the 1-sigma form because that is the conventional risk
// unit. Exposed for reporting.
func ExpectedAbsMoveBps(sigmaPerMinute float64, window time.Duration) float64 {
	return ExpectedMoveBps(sigmaPerMinute, window) * math.Sqrt(2/math.Pi)
}

// AnnualizedVolPct converts a per-sqrt-minute volatility to an annualised
// volatility percentage (e.g. 55.0 for 55%).
func AnnualizedVolPct(sigmaPerMinute float64) float64 {
	if sigmaPerMinute <= 0 {
		return 0
	}
	return sigmaPerMinute * math.Sqrt(MinutesPerYear) / 100.0
}
