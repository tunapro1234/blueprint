package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
