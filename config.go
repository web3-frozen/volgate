package volgate

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultThresholdBps is the default minimum expected move over the market
// window. It was calibrated on historical short-horizon crypto up/down trades
// (see the repository's calibration notes): catastrophic "both legs lose"
// outcomes came from windows whose realized move was under ~2-3 bps, because
// that is the scale at which two venues' different settlement sources disagree.
// Requiring a 1-sigma expected move of 5 bps keeps roughly a 2x margin over
// that disagreement zone.
const DefaultThresholdBps = 5.0

// Config controls how a Gate decides. Zero values fall back to defaults via
// DefaultConfig, so a partially populated Config is valid.
type Config struct {
	// Lookback is the trailing window of 1-minute bars used to estimate
	// volatility at check time. Default 60m.
	Lookback Duration `json:"lookback"`
	// MinExpectedMoveBps is the primary threshold: the 1-sigma expected move
	// over the market's resolution window must be at least this many bps.
	MinExpectedMoveBps float64 `json:"min_expected_move_bps"`
	// MinSigmaPerMinuteBps is an optional secondary floor on the raw
	// volatility. 0 disables it.
	MinSigmaPerMinuteBps float64 `json:"min_sigma_per_minute_bps"`
	// MinBars is the smallest number of returns required to trust an estimate.
	// Default 30.
	MinBars int `json:"min_bars"`
	// MaxAbsReturn is the sanity bound on a single 1-minute log return, in
	// decimal units (0.5 = 50%). Larger returns are treated as bad prints and
	// dropped. Default 0.5; 0 disables the filter.
	MaxAbsReturn float64 `json:"max_abs_return"`
	// DefaultWindow is used when a request does not specify one. Default 15m.
	DefaultWindow Duration `json:"default_window"`
	// FailClosed blocks trading when volatility cannot be estimated. Because
	// the gate exists to avoid a known loss mode, failing open silently
	// reintroduces it; the default is therefore true.
	FailClosed bool `json:"fail_closed"`
	// Assets overrides per asset (key: lowercase asset, e.g. "btc").
	Assets map[string]AssetConfig `json:"assets,omitempty"`
	// Symbols maps an asset to its spot symbol (e.g. btc -> BTCUSDT).
	Symbols map[string]string `json:"symbols,omitempty"`
	// BaseURL is the price source base URL.
	BaseURL string `json:"base_url,omitempty"`
	// CacheDir enables an on-disk bar cache; "" means memory only.
	CacheDir string `json:"cache_dir,omitempty"`
	// CacheTTL is how long the current day's bars are reused.
	CacheTTL Duration `json:"cache_ttl"`
	// Timeout bounds a single check's outbound calls.
	Timeout Duration `json:"timeout"`
}

// AssetConfig selectively overrides settings for one asset. A nil pointer
// means "inherit", so partial overrides are supported.
type AssetConfig struct {
	MinExpectedMoveBps   *float64 `json:"min_expected_move_bps,omitempty"`
	MinSigmaPerMinuteBps *float64 `json:"min_sigma_per_minute_bps,omitempty"`
	Symbol               string   `json:"symbol,omitempty"`
	// Window overrides the market window used for reporting when the caller
	// does not know it.
	Window *Duration `json:"window,omitempty"`
}

// DefaultSymbols maps the asset codes used by short-horizon markets to Binance
// spot symbols.
var DefaultSymbols = map[string]string{
	"btc":  "BTCUSDT",
	"eth":  "ETHUSDT",
	"bnb":  "BNBUSDT",
	"sol":  "SOLUSDT",
	"xrp":  "XRPUSDT",
	"doge": "DOGEUSDT",
	"ada":  "ADAUSDT",
	"ltc":  "LTCUSDT",
	"near": "NEARUSDT",
	"zec":  "ZECUSDT",
	"hype": "HYPEUSDT",
}

// DefaultConfig returns the calibrated defaults.
func DefaultConfig() Config {
	return Config{
		Lookback:             Duration(60 * time.Minute),
		MinExpectedMoveBps:   DefaultThresholdBps,
		MinSigmaPerMinuteBps: 0,
		MinBars:              30,
		MaxAbsReturn:         0.5,
		DefaultWindow:        Duration(15 * time.Minute),
		FailClosed:           true,
		Symbols:              map[string]string{},
		CacheDir:             DefaultCacheDir(),
		CacheTTL:             Duration(90 * time.Second),
		Timeout:              Duration(20 * time.Second),
	}
}

// WithDefaults fills zero fields with the defaults and returns the result.
func (c Config) WithDefaults() Config {
	d := DefaultConfig()
	if c.Lookback <= 0 {
		c.Lookback = d.Lookback
	}
	if c.DefaultWindow <= 0 {
		c.DefaultWindow = d.DefaultWindow
	}
	if c.MinBars <= 0 {
		c.MinBars = d.MinBars
	}
	if c.MaxAbsReturn <= 0 {
		c.MaxAbsReturn = d.MaxAbsReturn
	}
	if c.CacheTTL <= 0 {
		c.CacheTTL = d.CacheTTL
	}
	if c.Timeout <= 0 {
		c.Timeout = d.Timeout
	}
	if c.BaseURL == "" {
		c.BaseURL = d.BaseURL
	}
	if c.Symbols == nil {
		c.Symbols = map[string]string{}
	}
	return c
}

// Validate reports configuration problems that would otherwise fail silently.
func (c Config) Validate() error {
	if c.MinExpectedMoveBps < 0 || c.MinSigmaPerMinuteBps < 0 {
		return fmt.Errorf("volgate: thresholds must not be negative")
	}
	if c.MinExpectedMoveBps == 0 && c.MinSigmaPerMinuteBps == 0 {
		return fmt.Errorf("volgate: at least one threshold must be positive")
	}
	return nil
}

// SymbolFor resolves the spot symbol for an asset, honouring overrides and
// falling back to an uppercased "<ASSET>USDT".
func (c Config) SymbolFor(asset string) string {
	a := strings.ToLower(strings.TrimSpace(asset))
	if a == "" {
		return ""
	}
	if ac, ok := c.Assets[a]; ok && ac.Symbol != "" {
		return ac.Symbol
	}
	if s, ok := c.Symbols[a]; ok && s != "" {
		return s
	}
	if s, ok := DefaultSymbols[a]; ok {
		return s
	}
	return strings.ToUpper(a) + "USDT"
}

// Thresholds is the effective, per-asset threshold pair.
type Thresholds struct {
	MinExpectedMoveBps   float64  `json:"min_expected_move_bps"`
	MinSigmaPerMinuteBps float64  `json:"min_sigma_per_minute_bps"`
	Window               Duration `json:"window"`
}

// Effective returns the thresholds that apply to asset, after overrides.
func (c Config) Effective(asset string) Thresholds {
	t := Thresholds{
		MinExpectedMoveBps:   c.MinExpectedMoveBps,
		MinSigmaPerMinuteBps: c.MinSigmaPerMinuteBps,
		Window:               c.DefaultWindow,
	}
	if ac, ok := c.Assets[strings.ToLower(strings.TrimSpace(asset))]; ok {
		if ac.MinExpectedMoveBps != nil {
			t.MinExpectedMoveBps = *ac.MinExpectedMoveBps
		}
		if ac.MinSigmaPerMinuteBps != nil {
			t.MinSigmaPerMinuteBps = *ac.MinSigmaPerMinuteBps
		}
		if ac.Window != nil {
			t.Window = *ac.Window
		}
	}
	return t
}

// LoadConfig reads a JSON config file, merged over the defaults.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := jsonUnmarshalStrict(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("volgate: parse %s: %w", path, err)
	}
	return cfg.WithDefaults(), nil
}

// DefaultCacheDir is where bars are cached when a disk cache is enabled.
func DefaultCacheDir() string {
	if d, err := os.UserCacheDir(); err == nil && d != "" {
		return filepath.Join(d, "volgate")
	}
	return filepath.Join(os.TempDir(), "volgate")
}

// ApplyEnv overlays configuration from VOLGATE_* environment variables. It is
// the bridge used by callers (CLI, server, or a shell wrapper) that would
// rather not ship a config file.
//
//	VOLGATE_LOOKBACK             e.g. "60m"
//	VOLGATE_DEFAULT_WINDOW       e.g. "15m"
//	VOLGATE_MIN_EXPECTED_MOVE_BPS
//	VOLGATE_MIN_SIGMA_PER_MIN_BPS
//	VOLGATE_FAIL_CLOSED          "true"/"false"
//	VOLGATE_BASE_URL
//	VOLGATE_CACHE_DIR
//	VOLGATE_CACHE_TTL
//	VOLGATE_TIMEOUT
func (c Config) ApplyEnv(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	var err error
	setDur := func(key string, dst *time.Duration) {
		if err != nil {
			return
		}
		if v := strings.TrimSpace(getenv(key)); v != "" {
			d, e := ParseDuration(v)
			if e != nil {
				err = fmt.Errorf("%s: %w", key, e)
				return
			}
			*dst = d
		}
	}
	setFloat := func(key string, dst *float64) {
		if err != nil {
			return
		}
		if v := strings.TrimSpace(getenv(key)); v != "" {
			f, e := strconv.ParseFloat(v, 64)
			if e != nil {
				err = fmt.Errorf("%s: %w", key, e)
				return
			}
			*dst = f
		}
	}
	setDur("VOLGATE_LOOKBACK", (*time.Duration)(&c.Lookback))
	setDur("VOLGATE_DEFAULT_WINDOW", (*time.Duration)(&c.DefaultWindow))
	setDur("VOLGATE_CACHE_TTL", (*time.Duration)(&c.CacheTTL))
	setDur("VOLGATE_TIMEOUT", (*time.Duration)(&c.Timeout))
	setFloat("VOLGATE_MIN_EXPECTED_MOVE_BPS", &c.MinExpectedMoveBps)
	setFloat("VOLGATE_MIN_SIGMA_PER_MIN_BPS", &c.MinSigmaPerMinuteBps)
	if v := strings.TrimSpace(getenv("VOLGATE_BASE_URL")); v != "" {
		c.BaseURL = v
	}
	if v := strings.TrimSpace(getenv("VOLGATE_CACHE_DIR")); v != "" {
		c.CacheDir = v
	}
	if v := strings.TrimSpace(getenv("VOLGATE_FAIL_CLOSED")); v != "" {
		b, e := strconv.ParseBool(v)
		if e != nil {
			err = fmt.Errorf("VOLGATE_FAIL_CLOSED: %w", e)
		} else {
			c.FailClosed = b
		}
	}
	return c, err
}
