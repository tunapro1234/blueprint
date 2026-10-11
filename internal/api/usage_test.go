package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"blueprint/internal/tokens"
)

// usageRequest drives one request through a mux that has RegisterUsage mounted.
func usageRequest(t *testing.T, deps UsageDeps, method, path string) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	RegisterUsage(mux, deps)
	req := httptest.NewRequest(method, path, bytes.NewReader(nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	out := map[string]any{}
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func usageFloatPtr(v float64) *float64 { return &v }

func TestUsageAccountsEndpoint(t *testing.T) {
	asOf := time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)
	reset := time.Date(2026, 10, 11, 14, 0, 0, 0, time.UTC)
	deps := UsageDeps{
		Overview: func(context.Context) (ConsoleUsage, error) {
			return ConsoleUsage{
				AsOf: asOf,
				Accounts: []ConsoleUsageAccount{
					{Name: "azra", Slot: 2, PercentOfLimit: usageFloatPtr(0.37), Limit: usageFloatPtr(0.80),
						FiveHourResetAt: reset, Window: "5h", Active: true},
					// An account with no cached reading: percent and window unknown,
					// never a guessed zero.
					{Name: "idle", Slot: 3, Limit: usageFloatPtr(1.0), Active: false},
				},
				RecentDays: []ConsoleUsageDay{{Date: "2026-10-10", Percent: usageFloatPtr(0.42)}},
			}, nil
		},
	}

	code, body := usageRequest(t, deps, "GET", "/v1/usage/accounts")
	if code != http.StatusOK {
		t.Fatalf("usage status = %d, body %v", code, body)
	}
	if body["asOf"] != asOf.Format(time.RFC3339) {
		t.Fatalf("asOf = %v, want %s", body["asOf"], asOf.Format(time.RFC3339))
	}
	accounts, ok := body["accounts"].([]any)
	if !ok || len(accounts) != 2 {
		t.Fatalf("accounts = %v (want 2)", body["accounts"])
	}

	azra := accounts[0].(map[string]any)
	if azra["name"] != "azra" || azra["slot"].(float64) != 2 || azra["active"] != true {
		t.Fatalf("azra identity = %v", azra)
	}
	if azra["percentOfLimit"].(float64) != 0.37 || azra["limit"].(float64) != 0.80 {
		t.Fatalf("azra percent/limit = %v / %v", azra["percentOfLimit"], azra["limit"])
	}
	if azra["window"] != "5h" || azra["fiveHourResetAt"] != reset.Format(time.RFC3339) {
		t.Fatalf("azra window/reset = %v / %v", azra["window"], azra["fiveHourResetAt"])
	}
	// estTokens must be present and null: a token count is never fabricated from a
	// percentage.
	if v, present := azra["estTokens"]; !present || v != nil {
		t.Fatalf("azra estTokens = %v (present %v), want explicit null", v, present)
	}

	idle := accounts[1].(map[string]any)
	// Unknown utilization stays null rather than a guessed idle 0.
	if v, present := idle["percentOfLimit"]; !present || v != nil {
		t.Fatalf("idle percentOfLimit = %v (present %v), want null", v, present)
	}
	// An unknown window is omitted, not reported as an empty string.
	if _, present := idle["window"]; present {
		t.Fatalf("idle window should be omitted, got %v", idle["window"])
	}
	if _, present := idle["fiveHourResetAt"]; present {
		t.Fatalf("idle fiveHourResetAt should be omitted, got %v", idle["fiveHourResetAt"])
	}

	recent, ok := body["recentDays"].([]any)
	if !ok || len(recent) != 1 {
		t.Fatalf("recentDays = %v (want 1)", body["recentDays"])
	}
	day := recent[0].(map[string]any)
	if day["date"] != "2026-10-10" || day["percent"].(float64) != 0.42 {
		t.Fatalf("recent day = %v", day)
	}
	if v, present := day["estTokens"]; !present || v != nil {
		t.Fatalf("recent day estTokens = %v (present %v), want null", v, present)
	}
}

func TestUsageAccountsEmpty(t *testing.T) {
	deps := UsageDeps{
		Overview: func(context.Context) (ConsoleUsage, error) { return ConsoleUsage{}, nil },
	}
	code, body := usageRequest(t, deps, "GET", "/v1/usage/accounts")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	// Empty data still renders arrays, not null, so the client can iterate, and
	// no asOf is claimed when nothing is known.
	if accounts, ok := body["accounts"].([]any); !ok || len(accounts) != 0 {
		t.Fatalf("accounts = %v, want []", body["accounts"])
	}
	if recent, ok := body["recentDays"].([]any); !ok || len(recent) != 0 {
		t.Fatalf("recentDays = %v, want []", body["recentDays"])
	}
	if _, present := body["asOf"]; present {
		t.Fatalf("asOf should be omitted when unknown, got %v", body["asOf"])
	}
}

func TestUsageAccountsFailsClosed(t *testing.T) {
	deps := UsageDeps{
		Overview: func(context.Context) (ConsoleUsage, error) {
			return ConsoleUsage{}, errors.New("account store unreadable")
		},
	}
	code, _ := usageRequest(t, deps, "GET", "/v1/usage/accounts")
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (fail closed on unreadable usage)", code)
	}
}

func TestUsageAccountsNotConfigured(t *testing.T) {
	code, _ := usageRequest(t, UsageDeps{}, "GET", "/v1/usage/accounts")
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when Overview is not configured", code)
	}
}

func TestUsageSerializesCostFields(t *testing.T) {
	est := int64(1_500_000)
	deps := UsageDeps{
		Overview: func(context.Context) (ConsoleUsage, error) {
			return ConsoleUsage{
				Accounts: []ConsoleUsageAccount{
					{Name: "tuna", Slot: 1, PercentOfLimit: usageFloatPtr(0.15), EstTokens: &est, Window: "5h", Active: true},
				},
				Subscription: &ConsoleSubscription{Plan: "team", Seats: 4, PerSeatUSD: 150, MonthlyUSD: 600},
				Spend: &ConsoleSpend{
					Window: "30d", TotalTokens: 3_000_000, ApiEquivalentUSD: 6,
					ByModel: []ConsoleModelSpend{{Model: "opus", Tokens: 1_000_000, ApiEquivalentUSD: 5}},
				},
				Value: &ConsoleValue{ApiEquivalentUSD: 6, SubscriptionUSD: 600, Ratio: 0.01},
			}, nil
		},
	}
	code, body := usageRequest(t, deps, "GET", "/v1/usage/accounts")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	// A configured per-seat budget lets the percentage map to an estimated token
	// count (passed through from the overview), no longer a forced null.
	acct := body["accounts"].([]any)[0].(map[string]any)
	if acct["estTokens"].(float64) != 1_500_000 {
		t.Fatalf("estTokens = %v, want 1500000", acct["estTokens"])
	}
	sub, ok := body["subscription"].(map[string]any)
	if !ok || sub["seats"].(float64) != 4 || sub["monthlyUsd"].(float64) != 600 || sub["plan"] != "team" {
		t.Fatalf("subscription = %v", body["subscription"])
	}
	spend, ok := body["spend"].(map[string]any)
	if !ok || spend["window"] != "30d" || spend["apiEquivalentUsd"].(float64) != 6 {
		t.Fatalf("spend = %v", body["spend"])
	}
	if models, ok := spend["byModel"].([]any); !ok || len(models) != 1 || models[0].(map[string]any)["model"] != "opus" {
		t.Fatalf("spend.byModel = %v", spend["byModel"])
	}
	val, ok := body["value"].(map[string]any)
	if !ok || val["subscriptionUsd"].(float64) != 600 {
		t.Fatalf("value = %v", body["value"])
	}
}

func TestModelSpendUSD(t *testing.T) {
	// Opus: 1M input + 1M output = 5 + 25 = $30.
	opus := modelSpendUSD(tokens.ModelTokens{Model: "claude-opus-4-8", In: 1_000_000, Out: 1_000_000})
	if opus != 30 {
		t.Fatalf("opus usd = %v, want 30", opus)
	}
	// Cache read is 0.1x input, cache write 1.25x input (haiku input = $1/M).
	haiku := modelSpendUSD(tokens.ModelTokens{Model: "claude-haiku-5", CacheRead: 1_000_000, CacheWrite: 1_000_000})
	if haiku != 0.1+1.25 {
		t.Fatalf("haiku cache usd = %v, want 1.35", haiku)
	}
	// A non-Claude model (Codex/GPT) is not priced at Claude rates.
	if gpt := modelSpendUSD(tokens.ModelTokens{Model: "gpt-6", In: 1_000_000, Out: 1_000_000}); gpt != 0 {
		t.Fatalf("gpt usd = %v, want 0 (not a Claude model)", gpt)
	}
}

func TestUsageSpendFromStore(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	stateDir := t.TempDir()
	dailyDir := filepath.Join(stateDir, "tokens", "daily")
	if err := os.MkdirAll(dailyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"day":"2026-10-11","agent":"a1","src":"claude","model":"claude-opus-4-8","reqs":1,"in":1000000,"out":0,"cache_write":0,"cache_read":0}`,
		`{"day":"2026-10-11","agent":"a2","src":"claude","model":"claude-haiku-5","reqs":1,"in":1000000,"out":0,"cache_write":0,"cache_read":0}`,
		`{"day":"2026-10-11","agent":"a3","src":"codex","model":"gpt-6","reqs":1,"in":1000000,"out":0,"cache_write":0,"cache_read":0}`,
	}
	if err := os.WriteFile(filepath.Join(dailyDir, "2026.jsonl"), []byte(join(lines)), 0o644); err != nil {
		t.Fatal(err)
	}
	spend, ok := usageSpend(stateDir, now)
	if !ok {
		t.Fatal("usageSpend ok = false, want true")
	}
	// All three rows' tokens count; only the two Claude rows are priced
	// (opus $5 + haiku $1 = $6), the Codex row contributes tokens but no cost.
	if spend.TotalTokens != 3_000_000 {
		t.Fatalf("total tokens = %d, want 3000000", spend.TotalTokens)
	}
	if spend.ApiEquivalentUSD != 6 {
		t.Fatalf("apiEquivalentUsd = %v, want 6", spend.ApiEquivalentUSD)
	}
	if len(spend.ByModel) != 2 {
		t.Fatalf("byModel = %v, want 2 Claude families", spend.ByModel)
	}
	if spend.ByModel[0].Model != "opus" || spend.ByModel[0].ApiEquivalentUSD != 5 {
		t.Fatalf("top model = %+v, want opus $5", spend.ByModel[0])
	}
}

func join(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}
