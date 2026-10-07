// Command volgate measures short-horizon realized volatility and decides
// whether a market's resolution window is volatile enough to trade.
//
// Usage:
//
//	volgate check     -asset btc -window 15m [-json] [-exit]
//	volgate measure   -asset btc [-lookback 1h] [-window 15m] [-json]
//	volgate calibrate -trades trades.jsonl [-thresholds 3,4,5,6] [-json]
//	volgate serve     [-addr 127.0.0.1:8791] [-config volgate.json]
//	volgate config    [-json]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/web3-frozen/volgate"
)

// version is overridable at build time:
//
//	go build -ldflags "-X main.version=v0.1.0" ./cmd/volgate
//
// When it is not set (for example `go install ...@v0.1.0`), effectiveVersion
// falls back to the module version recorded in the build info.
var version = "dev"

// effectiveVersion reports the injected version, or the module version the
// binary was built from.
func effectiveVersion() string {
	if version != "dev" && version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	var blocked bool
	switch cmd {
	case "check":
		blocked, err = runCheck(args)
	case "measure", "vol":
		err = runMeasure(args)
	case "calibrate":
		err = runCalibrate(args)
	case "serve":
		err = runServe(args)
	case "config":
		err = runConfig(args)
	case "version", "--version", "-v":
		fmt.Println(effectiveVersion())
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "volgate: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "volgate: %v\n", err)
		os.Exit(2)
	}
	if blocked {
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `volgate — realized-volatility gate for short-horizon markets

  volgate check     -asset btc -window 15m [-json] [-exit] [-at RFC3339]
  volgate measure   -asset btc [-lookback 1h] [-window 15m] [-json]
  volgate calibrate -trades trades.jsonl [-thresholds 3,4,5,6] [-json] [-out FILE]
  volgate serve     [-addr 127.0.0.1:8791] [-config FILE]
  volgate config    [-json]

Configuration flags (also honoured as VOLGATE_* environment variables):
  -config FILE                 JSON config file
  -lookback DURATION           trailing window for volatility (default 60m)
  -window DURATION             market resolution window (default 15m)
  -min-expected-move-bps N     primary threshold (default 5)
  -min-sigma-per-min-bps N     optional raw volatility floor (default 0 = off)
  -base-url URL                price source base URL
  -cache-dir DIR               bar cache directory ("" = memory only)
  -timeout DURATION            per-check timeout
`)
}

type commonFlags struct {
	configFile string
	lookback   string
	window     string
	minMove    float64
	minSigma   float64
	baseURL    string
	cacheDir   string
	timeout    string
	failClosed string
}

func bindCommon(fs *flag.FlagSet, cf *commonFlags) {
	fs.StringVar(&cf.configFile, "config", "", "JSON config file")
	fs.StringVar(&cf.lookback, "lookback", "", "trailing window for volatility (e.g. 60m)")
	fs.StringVar(&cf.window, "window", "", "market resolution window (e.g. 15m)")
	fs.Float64Var(&cf.minMove, "min-expected-move-bps", -1, "minimum expected move over the window, bps")
	fs.Float64Var(&cf.minSigma, "min-sigma-per-min-bps", -1, "minimum raw volatility, bps per sqrt(minute)")
	fs.StringVar(&cf.baseURL, "base-url", "", "price source base URL")
	fs.StringVar(&cf.cacheDir, "cache-dir", "", "bar cache directory")
	fs.StringVar(&cf.timeout, "timeout", "", "per-check timeout (e.g. 20s)")
	fs.StringVar(&cf.failClosed, "fail-closed", "", "block when volatility is unavailable (true/false)")
}

// buildConfig merges file -> env -> flags (later wins).
func buildConfig(cf commonFlags) (volgate.Config, error) {
	cfg := volgate.DefaultConfig()
	if cf.configFile != "" {
		loaded, err := volgate.LoadConfig(cf.configFile)
		if err != nil {
			return cfg, err
		}
		cfg = loaded
	}
	cfg, err := cfg.ApplyEnv(os.Getenv)
	if err != nil {
		return cfg, err
	}
	return cfg, err
}

func newGate(cf commonFlags, fs *flag.FlagSet) (*volgate.Gate, volgate.Config, error) {
	cfg, err := buildConfig(cf)
	if err != nil {
		return nil, cfg, err
	}
	// Re-run flag application now that we know it was requested; buildConfig
	// applies flags inside its own Visit call.
	cfg, err = applyFlags(cf, fs, cfg)
	if err != nil {
		return nil, cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, cfg, err
	}
	src := volgate.NewBinanceSource()
	if cfg.BaseURL != "" {
		src.BaseURL = cfg.BaseURL
	}
	return volgate.NewGate(src, cfg), cfg, nil
}

func applyFlags(cf commonFlags, fs *flag.FlagSet, cfg volgate.Config) (volgate.Config, error) {
	var err error
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "lookback":
			var d time.Duration
			if d, err = volgate.ParseDuration(cf.lookback); err == nil {
				cfg.Lookback = volgate.Duration(d)
			}
		case "window":
			var d time.Duration
			if d, err = volgate.ParseDuration(cf.window); err == nil {
				cfg.DefaultWindow = volgate.Duration(d)
			}
		case "timeout":
			var d time.Duration
			if d, err = volgate.ParseDuration(cf.timeout); err == nil {
				cfg.Timeout = volgate.Duration(d)
			}
		case "min-expected-move-bps":
			cfg.MinExpectedMoveBps = cf.minMove
		case "min-sigma-per-min-bps":
			cfg.MinSigmaPerMinuteBps = cf.minSigma
		case "base-url":
			cfg.BaseURL = cf.baseURL
		case "cache-dir":
			cfg.CacheDir = cf.cacheDir
		case "fail-closed":
			b, e := strconv.ParseBool(cf.failClosed)
			if e != nil {
				err = fmt.Errorf("fail-closed: %w", e)
				return
			}
			cfg.FailClosed = b
		}
	})
	return cfg, err
}

func runCheck(args []string) (bool, error) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	var cf commonFlags
	bindCommon(fs, &cf)
	asset := fs.String("asset", "", "asset code, e.g. btc")
	asJSON := fs.Bool("json", false, "emit JSON")
	exitCode := fs.Bool("exit", false, "exit 1 when the gate blocks")
	at := fs.String("at", "", "evaluate at this RFC3339 time (default now)")
	fs.Parse(args)

	gate, _, err := newGate(cf, fs)
	if err != nil {
		return false, err
	}
	req := volgate.Request{Asset: *asset}
	if byFlag(fs, "window") {
		d, _ := volgate.ParseDuration(cf.window)
		req.Window = d
	}
	if *at != "" {
		t, err := time.Parse(time.RFC3339, *at)
		if err != nil {
			return false, fmt.Errorf("invalid -at: %w", err)
		}
		req.At = t
	}
	if req.Asset == "" {
		return false, fmt.Errorf("-asset is required")
	}
	v := gate.Allow(context.Background(), req)
	if *asJSON {
		return !v.Allow && *exitCode, emitJSON(v)
	}
	printVerdict(v)
	return !v.Allow && *exitCode, nil
}

func runMeasure(args []string) error {
	fs := flag.NewFlagSet("measure", flag.ExitOnError)
	var cf commonFlags
	bindCommon(fs, &cf)
	asset := fs.String("asset", "", "asset code, e.g. btc")
	asJSON := fs.Bool("json", false, "emit JSON")
	at := fs.String("at", "", "evaluate at this RFC3339 time (default now)")
	fs.Parse(args)

	gate, _, err := newGate(cf, fs)
	if err != nil {
		return err
	}
	req := volgate.Request{Asset: *asset}
	if byFlag(fs, "window") {
		d, _ := volgate.ParseDuration(cf.window)
		req.Window = d
	}
	if *at != "" {
		t, err := time.Parse(time.RFC3339, *at)
		if err != nil {
			return fmt.Errorf("invalid -at: %w", err)
		}
		req.At = t
	}
	if req.Asset == "" {
		return fmt.Errorf("-asset is required")
	}
	sig, err := gate.Measure(context.Background(), req)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(sig)
	}
	fmt.Printf("%s (%s) at %s\n", sig.Asset, sig.Symbol, sig.At.Format(time.RFC3339))
	fmt.Printf("  bars sampled       : %d (%d returns, lookback %s)\n", sig.Bars, sig.Returns, sig.Lookback)
	fmt.Printf("  sigma              : %.3f bps per sqrt(minute)  (annualised %.2f%%)\n", sig.SigmaPerMinuteBps, sig.AnnualizedVolPct)
	fmt.Printf("  expected move      : %.3f bps over %s (1 sigma)\n", sig.ExpectedMoveBps, sig.Window)
	fmt.Printf("  expected |move|    : %.3f bps over %s\n", sig.ExpectedAbsMoveBps, sig.Window)
	fmt.Printf("  last price         : %g\n", sig.LastPrice)
	return nil
}

func runCalibrate(args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ExitOnError)
	var cf commonFlags
	bindCommon(fs, &cf)
	tradesPath := fs.String("trades", "", "newline-delimited JSON trades")
	asJSON := fs.Bool("json", false, "emit JSON")
	out := fs.String("out", "", "write JSON report to this file")
	thresholds := fs.String("thresholds", "", "comma-separated bps thresholds")
	wt := fs.Bool("with-rows", false, "include per-trade rows in the report")
	fs.Parse(args)

	gate, _, err := newGate(cf, fs)
	if err != nil {
		return err
	}
	if *tradesPath == "" {
		return fmt.Errorf("-trades is required")
	}
	f, err := os.Open(*tradesPath)
	if err != nil {
		return err
	}
	defer f.Close()
	trades, err := volgate.LoadTrades(f)
	if err != nil {
		return err
	}
	var ths []float64
	if *thresholds != "" {
		for _, s := range strings.Split(*thresholds, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return fmt.Errorf("invalid -thresholds entry %q", s)
			}
			ths = append(ths, v)
		}
	}
	rep, err := volgate.Calibrate(context.Background(), gate, trades, ths)
	if err != nil {
		return err
	}
	if !*wt {
		rep.Rows = nil
	}
	if *out != "" {
		raw, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*out, raw, 0o644); err != nil {
			return err
		}
	}
	if *asJSON {
		return emitJSON(rep)
	}
	printReport(rep)
	return nil
}

func printReport(rep volgate.Report) {
	fmt.Printf("trades=%d measured=%d failed=%d  lookback=%s window=%s\n",
		rep.Trades, rep.Measured, rep.Failed, rep.Lookback, rep.Window)
	fmt.Printf("baseline pnl = %+.2f\n\n", rep.BaselinePnL)
	fmt.Printf("%8s %6s %6s %11s %11s %8s %8s\n",
		"min_bps", "kept", "excl", "kept_pnl", "excl_pnl", "avg_kept", "dis%")
	for _, s := range rep.Sweep {
		fmt.Printf("%8.1f %6d %6d %+11.2f %+11.2f %+8.2f %7.0f%%\n",
			s.ThresholdBps, s.Kept, s.Excluded, s.KeptPnL, s.ExcludedPnL,
			s.KeptAvgPnL, 100*s.KeptDisagreeRate)
	}
}

func printVerdict(v volgate.Verdict) {
	status := "ALLOW"
	if !v.Allow {
		status = "BLOCK"
	}
	fmt.Printf("%s  %s  %s\n", status, v.Signal.Asset, v.Reason)
	if v.Signal.SigmaPerMinuteBps > 0 {
		fmt.Printf("       sigma=%.3f bps/min  expected_move=%.3f bps over %s  (min %.3f)\n",
			v.Signal.SigmaPerMinuteBps, v.Signal.ExpectedMoveBps, v.Signal.Window,
			v.Thresholds.MinExpectedMoveBps)
	}
}

func emitJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func byFlag(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func runConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	var cf commonFlags
	bindCommon(fs, &cf)
	asJSON := fs.Bool("json", false, "emit JSON")
	fs.Parse(args)
	cfg, err := buildConfig(cf)
	if err != nil {
		return err
	}
	cfg, err = applyFlags(cf, fs, cfg)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(cfg)
	}
	fmt.Printf("lookback                 : %s\n", cfg.Lookback)
	fmt.Printf("default window           : %s\n", cfg.DefaultWindow)
	fmt.Printf("min expected move        : %.3f bps\n", cfg.MinExpectedMoveBps)
	fmt.Printf("min sigma per minute     : %.3f bps (0 = disabled)\n", cfg.MinSigmaPerMinuteBps)
	fmt.Printf("min bars                 : %d\n", cfg.MinBars)
	fmt.Printf("fail closed              : %v\n", cfg.FailClosed)
	fmt.Printf("base url                 : %s\n", cfg.BaseURL)
	fmt.Printf("cache dir                : %s\n", cfg.CacheDir)
	fmt.Printf("cache ttl                : %s\n", cfg.CacheTTL)
	fmt.Printf("timeout                  : %s\n", cfg.Timeout)
	return nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var cf commonFlags
	bindCommon(fs, &cf)
	addr := fs.String("addr", "127.0.0.1:8791", "listen address")
	fs.Parse(args)
	gate, cfg, err := newGate(cf, fs)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, gate.Config())
	})
	mux.HandleFunc("/measure", func(w http.ResponseWriter, r *http.Request) {
		req, err := requestFromQuery(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		sig, err := gate.Measure(r.Context(), req)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, sig)
	})
	mux.HandleFunc("/check", func(w http.ResponseWriter, r *http.Request) {
		req, err := requestFromQuery(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, gate.Allow(r.Context(), req))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "volgate %s\n\nGET /healthz\nGET /config\nGET /measure?asset=btc&window=900&at=RFC3339\nGET /check?asset=btc&window=900&at=RFC3339\n",
			effectiveVersion())
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "volgate %s serving on http://%s (window=%s, min>%.2fbps, lookback=%s)\n",
		effectiveVersion(), *addr, cfg.DefaultWindow, cfg.MinExpectedMoveBps, cfg.Lookback)
	return srv.ListenAndServe()
}

func requestFromQuery(r *http.Request) (volgate.Request, error) {
	q := r.URL.Query()
	req := volgate.Request{Asset: q.Get("asset")}
	if req.Asset == "" {
		return req, fmt.Errorf("asset is required")
	}
	if v := q.Get("window"); v != "" {
		d, err := volgate.ParseDuration(v)
		if err != nil {
			return req, err
		}
		req.Window = d
	}
	if v := q.Get("at"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return req, fmt.Errorf("at: %w", err)
		}
		req.At = t
	}
	return req, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
