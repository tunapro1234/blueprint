// Package codexauth answers one question offline: can this machine still talk
// to the Codex API at all? The usage collector writes `codex_5h: null` both
// when nobody ran codex and when nobody CAN run it (a revoked OAuth session
// answers 401), and those two states look identical downstream. The local
// session file distinguishes them without any network call.
//
// The package touches nothing but $CODEX_HOME/auth.json: no network, no tmux,
// no process inspection, so a later `bp doctor` can reuse it verbatim.
//
// It never returns, logs, or formats token material or the account id. Only
// timestamps, durations, the file path, and JSON field names ever reach a
// Reason string.
package codexauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// staleAfter is how long last_refresh may lag before the session counts as
// unmaintained.
//
// Measured on the live file (2026-08-10): id_token.exp is exactly
// last_refresh+1h, and the file's mtime equals last_refresh, so codex rewrites
// auth.json every time it refreshes and a session in use is rewritten roughly
// hourly. A single file cannot show the historical cadence - there is no
// rotation or backup to compare against - so the hourly figure is taken from
// that 1h id_token lifetime and from the operator's report, not from observed
// consecutive refreshes.
//
// 24h is therefore ~24 refresh intervals of headroom: long enough that an
// overnight quiet spell never accuses a healthy session, short enough that a
// revoked session surfaces the same day. The residual ambiguity is real and
// unavoidable offline: a machine that is genuinely idle for a day looks exactly
// like one whose refresh has been failing for a day. That is why the verdict
// below is reported as a state plus the timestamps it used, so a caller can
// show its evidence instead of asserting a cause.
const staleAfter = 24 * time.Hour

// Status is the verdict about the local session.
type Status string

const (
	// Unknown means the file could not be read or understood; no claim is made.
	Unknown Status = "unknown"
	// OK means last_refresh is recent and id_token has not expired.
	OK Status = "ok"
	// Stale means one signal is off but not both: normal between-run expiry, or
	// an unusually old refresh that still holds a live id_token. Not evidence
	// of broken access.
	Stale Status = "stale"
	// Expired means last_refresh has been frozen past staleAfter AND the
	// id_token it minted is already expired: nothing has refreshed this session
	// for a long time, so calls that need it will fail.
	Expired Status = "expired"
)

// State is the full answer, including the evidence it rests on so callers can
// print the timestamps rather than repeat the verdict.
type State struct {
	Status Status
	// Reason is a short human-readable explanation. Guaranteed free of token
	// material and of the account id.
	Reason string
	// Path is the file that was consulted, even when reading it failed.
	Path string
	// LastRefresh is auth.json's last_refresh; zero when absent or unparseable.
	LastRefresh time.Time
	// IDTokenExp is the `exp` claim of tokens.id_token; zero when unavailable.
	//
	// tokens.access_token is deliberately NOT parsed. On the currently broken
	// file its exp is 2026-08-17 - eight days in the future - while the session
	// has been dead since 2026-08-07: a check that trusts it reports a healthy
	// fleet in the middle of a total outage. It is not evidence and this
	// package does not look at it.
	IDTokenExp time.Time
	// Checked is the reference clock the verdict used.
	Checked time.Time
}

// Broken reports whether access is provably unusable, which is the only state
// that justifies telling a user their access is gone.
func (s State) Broken() bool { return s.Status == Expired }

// Home resolves CODEX_HOME for THIS process, falling back to ~/.codex.
//
// cmd/bp/bar.go's codexHome() implements the same fallback but cannot be
// reused: it is in package main, and it deliberately resolves the variable from
// /proc/<pid>/environ for another process, because the bar describes a pane's
// codex, not bp's own environment. This reader describes the machine's session,
// so the rule is mirrored against our own environment instead.
func Home() string {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return home
	}
	if user, _ := os.UserHomeDir(); user != "" {
		return filepath.Join(user, ".codex")
	}
	return ""
}

// Path is the auth.json this package reads.
func Path() string {
	home := Home()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "auth.json")
}

// Check reads the resolved auth.json and judges it against the current clock.
func Check() State { return CheckAt(Path(), time.Now()) }

// CheckAt is Check with the file and clock injected, for tests and for callers
// that already hold a reference time.
func CheckAt(path string, now time.Time) State {
	state := State{Status: Unknown, Path: path, Checked: now}
	if path == "" {
		state.Reason = "CODEX_HOME could not be resolved"
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			state.Reason = "auth.json not found at " + path
		} else {
			// Only the syscall error is surfaced; the file body is not read.
			state.Reason = "auth.json unreadable: " + err.Error()
		}
		return state
	}
	// Only the fields needed for the verdict are decoded. account_id and the
	// refresh token are never bound to a variable.
	var file struct {
		LastRefresh string `json:"last_refresh"`
		Tokens      struct {
			IDToken string `json:"id_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		state.Reason = "auth.json is not valid JSON"
		return state
	}
	if strings.TrimSpace(file.LastRefresh) == "" {
		state.Reason = "auth.json has no last_refresh"
		return state
	}
	lastRefresh, err := time.Parse(time.RFC3339, strings.TrimSpace(file.LastRefresh))
	if err != nil {
		state.Reason = "auth.json last_refresh is not an RFC3339 time"
		return state
	}
	state.LastRefresh = lastRefresh
	if strings.TrimSpace(file.Tokens.IDToken) == "" {
		state.Reason = "auth.json has no tokens.id_token"
		return state
	}
	exp, err := jwtExpiry(file.Tokens.IDToken)
	if err != nil {
		// jwtExpiry's errors describe structure only, never content.
		state.Reason = "id_token " + err.Error()
		return state
	}
	state.IDTokenExp = exp

	refreshAge := now.Sub(lastRefresh)
	if refreshAge < 0 {
		state.Reason = fmt.Sprintf("last_refresh is %s in the future; clock skew, no verdict", shortAge(-refreshAge))
		return state
	}
	staleRefresh := refreshAge > staleAfter
	idExpired := !now.Before(exp)
	switch {
	case staleRefresh && idExpired:
		state.Status = Expired
		state.Reason = fmt.Sprintf("last_refresh %s old (>%s), id_token expired %s ago", shortAge(refreshAge), shortAge(staleAfter), shortAge(now.Sub(exp)))
	case staleRefresh:
		state.Status = Stale
		state.Reason = fmt.Sprintf("last_refresh %s old (>%s) but id_token still valid for %s", shortAge(refreshAge), shortAge(staleAfter), shortAge(exp.Sub(now)))
	case idExpired:
		// The expected shape after an hour of not running codex: the next run
		// refreshes it. Not a fault.
		state.Status = Stale
		state.Reason = fmt.Sprintf("id_token expired %s ago, last_refresh %s old; awaiting next refresh", shortAge(now.Sub(exp)), shortAge(refreshAge))
	default:
		state.Status = OK
		state.Reason = fmt.Sprintf("last_refresh %s old, id_token valid for %s", shortAge(refreshAge), shortAge(exp.Sub(now)))
	}
	return state
}

// jwtExpiry decodes a JWT's claims segment and returns its exp. The token is
// never returned, echoed, or included in an error.
func jwtExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("is not a three-segment JWT")
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, fmt.Errorf("claims segment is not base64url")
	}
	var claims struct {
		Exp *int64 `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return time.Time{}, fmt.Errorf("claims segment is not JSON")
	}
	if claims.Exp == nil {
		return time.Time{}, fmt.Errorf("claims carry no exp")
	}
	return time.Unix(*claims.Exp, 0).UTC(), nil
}

// shortAge formats a duration the way the rest of bp does: coarse, ASCII, and
// never more than two units.
func shortAge(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	d = d.Round(time.Minute)
	days := int(d / (24 * time.Hour))
	hours := int(d % (24 * time.Hour) / time.Hour)
	minutes := int(d % time.Hour / time.Minute)
	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case days > 0:
		return fmt.Sprintf("%dd", days)
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
