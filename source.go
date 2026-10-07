package volgate

import (
	"context"
	"time"
)

// BarSource returns 1-minute bars for symbol covering [start, end].
//
// Implementations must return bars in ascending time order and only need to
// cover the requested interval. The result may be shorter than requested (e.g.
// the market was closed, or the range extends past "now").
type BarSource interface {
	Bars(ctx context.Context, symbol string, start, end time.Time) ([]Bar, error)
}

// SourceFunc adapts a function to the BarSource interface. Useful in tests and
// for wiring alternative venues without a type.
type SourceFunc func(ctx context.Context, symbol string, start, end time.Time) ([]Bar, error)

// Bars implements BarSource.
func (f SourceFunc) Bars(ctx context.Context, symbol string, start, end time.Time) ([]Bar, error) {
	return f(ctx, symbol, start, end)
}

// Interval is the bar spacing this package assumes (one minute). Sources that
// publish a different cadence must resample before returning.
const Interval = time.Minute

// trimTo returns the bars whose timestamps lie within [start, end].
func trimTo(bars []Bar, start, end time.Time) []Bar {
	out := bars[:0:0]
	for _, b := range bars {
		if b.Time.Before(start) || b.Time.After(end) {
			continue
		}
		out = append(out, b)
	}
	return out
}
