package volgate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// DefaultBinanceBaseURL is the public spot REST endpoint used for bars.
const DefaultBinanceBaseURL = "https://api.binance.com"

// BinanceSource fetches 1-minute spot klines from the Binance REST API.
//
// Binance is used because it is the reference venue for many short-horizon
// crypto markets and because its 1-minute klines are freely paginated far back
// in time. Any other venue can be plugged in by implementing BarSource.
type BinanceSource struct {
	BaseURL string
	Client  *http.Client
	// MaxPages bounds pagination so a bad range cannot spin forever.
	MaxPages int
	// PageDelay is the pause between paginated requests, to stay well inside
	// public rate limits.
	PageDelay time.Duration
}

// NewBinanceSource returns a source with sane defaults.
func NewBinanceSource() *BinanceSource {
	return &BinanceSource{
		BaseURL:   DefaultBinanceBaseURL,
		Client:    &http.Client{Timeout: 20 * time.Second},
		MaxPages:  20,
		PageDelay: 80 * time.Millisecond,
	}
}

// Bars implements BarSource. Requests are paginated at 1000 bars per call.
func (b *BinanceSource) Bars(ctx context.Context, symbol string, start, end time.Time) ([]Bar, error) {
	base := b.BaseURL
	if base == "" {
		base = DefaultBinanceBaseURL
	}
	client := b.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	maxPages := b.MaxPages
	if maxPages <= 0 {
		maxPages = 20
	}

	startMs := start.UTC().UnixMilli()
	endMs := end.UTC().UnixMilli()
	if endMs <= startMs {
		return nil, fmt.Errorf("volgate: invalid range %s..%s", start, end)
	}

	var out []Bar
	cursor := startMs
	for page := 0; page < maxPages && cursor < endMs; page++ {
		q := url.Values{}
		q.Set("symbol", symbol)
		q.Set("interval", "1m")
		q.Set("startTime", strconv.FormatInt(cursor, 10))
		q.Set("endTime", strconv.FormatInt(endMs, 10))
		q.Set("limit", "1000")

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v3/klines?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "volgate/1.0")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("volgate: binance %s: %s", resp.Status, truncate(string(body), 200))
		}
		rows, err := parseKlines(body)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		out = append(out, rows...)
		last := rows[len(rows)-1].Time.UnixMilli()
		if last <= cursor {
			break
		}
		cursor = last + int64(Interval/time.Millisecond)
		if len(rows) < 1000 {
			break
		}
		if b.PageDelay > 0 {
			select {
			case <-time.After(b.PageDelay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return trimTo(out, start, end), nil
}

// parseKlines decodes Binance's array-of-arrays kline payload. Index 0 is the
// open time in milliseconds and index 4 is the close price.
func parseKlines(body []byte) ([]Bar, error) {
	var raw [][]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("volgate: decode klines: %w", err)
	}
	out := make([]Bar, 0, len(raw))
	for _, row := range raw {
		if len(row) < 5 {
			continue
		}
		var openMs int64
		var closeStr string
		if err := json.Unmarshal(row[0], &openMs); err != nil {
			continue
		}
		if err := json.Unmarshal(row[4], &closeStr); err != nil {
			continue
		}
		closePx, err := strconv.ParseFloat(closeStr, 64)
		if err != nil || closePx <= 0 {
			continue
		}
		out = append(out, Bar{Time: time.UnixMilli(openMs).UTC(), Close: closePx})
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
