package volgate

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration that marshals as a human string ("15m", "1h")
// and unmarshals from either that string form or a plain number of seconds.
// Plain JSON durations would otherwise be nanosecond integers and unreadable.
type Duration time.Duration

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// String renders the duration using Go's compact form.
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON writes the compact string form.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts "15m", "900s", "1h30m", or a bare number of seconds.
func (d *Duration) UnmarshalJSON(raw []byte) error {
	s := strings.TrimSpace(string(raw))
	if s == "null" || s == `""` {
		*d = 0
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return err
		}
		v, err := ParseDuration(str)
		if err != nil {
			return err
		}
		*d = Duration(v)
		return nil
	}
	secs, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("volgate: invalid duration %s", s)
	}
	*d = Duration(time.Duration(secs * float64(time.Second)))
	return nil
}

// ParseDuration parses "15m" / "900" / "900s" / "1h30m".
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("volgate: empty duration")
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(n * float64(time.Second)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("volgate: invalid duration %q", s)
	}
	return d, nil
}
