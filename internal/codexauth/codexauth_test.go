package codexauth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// now is the reference clock for every case; the fixtures are written relative
// to it so the table reads like the real timeline.
var now = time.Date(2026, 8, 10, 13, 15, 0, 0, time.UTC)

// tokenMarker appears inside every fixture token so a leak into Reason (or into
// any other string field) is detectable.
const tokenMarker = "SECRETTOKENMATERIAL"

func jwt(t *testing.T, claims map[string]any) string {
	t.Helper()
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString([]byte("sig-"+tokenMarker))
}

func expiring(t *testing.T, at time.Time) string {
	t.Helper()
	return jwt(t, map[string]any{"exp": at.Unix(), "email": "someone@example.com", "jti": tokenMarker})
}

// writeAuth writes a fixture shaped like the real file, including the
// access_token that must not influence the verdict.
func writeAuth(t *testing.T, lastRefresh, idExp, accessExp time.Time) string {
	t.Helper()
	file := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"last_refresh":   lastRefresh.Format(time.RFC3339Nano),
		"tokens": map[string]any{
			"id_token":      expiring(t, idExp),
			"access_token":  expiring(t, accessExp),
			"refresh_token": "rt_" + tokenMarker,
			"account_id":    "acct_" + tokenMarker,
		},
	}
	return writeRaw(t, mustJSON(t, file))
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(data)
}

func writeRaw(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestCheckAt(t *testing.T) {
	cases := []struct {
		name       string
		path       func(t *testing.T) string
		want       Status
		wantReason string
	}{
		{
			name: "healthy session refreshed minutes ago",
			path: func(t *testing.T) string {
				return writeAuth(t, now.Add(-10*time.Minute), now.Add(50*time.Minute), now.Add(10*24*time.Hour))
			},
			want:       OK,
			wantReason: "last_refresh 10m old, id_token valid for 50m",
		},
		{
			// The measured outage: access_token.exp is a week in the FUTURE, so
			// anything that trusts it calls this healthy. It is not.
			name: "revoked session with future access_token exp",
			path: func(t *testing.T) string {
				return writeAuth(t, now.Add(-72*time.Hour), now.Add(-75*time.Hour), now.Add(8*24*time.Hour))
			},
			want:       Expired,
			wantReason: "last_refresh 3d old (>1d), id_token expired 3d 3h ago",
		},
		{
			// Ordinary idleness: codex simply has not run for over an hour.
			name: "fresh refresh with expired id_token is only stale",
			path: func(t *testing.T) string {
				return writeAuth(t, now.Add(-95*time.Minute), now.Add(-35*time.Minute), now.Add(9*24*time.Hour))
			},
			want:       Stale,
			wantReason: "id_token expired 35m ago, last_refresh 1h 35m old; awaiting next refresh",
		},
		{
			name: "old refresh with live id_token is only stale",
			path: func(t *testing.T) string {
				return writeAuth(t, now.Add(-40*time.Hour), now.Add(30*time.Minute), now.Add(5*24*time.Hour))
			},
			want:       Stale,
			wantReason: "last_refresh 1d 16h old (>1d) but id_token still valid for 30m",
		},
		{
			name: "missing file",
			path: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "auth.json")
			},
			want:       Unknown,
			wantReason: "auth.json not found at ",
		},
		{
			name: "unresolvable path",
			path: func(t *testing.T) string {
				return ""
			},
			want:       Unknown,
			wantReason: "CODEX_HOME could not be resolved",
		},
		{
			name: "malformed json",
			path: func(t *testing.T) string {
				return writeRaw(t, `{"last_refresh": `)
			},
			want:       Unknown,
			wantReason: "auth.json is not valid JSON",
		},
		{
			name: "no last_refresh",
			path: func(t *testing.T) string {
				return writeRaw(t, mustJSON(t, map[string]any{"tokens": map[string]any{"id_token": expiring(t, now.Add(time.Hour))}}))
			},
			want:       Unknown,
			wantReason: "auth.json has no last_refresh",
		},
		{
			name: "last_refresh is not a timestamp",
			path: func(t *testing.T) string {
				return writeRaw(t, mustJSON(t, map[string]any{
					"last_refresh": "yesterday",
					"tokens":       map[string]any{"id_token": expiring(t, now.Add(time.Hour))},
				}))
			},
			want:       Unknown,
			wantReason: "auth.json last_refresh is not an RFC3339 time",
		},
		{
			name: "no id_token",
			path: func(t *testing.T) string {
				return writeRaw(t, mustJSON(t, map[string]any{
					"last_refresh": now.Add(-5 * time.Minute).Format(time.RFC3339Nano),
					"tokens":       map[string]any{"access_token": expiring(t, now.Add(240*time.Hour))},
				}))
			},
			want:       Unknown,
			wantReason: "auth.json has no tokens.id_token",
		},
		{
			name: "id_token claims segment is not base64url",
			path: func(t *testing.T) string {
				// The live file's refresh_token has exactly this shape: three
				// dot-separated segments whose middle is not decodable.
				return writeRaw(t, mustJSON(t, map[string]any{
					"last_refresh": now.Add(-5 * time.Minute).Format(time.RFC3339Nano),
					"tokens":       map[string]any{"id_token": "aaa." + tokenMarker + "!!!.ccc"},
				}))
			},
			want:       Unknown,
			wantReason: "id_token claims segment is not base64url",
		},
		{
			name: "id_token is not a jwt",
			path: func(t *testing.T) string {
				return writeRaw(t, mustJSON(t, map[string]any{
					"last_refresh": now.Add(-5 * time.Minute).Format(time.RFC3339Nano),
					"tokens":       map[string]any{"id_token": "opaque-" + tokenMarker},
				}))
			},
			want:       Unknown,
			wantReason: "id_token is not a three-segment JWT",
		},
		{
			name: "id_token claims are not json",
			path: func(t *testing.T) string {
				body := base64.RawURLEncoding.EncodeToString([]byte("not json " + tokenMarker))
				return writeRaw(t, mustJSON(t, map[string]any{
					"last_refresh": now.Add(-5 * time.Minute).Format(time.RFC3339Nano),
					"tokens":       map[string]any{"id_token": "aaa." + body + ".ccc"},
				}))
			},
			want:       Unknown,
			wantReason: "id_token claims segment is not JSON",
		},
		{
			name: "id_token has no exp claim",
			path: func(t *testing.T) string {
				return writeRaw(t, mustJSON(t, map[string]any{
					"last_refresh": now.Add(-5 * time.Minute).Format(time.RFC3339Nano),
					"tokens":       map[string]any{"id_token": jwt(t, map[string]any{"sub": tokenMarker})},
				}))
			},
			want:       Unknown,
			wantReason: "id_token claims carry no exp",
		},
		{
			name: "last_refresh in the future yields no verdict",
			path: func(t *testing.T) string {
				return writeAuth(t, now.Add(2*time.Hour), now.Add(3*time.Hour), now.Add(240*time.Hour))
			},
			want:       Unknown,
			wantReason: "clock skew, no verdict",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			state := CheckAt(testCase.path(t), now)
			if state.Status != testCase.want {
				t.Fatalf("status = %q (reason %q), want %q", state.Status, state.Reason, testCase.want)
			}
			if !strings.Contains(state.Reason, testCase.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", state.Reason, testCase.wantReason)
			}
			// No case may report healthy unless it is the healthy fixture.
			if state.Status == OK && testCase.want != OK {
				t.Fatalf("unexpected healthy verdict: %+v", state)
			}
			// Nothing derived from a token, nor the account id, may surface.
			rendered := fmt.Sprintf("%+v", state)
			if strings.Contains(rendered, tokenMarker) {
				t.Fatalf("token material leaked into state: %q", rendered)
			}
			if strings.Contains(rendered, "acct_") || strings.Contains(rendered, "rt_") {
				t.Fatalf("account id or refresh token leaked into state: %q", rendered)
			}
			if state.Checked != now {
				t.Fatalf("checked clock = %v, want %v", state.Checked, now)
			}
		})
	}
}

// Broken must be true only for the one state that proves access is gone,
// because it is what makes bp claim "ERISIM YOK" to a user.
func TestBrokenOnlyForExpired(t *testing.T) {
	for status, want := range map[Status]bool{Unknown: false, OK: false, Stale: false, Expired: true} {
		if got := (State{Status: status}).Broken(); got != want {
			t.Fatalf("Broken() for %q = %v, want %v", status, got, want)
		}
	}
}

// The verdict must rest on last_refresh and id_token only: an access_token that
// expired long ago cannot turn a freshly refreshed session into a fault, just
// as a future one cannot rescue a dead session (the case above).
func TestAccessTokenExpIsNotEvidence(t *testing.T) {
	healthy := CheckAt(writeAuth(t, now.Add(-10*time.Minute), now.Add(50*time.Minute), now.Add(-30*24*time.Hour)), now)
	if healthy.Status != OK {
		t.Fatalf("expired access_token changed the verdict: %+v", healthy)
	}
	if !healthy.IDTokenExp.Equal(now.Add(50 * time.Minute)) {
		t.Fatalf("id_token exp = %v, want %v", healthy.IDTokenExp, now.Add(50*time.Minute))
	}
	if !healthy.LastRefresh.Equal(now.Add(-10 * time.Minute)) {
		t.Fatalf("last_refresh = %v, want %v", healthy.LastRefresh, now.Add(-10*time.Minute))
	}
}

func TestHomeUsesCodexHomeEnv(t *testing.T) {
	t.Setenv("CODEX_HOME", "/custom/codex")
	if got := Home(); got != "/custom/codex" {
		t.Fatalf("Home() = %q, want /custom/codex", got)
	}
	if got := Path(); got != "/custom/codex/auth.json" {
		t.Fatalf("Path() = %q, want /custom/codex/auth.json", got)
	}
	t.Setenv("CODEX_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if got := Home(); got != filepath.Join(home, ".codex") {
		t.Fatalf("Home() = %q, want %q", got, filepath.Join(home, ".codex"))
	}
}

func TestShortAge(t *testing.T) {
	cases := map[time.Duration]string{
		0:                            "0m",
		29 * time.Second:             "0m",
		30 * time.Second:             "1m",
		90 * time.Second:             "2m",
		time.Hour:                    "1h",
		95 * time.Minute:             "1h 35m",
		24 * time.Hour:               "1d",
		72 * time.Hour:               "3d",
		75 * time.Hour:               "3d 3h",
		-75 * time.Hour:              "3d 3h",
		40 * time.Hour:               "1d 16h",
		7*24*time.Hour + 1*time.Hour: "7d 1h",
	}
	for input, want := range cases {
		if got := shortAge(input); got != want {
			t.Fatalf("shortAge(%v) = %q, want %q", input, got, want)
		}
	}
}
