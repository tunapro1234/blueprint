package api

// usage.go serves the fleet console's accounts/usage surface
// (docs/fleet-console.md, "Accounts / usage — WS3"): per-account utilization as
// a fraction of each account's limit, plus a recent-days utilization series. It
// is strictly read-only: it never switches accounts, never refreshes a token and
// never touches the network, so the console cannot become a second control plane
// over the accounts module.
//
// Honesty is the whole job here. The reused packages expose utilization as a
// percentage only; none of them exposes a per-window token budget. So estTokens
// cannot be derived without inventing a number, and it is always null. Likewise
// an account with no cached reading reports a null percentOfLimit rather than a
// guessed zero, and asOf carries the real freshness of the underlying reading so
// a stale panel is visibly stale rather than silently passed off as current.
//
// The handler is registered through RegisterUsage so the integrator wires it onto
// the /v1 mux without this file touching http.go. It depends on a single injected
// Overview function (mirroring how fleet.go injects Observe), so tests drive it
// with a stub and need no claudeacct store, no Claude login and no network.

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"blueprint/internal/claudeacct"
	"blueprint/internal/usagecli"
)

// usageRecentDays bounds how many recent calendar days the recent-days series
// reports, newest last.
const usageRecentDays = 14

// UsageDeps is the usage endpoint's single dependency. The integrator builds it
// with NewUsageDeps for production; tests inject a stub Overview so the handler
// needs no real accounts store, Claude login or network.
type UsageDeps struct {
	// Overview returns the console's usage view. It must reflect real data only:
	// unknown values stay unknown (nil), and asOf is the freshness of the
	// underlying reading, not the wall clock.
	Overview func(ctx context.Context) (ConsoleUsage, error)
}

// ConsoleUsage is the usage view the console renders. It is the exact shape of
// GET /v1/usage/accounts, carried in Go-native types so the production builder
// and the tests both speak the same structure; the handler formats times and
// nullable fields into JSON.
type ConsoleUsage struct {
	// AsOf is when the underlying reading was taken (its real freshness). Zero
	// means unknown, and the field is then omitted.
	AsOf time.Time
	// Accounts is the per-account current utilization.
	Accounts []ConsoleUsageAccount
	// RecentDays is the overall recent-days utilization series, oldest first.
	RecentDays []ConsoleUsageDay
}

// ConsoleUsageAccount is one account's current utilization. PercentOfLimit and
// Limit are fractions in [0,1] (0.37 = 37%), matching the contract. A nil
// PercentOfLimit means no reading is known; EstTokens is always nil until a
// token budget is actually available, because a token count cannot be derived
// from a percentage alone.
type ConsoleUsageAccount struct {
	Name            string
	Slot            int
	PercentOfLimit  *float64
	Limit           *float64
	EstTokens       *int64
	FiveHourResetAt time.Time
	Window          string
	Active          bool
}

// ConsoleUsageDay is one day's overall utilization. Percent is a fraction in
// [0,1]; EstTokens is always nil (see ConsoleUsageAccount).
type ConsoleUsageDay struct {
	Date      string
	Percent   *float64
	EstTokens *int64
}

// NewUsageDeps wires the production usage dependency. accounts come from the
// Claude accounts store under stateDir (read-only, never fetched), and the
// recent-days series from the usage history file at usageHistory (the same file
// `bp usage` reads). Either source being absent degrades gracefully to fewer
// rows rather than a fabricated reading.
func NewUsageDeps(stateDir, usageHistory string) UsageDeps {
	return UsageDeps{
		Overview: func(ctx context.Context) (ConsoleUsage, error) {
			return productionUsageOverview(ctx, stateDir, usageHistory)
		},
	}
}

// RegisterUsage mounts the usage endpoint on mux. The caller supplies an
// already-authenticated mux; this handler adds no auth of its own.
func RegisterUsage(mux *http.ServeMux, deps UsageDeps) {
	mux.HandleFunc("GET /v1/usage/accounts", deps.handleAccounts)
}

// --- GET /v1/usage/accounts ---

type usageResponseJSON struct {
	AsOf       string             `json:"asOf,omitempty"`
	Accounts   []usageAccountJSON `json:"accounts"`
	RecentDays []usageDayJSON     `json:"recentDays"`
}

type usageAccountJSON struct {
	Name            string   `json:"name"`
	Slot            int      `json:"slot"`
	PercentOfLimit  *float64 `json:"percentOfLimit"`
	Limit           *float64 `json:"limit"`
	EstTokens       *int64   `json:"estTokens"`
	FiveHourResetAt string   `json:"fiveHourResetAt,omitempty"`
	Window          string   `json:"window,omitempty"`
	Active          bool     `json:"active"`
}

type usageDayJSON struct {
	Date      string   `json:"date"`
	Percent   *float64 `json:"percent"`
	EstTokens *int64   `json:"estTokens"`
}

func (d UsageDeps) handleAccounts(w http.ResponseWriter, r *http.Request) {
	if d.Overview == nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "usage overview not configured", "")
		return
	}
	overview, err := d.Overview(r.Context())
	if err != nil {
		// Fail closed: a reading that cannot be produced is an error, never a
		// row of fabricated zeros.
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "read usage: "+err.Error(), "")
		return
	}
	resp := usageResponseJSON{
		Accounts:   make([]usageAccountJSON, 0, len(overview.Accounts)),
		RecentDays: make([]usageDayJSON, 0, len(overview.RecentDays)),
	}
	if !overview.AsOf.IsZero() {
		resp.AsOf = overview.AsOf.UTC().Format(time.RFC3339)
	}
	for _, a := range overview.Accounts {
		entry := usageAccountJSON{
			Name:           a.Name,
			Slot:           a.Slot,
			PercentOfLimit: a.PercentOfLimit,
			Limit:          a.Limit,
			EstTokens:      a.EstTokens,
			Window:         a.Window,
			Active:         a.Active,
		}
		if !a.FiveHourResetAt.IsZero() {
			entry.FiveHourResetAt = a.FiveHourResetAt.UTC().Format(time.RFC3339)
		}
		resp.Accounts = append(resp.Accounts, entry)
	}
	for _, day := range overview.RecentDays {
		resp.RecentDays = append(resp.RecentDays, usageDayJSON{Date: day.Date, Percent: day.Percent, EstTokens: day.EstTokens})
	}
	writeJSONResponse(w, http.StatusOK, resp)
}

// --- production Overview builder ---

// productionUsageOverview reads the accounts store (read-only) and the usage
// history to build the console usage view. It never fetches: no network call and
// no token rotation, so the console stays a pure reader of state the accounts
// module already maintains.
func productionUsageOverview(ctx context.Context, stateDir, usageHistory string) (ConsoleUsage, error) {
	manager := &claudeacct.Manager{
		Store:  claudeacct.NewStore(stateDir),
		Env:    claudeacct.OSEnv(),
		Client: &claudeacct.Client{},
	}
	// OverviewOptions zero value: Fetch is false, so the cached reading is used
	// as-is and asOf reports exactly how fresh it is.
	overview, err := manager.Overview(ctx, claudeacct.OverviewOptions{})
	if err != nil {
		return ConsoleUsage{}, err
	}
	now := overview.Now
	out := ConsoleUsage{}
	var asOf time.Time
	for _, slot := range overview.Slots {
		out.Accounts = append(out.Accounts, usageAccountFromSlot(slot, now))
		if slot.LastUsage != nil && slot.LastUsage.FetchedAt.After(asOf) {
			asOf = slot.LastUsage.FetchedAt
		}
	}
	days, latestSample := recentDaysFromHistory(usageHistory)
	out.RecentDays = days
	if latestSample.After(asOf) {
		asOf = latestSample
	}
	out.AsOf = asOf
	return out, nil
}

// usageAccountFromSlot maps one stored account to the console view. Status fields
// are copied from the cached reading; nothing is recomputed into a figure the
// data does not support.
func usageAccountFromSlot(slot claudeacct.SlotView, now time.Time) ConsoleUsageAccount {
	account := ConsoleUsageAccount{
		Name:   usageAccountName(slot.Slot),
		Slot:   slot.Number,
		Active: slot.Active,
	}
	// limit is the default policy threshold (100%). Per-account configured limits
	// (AutoPolicy.Limits) are not wired into the read-only console in v1; this is
	// the real default cap, not an estimate.
	limit := 1.0
	account.Limit = &limit
	var usage *claudeacct.Usage
	if slot.LastUsage != nil {
		usage = slot.LastUsage.Usage
	}
	if usage == nil {
		// No cached reading: percentOfLimit stays nil (unknown), never 0.
		return account
	}
	switch {
	case usage.FiveHour != nil:
		fraction := usageWindowFraction(usage.FiveHour, now)
		account.PercentOfLimit = &fraction
		account.Window = "5h"
		if !usage.FiveHour.ResetsAt.IsZero() {
			account.FiveHourResetAt = usage.FiveHour.ResetsAt
		}
	case usage.SevenDay != nil:
		fraction := usageWindowFraction(usage.SevenDay, now)
		account.PercentOfLimit = &fraction
		account.Window = "7d"
	}
	// estTokens stays nil: no reused package exposes a per-window token budget,
	// so a token count cannot be derived from the percentage without inventing
	// the denominator.
	return account
}

// usageAccountName names an account the way the console labels it: its alias,
// else its email, else "slot N".
func usageAccountName(slot claudeacct.Slot) string {
	if slot.Alias != "" {
		return slot.Alias
	}
	if slot.Email != "" {
		return slot.Email
	}
	return "slot " + strconv.Itoa(slot.Number)
}

// usageWindowFraction is a window's utilization as a fraction in [0,1]. A window
// whose reset time has passed has started over, so it reads as 0 (mirroring the
// accounts module's own effective-utilization rule) rather than reporting a stale
// full window.
func usageWindowFraction(window *claudeacct.Window, now time.Time) float64 {
	utilization := window.Utilization
	if !window.ResetsAt.IsZero() && !now.Before(window.ResetsAt) {
		utilization = 0
	}
	return utilization / 100
}

// recentDaysFromHistory builds the overall recent-days utilization series from
// the usage history file (the same file `bp usage` reads). Each day's value is
// the peak Claude five-hour utilization recorded that day, which is the most
// day-granular figure the samples carry. It also returns the newest sample
// timestamp seen, used to date the whole view honestly. A missing path or an
// unreadable history yields no rows rather than an error: the accounts list is
// the primary payload and must still render.
func recentDaysFromHistory(path string) ([]ConsoleUsageDay, time.Time) {
	if path == "" {
		return nil, time.Time{}
	}
	dayMax := map[string]float64{}
	var latest time.Time
	// readJSONL skips undecodable lines, so a torn tail never hides the history.
	_ = readJSONL(path, func(sample usagecli.Sample) bool {
		ts, err := time.Parse(time.RFC3339, sample.TS)
		if err != nil {
			return true
		}
		if ts.After(latest) {
			latest = ts
		}
		percent, ok := sampleClaudePercent(sample)
		if !ok {
			return true
		}
		date := ts.UTC().Format("2006-01-02")
		if current, seen := dayMax[date]; !seen || percent > current {
			dayMax[date] = percent
		}
		return true
	})
	if len(dayMax) == 0 {
		return nil, latest
	}
	dates := make([]string, 0, len(dayMax))
	for date := range dayMax {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	if len(dates) > usageRecentDays {
		dates = dates[len(dates)-usageRecentDays:]
	}
	out := make([]ConsoleUsageDay, 0, len(dates))
	for _, date := range dates {
		fraction := dayMax[date] / 100
		out = append(out, ConsoleUsageDay{Date: date, Percent: &fraction})
	}
	return out, latest
}

// sampleClaudePercent reads the Claude five-hour utilization from a history
// sample. The value is an untyped number in the record, so it is float64 after
// JSON decoding; anything else (a null, a missing figure) is not a reading.
func sampleClaudePercent(sample usagecli.Sample) (float64, bool) {
	if value, ok := sample.Claude5.(float64); ok {
		return value, true
	}
	return 0, false
}
