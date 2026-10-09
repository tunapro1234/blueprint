package claudeacct

import (
	"encoding/json"
	"errors"
	"math"
	"time"
)

// Window is one rate-limit window: utilization percent and reset time.
type Window struct {
	Utilization float64   `json:"utilization"`
	ResetsAt    time.Time `json:"resetsAt,omitempty"`
}

// ScopedLimit is a per-model limit from the limits array, shown but not
// used for switching decisions.
type ScopedLimit struct {
	Name     string    `json:"name"`
	Percent  float64   `json:"percent"`
	ResetsAt time.Time `json:"resetsAt,omitempty"`
}

// Usage is the normalized usage answer. A nil window is unknown (Team seats
// have no seven_day window).
type Usage struct {
	FiveHour *Window       `json:"fiveHour,omitempty"`
	SevenDay *Window       `json:"sevenDay,omitempty"`
	Scoped   []ScopedLimit `json:"scoped,omitempty"`
}

type rawWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

// ParseUsage normalizes the usage endpoint body.
func ParseUsage(body []byte) (*Usage, error) {
	var raw struct {
		FiveHour *rawWindow        `json:"five_hour"`
		SevenDay *rawWindow        `json:"seven_day"`
		Limits   []json.RawMessage `json:"limits"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil || top == nil {
		return nil, errors.New("usage answer is not a JSON object")
	}
	usage := &Usage{FiveHour: raw.FiveHour.window(), SevenDay: raw.SevenDay.window()}
	for _, item := range raw.Limits {
		var limit struct {
			Scope *struct {
				Model *struct {
					DisplayName string `json:"display_name"`
				} `json:"model"`
			} `json:"scope"`
			Percent  *float64 `json:"percent"`
			ResetsAt *string  `json:"resets_at"`
		}
		if json.Unmarshal(item, &limit) != nil || limit.Scope == nil || limit.Scope.Model == nil || limit.Scope.Model.DisplayName == "" || limit.Percent == nil {
			continue
		}
		usage.Scoped = append(usage.Scoped, ScopedLimit{Name: limit.Scope.Model.DisplayName, Percent: *limit.Percent, ResetsAt: parseReset(limit.ResetsAt)})
	}
	return usage, nil
}

func (w *rawWindow) window() *Window {
	if w == nil || w.Utilization == nil || math.IsNaN(*w.Utilization) {
		return nil
	}
	return &Window{Utilization: *w.Utilization, ResetsAt: parseReset(w.ResetsAt)}
}

func parseReset(value *string) time.Time {
	if value == nil || *value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05.999999999Z07:00"} {
		if parsed, err := time.Parse(layout, *value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// effective is the window's utilization at now: a window whose reset time
// has passed has started over.
func (w *Window) effective(now time.Time) float64 {
	if !w.ResetsAt.IsZero() && !now.Before(w.ResetsAt) {
		return 0
	}
	return w.Utilization
}

// Max is the higher of the 5h and 7d utilization at now; ok is false when
// neither window is known.
func (u *Usage) Max(now time.Time) (value float64, ok bool) {
	if u == nil {
		return 0, false
	}
	for _, w := range []*Window{u.FiveHour, u.SevenDay} {
		if w == nil {
			continue
		}
		if e := w.effective(now); !ok || e > value {
			value = e
		}
		ok = true
	}
	return value, ok
}

// FiveHourReset is when the five-hour window next resets, or the zero time
// when there is no known five-hour window (an idle account has none).
func (u *Usage) FiveHourReset() time.Time {
	if u == nil || u.FiveHour == nil {
		return time.Time{}
	}
	return u.FiveHour.ResetsAt
}

// Usage fetch backoff when the server gives no Retry-After.
const (
	backoffBase = time.Minute
	backoffCap  = 30 * time.Minute
)

// recordUsage updates a cache after a fetch.
func recordUsage(cache *UsageCache, now time.Time, usage *Usage, err error) *UsageCache {
	if cache == nil {
		cache = &UsageCache{}
	}
	cache.AttemptAt = now
	if err == nil {
		cache.Usage, cache.FetchedAt = usage, now
		cache.Error, cache.Failures, cache.BackoffUntil = "", 0, time.Time{}
		return cache
	}
	cache.Failures++
	kind := "network"
	wait := backoffBase << min(cache.Failures-1, 5)
	if wait > backoffCap {
		wait = backoffCap
	}
	var usageErr *UsageError
	var refreshErr *RefreshError
	switch {
	case errors.As(err, &usageErr):
		kind = usageErr.Kind
		if usageErr.HasRetry {
			wait = usageErr.RetryAfter
		}
	case errors.As(err, &refreshErr):
		kind = "refresh-" + refreshErr.Kind
	}
	cache.Error = kind
	cache.BackoffUntil = now.Add(wait)
	return cache
}

// due reports whether the cache should be refetched.
func (c *UsageCache) due(now time.Time, maxAge time.Duration) bool {
	if c == nil {
		return true
	}
	if now.Before(c.BackoffUntil) {
		return false
	}
	last := c.FetchedAt
	if c.AttemptAt.After(last) {
		last = c.AttemptAt
	}
	return now.Sub(last) >= maxAge
}
