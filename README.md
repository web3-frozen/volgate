# volgate

Realized-volatility gate for short-horizon prediction markets.

`volgate` answers one question, in one number:

> Given how much the underlying has actually been moving, how large a move
> should we expect over this market's resolution window?

It reports that as **expected move in basis points**, and blocks trading when the
number is too small.

## Why this exists

Short-horizon "up or down" markets (5m/15m/1h crypto windows) are often listed on
more than one venue with the same slug. Those venues do not necessarily settle
from the same reference price — one may use a Chainlink TWAP, another a spot
exchange price, another a K-line open/close.

When the underlying moves a lot over the window, every reference price agrees on
the direction and the distinction does not matter. When the underlying barely
moves, the difference between the two reference prices *is* the outcome: the
venues can resolve in **opposite** directions from a move far smaller than any
sensible market view. A hedge that requires the venues to agree loses on **both**
legs.

The size of that "danger zone" is a few basis points, and it is cheap to measure:
if the expected move over the window is comfortably larger than the spread
between the venues' reference prices, the disagreement regime cannot bite.

`volgate` computes the expected move from realized volatility of 1-minute bars
and refuses to trade when it is below a configured threshold.

## Install

```bash
go install github.com/web3-frozen/volgate/cmd/volgate@latest
```

Or build from a checkout:

```bash
make build          # -> bin/volgate
```

The library and CLI have **no dependencies outside the Go standard library**.

## Usage

### CLI

```bash
# One decision, machine readable.
volgate check -asset btc -window 15m -json

# Just the measurement.
volgate measure -asset btc -window 15m

# Re-derive the threshold from your own trade history.
volgate calibrate -trades trades.jsonl -window 15m

# Long-running HTTP service (in-memory bar cache, for polling callers).
volgate serve -addr 127.0.0.1:8791
```

`check` prints a verdict and always exits 0 unless something went wrong. Add
`-exit` to exit 1 when the gate blocks, which is convenient in shell scripts:

```bash
volgate check -asset btc -window 15m -exit || echo "too quiet, skipping"
```

### HTTP

```
GET /healthz
GET /config
GET /measure?asset=btc&window=900&at=2026-05-01T12:00:00Z
GET /check?asset=btc&window=900
```

### Library

```go
src := volgate.NewBinanceSource()
cfg := volgate.DefaultConfig()
cfg.Lookback = volgate.Duration(60 * time.Minute)
cfg.MinExpectedMoveBps = 5

gate := volgate.NewGate(src, cfg)
v := gate.Allow(ctx, volgate.Request{Asset: "btc", Window: 15 * time.Minute})

if !v.Allow {
    log.Printf("skip: %s (expected %.2f bps)", v.Reason, v.Signal.ExpectedMoveBps)
}
```

`Gate.Allow` never returns an error. When the price source is unreachable the
verdict follows the `FailClosed` setting, which defaults to `true`: the gate
exists to avoid a known loss mode, so failing open would silently reintroduce it.

## How the number is computed

1. Fetch 1-minute closes for the asset over a trailing lookback (default 60m).
2. `sigma = stdev(log returns)`, in **bps per sqrt(minute)**.
3. `expected_move = sigma * sqrt(window_minutes)`, in **bps**.

That is the one-standard-deviation expected size of the move over the window.
Two details matter:

- **Heavy tails are kept.** One-minute crypto returns have fat tails. Estimators
  that clip aggressively or use a MAD-based scale report a fraction of the true
  volatility, which would block nearly everything. Only an absolute sanity bound
  is applied (default 50% in one minute), which removes bad prints without
  touching real moves.
- **The window matters.** `expected_move` scales with `sqrt(window)`, so a
  threshold expressed in bps is comparable across 5m/15m/1h markets, and a 5m
  market needs higher per-minute volatility to clear it. That is the right
  behaviour: shorter windows have less time to travel away from the strike.

## Thresholds

The threshold is a property of the *venues*, not of the model: it should sit
comfortably above the typical difference between their reference prices. A
default of **5 bps** is shipped, which is roughly 2x the level at which
short-horizon venues settled from different reference prices in the calibration
sample this package was built from.

Re-derive it against your own history with `calibrate`. Give it one JSON object
per trade per line:

```json
{"asset":"btc","slug":"btc-updown-15m-1790498700","entry_ts":"2026-09-27T08:47:00Z","window":"15m","payout":1,"shares":5.34,"cost":4.59,"agreed":true}
```

| field      | meaning                                                          |
|------------|------------------------------------------------------------------|
| `asset`    | asset code (`btc`, `eth`, …)                                     |
| `entry_ts` | RFC3339 timestamp of the entry                                   |
| `window`   | resolution window (`"15m"`, `900`, …)                            |
| `payout`   | how many legs settled in the money (`0` = both lost)             |
| `shares`   | position size                                                     |
| `cost`     | total USD paid for both legs                                      |
| `pnl`      | optional explicit PnL, overriding `payout*shares - cost`          |
| `agreed`   | optional: did the venues resolve the same way?                   |

`calibrate` prints, for each candidate threshold, how many trades survive, the
PnL of the kept and excluded groups, and the disagreement rate in each. Read it
as: *does removing the low-volatility tail remove the losses?*

```
 min_bps   kept   excl    kept_pnl    excl_pnl avg_kept     dis%
     3.0     70      0       -8.75       +0.00    -0.13      26%
     5.0     49     21      +15.47      -24.23    +0.32      20%
     8.0     25     45       -9.43       +0.68    -0.38      36%
```

Pick the smallest threshold where the excluded group is the loss-making one and
the result is stable across neighbouring thresholds — non-monotonic bumps are
usually small-sample noise.

## Configuration

Flags override environment variables, which override a `-config` JSON file,
which overrides the defaults.

| env var                            | flag                          | default          |
|------------------------------------|-------------------------------|------------------|
| `VOLGATE_LOOKBACK`                 | `-lookback`                   | `60m`            |
| `VOLGATE_DEFAULT_WINDOW`           | `-window`                     | `15m`            |
| `VOLGATE_MIN_EXPECTED_MOVE_BPS`    | `-min-expected-move-bps`      | `5`              |
| `VOLGATE_MIN_SIGMA_PER_MIN_BPS`    | `-min-sigma-per-min-bps`      | `0` (disabled)   |
| `VOLGATE_FAIL_CLOSED`              | `-fail-closed`                | `true`           |
| `VOLGATE_BASE_URL`                 | `-base-url`                   | Binance spot     |
| `VOLGATE_CACHE_DIR`                | `-cache-dir`                  | user cache dir   |
| `VOLGATE_CACHE_TTL`                |                               | `90s`            |
| `VOLGATE_TIMEOUT`                  | `-timeout`                    | `20s`            |

```json
{
  "lookback": "60m",
  "min_expected_move_bps": 5,
  "fail_closed": true,
  "assets": {
    "btc": { "min_expected_move_bps": 5 },
    "eth": { "min_expected_move_bps": 7, "symbol": "ETHUSDT" }
  }
}
```

Unknown fields in a config file are an error, so a typo cannot silently disable
a setting.

## Caching

Every check needs the same recent bars, so `volgate` caches them per UTC day.
Completed days are immutable and cached for the process lifetime; the current day
is refreshed after `CacheTTL`. If a disk cache directory is configured, completed
days survive restarts — which also means a run against historical trades does not
refetch the same day repeatedly.

In the HTTP server the cache is in memory, so keep the process running rather
than shelling out per check.

## Testing

```bash
make test     # go vet + go test ./...
```

## License

MIT — see [LICENSE](LICENSE).
