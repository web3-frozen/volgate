package volgate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const klineElem = `[%d,"100.0","101.0","99.0","%s","1.0",%d,"1.0",1,"1.0","1.0","0"]`

func TestParseKlines(t *testing.T) {
	body := []byte(`[[1790404200000,"100.0","101.0","99.0","100.5","1.0",1790404259999,"1","1","1","1","0"],
	                  [1790404260000,"100.5","102.0","100.0","101.5","1.0",1790404319999,"1","1","1","1","0"]]`)
	bars, err := parseKlines(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 {
		t.Fatalf("got %d bars", len(bars))
	}
	if bars[0].Close != 100.5 || bars[1].Close != 101.5 {
		t.Fatalf("closes = %v, %v", bars[0].Close, bars[1].Close)
	}
	if !bars[0].Time.Equal(time.UnixMilli(1790404200000).UTC()) {
		t.Fatalf("time = %v", bars[0].Time)
	}
}

func TestParseKlinesSkipsJunk(t *testing.T) {
	body := []byte(`[["x","1","1","1","1","1",1,"1",1,"1","1","0"],
	                 [1790404260000,"1","1","1","0","1",1,"1",1,"1","1","0"],
	                 [1790404320000,"1","1","1","12.5","1",1,"1",1,"1","1","0"]]`)
	bars, err := parseKlines(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 || bars[0].Close != 12.5 {
		t.Fatalf("bars = %+v", bars)
	}
}

func TestBinanceSourcePaginatesAndTrims(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		start, _ := strconv.ParseInt(q.Get("startTime"), 10, 64)
		limit, _ := strconv.ParseInt(q.Get("limit"), 10, 64)
		w.Header().Set("Content-Type", "application/json")
		var sb strings.Builder
		sb.WriteString("[")
		for i := int64(0); i < limit; i++ {
			if i > 0 {
				sb.WriteString(",")
			}
			ms := start + i*60_000
			fmt.Fprintf(&sb, klineElem, ms, "101.0", ms+59_999)
		}
		sb.WriteString("]")
		fmt.Fprint(w, sb.String())
	}))
	defer srv.Close()

	src := NewBinanceSource()
	src.BaseURL = srv.URL
	src.PageDelay = 0
	start := time.Unix(1_790_400_000, 0).UTC()
	end := start.Add(2500 * time.Minute) // forces multiple pages

	bars, err := src.Bars(context.Background(), "BTCUSDT", start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2501 {
		t.Fatalf("got %d bars, want 2501", len(bars))
	}
	if calls.Load() < 3 {
		t.Fatalf("expected pagination, got %d calls", calls.Load())
	}
	if !bars[0].Time.Equal(start) {
		t.Fatalf("first bar = %v, want %v", bars[0].Time, start)
	}
	if bars[len(bars)-1].Time.After(end) {
		t.Fatalf("last bar %v is past the requested end %v", bars[len(bars)-1].Time, end)
	}
}

func TestBinanceSourceHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":-1121,"msg":"Invalid symbol."}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	src := NewBinanceSource()
	src.BaseURL = srv.URL
	_, err := src.Bars(context.Background(), "NOPEUSDT", time.Unix(0, 0), time.Unix(600, 0))
	if err == nil || !strings.Contains(err.Error(), "Invalid symbol") {
		t.Fatalf("err = %v", err)
	}
}

func TestBinanceSourceRejectsBadRange(t *testing.T) {
	src := NewBinanceSource()
	if _, err := src.Bars(context.Background(), "BTCUSDT", time.Unix(600, 0), time.Unix(0, 0)); err == nil {
		t.Fatal("expected an error for an inverted range")
	}
}
