package claudeacct

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseUsageWindowsAndScopedLimits(t *testing.T) {
	body := `{"five_hour":{"utilization":42.5,"resets_at":"2030-01-02T05:00:00.123456+00:00"},
		"seven_day":null,
		"seven_day_opus":{"utilization":null,"resets_at":null},
		"limits":[{"scope":{"model":{"display_name":"Opus"}},"percent":77,"resets_at":"2030-01-05T00:00:00Z"},{"scope":{}}]}`
	u, err := ParseUsage([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if u.FiveHour == nil || u.FiveHour.Utilization != 42.5 || u.FiveHour.ResetsAt.IsZero() || u.SevenDay != nil {
		t.Fatalf("windows %+v %+v", u.FiveHour, u.SevenDay)
	}
	if len(u.Scoped) != 1 || u.Scoped[0].Name != "Opus" || u.Scoped[0].Percent != 77 {
		t.Fatalf("scoped %+v", u.Scoped)
	}
	now := time.Date(2030, 1, 2, 3, 0, 0, 0, time.UTC)
	if v, ok := u.Max(now); !ok || v != 42.5 {
		t.Fatalf("max %v %v", v, ok)
	}
	// A window whose reset time passed counts as 0.
	if v, _ := u.Max(now.Add(3 * time.Hour)); v != 0 {
		t.Fatalf("max after reset %v", v)
	}
	null, err := ParseUsage([]byte(`{"five_hour":{"utilization":null},"seven_day":{"utilization":3}}`))
	if err != nil || null.FiveHour != nil || null.SevenDay == nil {
		t.Fatalf("%+v %v", null, err)
	}
	if v, ok := (&Usage{}).Max(now); ok || v != 0 {
		t.Fatal("empty usage reported a value")
	}
	for _, bad := range []string{`[]`, `nope`, `null`} {
		if _, err := ParseUsage([]byte(bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	out := UsageLine(&UsageCache{Usage: null, FetchedAt: now.Add(-2 * time.Minute)}, now)
	if out != "5h n/a  7d 3%  updated 2m ago" {
		t.Fatalf("line %q", out)
	}
}

func TestFetchUsageErrorsAndRetryAfter(t *testing.T) {
	f := newFixture(t, false)
	token := fakeToken("access", 1)
	f.api.usageStatus[token] = [2]string{"429", "240"}
	_, err := f.m.Client.FetchUsage(testCtx(t), token)
	var usageErr *UsageError
	if !errors.As(err, &usageErr) || usageErr.Kind != "http-429" || !usageErr.HasRetry || usageErr.RetryAfter != 4*time.Minute {
		t.Fatalf("err %#v", err)
	}
	cache := recordUsage(nil, f.now, nil, err)
	if cache.Error != "http-429" || !cache.BackoffUntil.Equal(f.now.Add(4*time.Minute)) || cache.due(f.now.Add(3*time.Minute), 0) {
		t.Fatalf("cache %+v", cache)
	}
	if line := UsageLine(cache, f.now); line != "5h n/a  7d n/a  usage: error (http-429, retry 4m)" {
		t.Fatalf("line %q", line)
	}
	// Without Retry-After the backoff doubles up to the cap.
	f.api.usageStatus[token] = [2]string{"500", ""}
	_, err = f.m.Client.FetchUsage(testCtx(t), token)
	cache = recordUsage(nil, f.now, nil, err)
	if cache.Error != "http-500" || !cache.BackoffUntil.Equal(f.now.Add(time.Minute)) {
		t.Fatalf("first backoff %+v", cache)
	}
	for i := 0; i < 10; i++ {
		cache = recordUsage(cache, f.now, nil, err)
	}
	if !cache.BackoffUntil.Equal(f.now.Add(30 * time.Minute)) {
		t.Fatalf("capped backoff %v", cache.BackoffUntil.Sub(f.now))
	}
	cache = recordUsage(cache, f.now, &Usage{}, nil)
	if cache.Error != "" || cache.Failures != 0 || !cache.BackoffUntil.IsZero() {
		t.Fatalf("success did not clear %+v", cache)
	}
	// Garbage answers are bad-response; dead endpoints are network.
	f.api.usage[token] = `not json`
	delete(f.api.usageStatus, token)
	if _, err := f.m.Client.FetchUsage(testCtx(t), token); !errors.As(err, &usageErr) || usageErr.Kind != "bad-response" {
		t.Fatalf("bad body: %v", err)
	}
	dead := &Client{UsageURL: "http://127.0.0.1:1/usage"}
	if _, err := dead.FetchUsage(testCtx(t), token); !errors.As(err, &usageErr) || usageErr.Kind != "network" {
		t.Fatalf("network: %v", err)
	}
}

func TestListShowsPerAccountErrorsWithoutFailing(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(2)
	f.api.usageStatus[fakeToken("access", 1)] = [2]string{"429", "240"}
	f.api.setUsage(fakeToken("access", 2), 12, 34)
	o, err := f.m.Overview(testCtx(t), OverviewOptions{Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	out := render(o)
	if !strings.Contains(out, "usage: error (http-429, retry 4m)") || !strings.Contains(out, "5h 12%") {
		t.Fatalf("listing:\n%s", out)
	}
	assertNoToken(t, "list", out)
}

func TestRefreshClassification(t *testing.T) {
	f := newFixture(t, false)
	cases := []struct {
		status, code, kind string
		dead               bool
	}{
		{"400", "invalid_grant", "invalid_grant", true},
		{"401", "invalid_grant", "invalid_grant", true},
		{"403", "invalid_client", "invalid_client", false},
		{"500", "invalid_grant", "http-500", false},
		{"400", "invalid_request", "http-400", false},
	}
	for i, c := range cases {
		token := fakeToken("refresh", 500+i)
		f.api.refreshFail[token] = [2]string{c.status, c.code}
		_, err := f.m.Client.Refresh(testCtx(t), Tokens{RefreshToken: token})
		var refreshErr *RefreshError
		if !errors.As(err, &refreshErr) || refreshErr.Kind != c.kind || refreshErr.Dead() != c.dead {
			t.Fatalf("%+v: %v", c, err)
		}
		assertNoToken(t, "refresh error", err.Error(), fmt.Sprintf("%v %+v %#v", err, err, err))
	}
	_, err := f.m.Client.Refresh(testCtx(t), Tokens{AccessToken: fakeToken("access", 1)})
	var refreshErr *RefreshError
	if !errors.As(err, &refreshErr) || refreshErr.Kind != "no_refresh_token" || !refreshErr.Dead() {
		t.Fatalf("no refresh token: %v", err)
	}
	// Rotation: a new refresh token replaces the old one; scopes follow.
	next, err := f.m.Client.Refresh(testCtx(t), Tokens{RefreshToken: fakeToken("refresh", 1), Scopes: []string{"old"}})
	if err != nil || next.RefreshToken == fakeToken("refresh", 1) || next.ExpiresAt != f.now.UnixMilli()+28800000 || len(next.Scopes) != 2 {
		t.Fatalf("rotation %v", err)
	}
	assertNoToken(t, "token formatting", fmt.Sprintf("%v %+v %#v %s", next, next, next, next))
}

func TestDecideTable(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	slot := func(n int, five, seven float64, flags ...string) Slot {
		s := Slot{Number: n, AccountUUID: fmt.Sprint("a", n)}
		if five >= 0 {
			s.LastUsage = &UsageCache{Usage: &Usage{FiveHour: &Window{five, reset}, SevenDay: &Window{seven, reset}}}
		}
		for _, flag := range flags {
			switch flag {
			case "disabled":
				s.Disabled = true
			case "dead":
				s.Dead = true
			}
		}
		return s
	}
	policy := AutoPolicy{Threshold: 90, Cooldown: 5 * time.Minute}
	recent := AutoState{LastSwitchAt: now.Add(-time.Minute)}
	old := AutoState{LastSwitchAt: now.Add(-10 * time.Minute)}
	cases := []struct {
		name   string
		slots  []Slot
		active int
		state  AutoState
		action string
		to     int
	}{
		{"below threshold", []Slot{slot(1, 50, 89), slot(2, 0, 0)}, 1, old, AutoNone, 0},
		{"5h at threshold", []Slot{slot(1, 90, 10), slot(2, 20, 20)}, 1, old, AutoSwitch, 2},
		{"7d over threshold", []Slot{slot(1, 10, 95), slot(2, 20, 20), slot(3, 5, 5)}, 1, old, AutoSwitch, 3},
		{"hysteresis blocks near-equal target", []Slot{slot(1, 92, 0), slot(2, 85, 0)}, 1, old, AutoNoTarget, 0},
		{"hysteresis allows ten points", []Slot{slot(1, 95, 0), slot(2, 85, 0)}, 1, old, AutoSwitch, 2},
		{"cooldown", []Slot{slot(1, 99, 0), slot(2, 0, 0)}, 1, recent, AutoNone, 0},
		{"no previous switch", []Slot{slot(1, 99, 0), slot(2, 0, 0)}, 1, AutoState{}, AutoSwitch, 2},
		{"all exhausted", []Slot{slot(1, 99, 0), slot(2, 95, 0), slot(3, 100, 100)}, 1, old, AutoNoTarget, 0},
		{"disabled skipped", []Slot{slot(1, 99, 0), slot(2, 0, 0, "disabled"), slot(3, 50, 0)}, 1, old, AutoSwitch, 3},
		{"dead skipped", []Slot{slot(1, 99, 0), slot(2, 0, 0, "dead")}, 1, old, AutoNoTarget, 0},
		{"unknown target usage skipped", []Slot{slot(1, 99, 0), slot(2, -1, 0)}, 1, old, AutoNoTarget, 0},
		{"unknown active usage", []Slot{slot(1, -1, 0), slot(2, 0, 0)}, 1, old, AutoNone, 0},
		{"unmanaged live login", []Slot{slot(1, 99, 0), slot(2, 0, 0)}, 0, old, AutoNone, 0},
	}
	for _, c := range cases {
		d := Decide(&Accounts{Slots: c.slots}, c.active, c.state, policy, now)
		if d.Action != c.action || d.To != c.to {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
}

func TestAutoOnceSwitchesAndRecordsCooldown(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(3) // slot 3 live
	f.api.setUsage(fakeToken("access", 1), 30, 40)
	f.api.setUsage(fakeToken("access", 2), 10, 20)
	f.api.setUsage(fakeToken("access", 3), 95, 50)
	policy := AutoPolicy{Threshold: 90, Cooldown: 5 * time.Minute}
	dry, err := f.m.AutoOnce(testCtx(t), AutoOptions{Policy: policy, DryRun: true})
	if err != nil || dry.Decision.Action != AutoNone {
		t.Fatalf("dry run without data: %+v %v", dry, err)
	}
	// One poll per pass: the active account first.
	res, err := f.m.AutoOnce(testCtx(t), AutoOptions{Policy: policy, PollEvery: 5 * time.Minute, MaxPolls: 1})
	if err != nil || len(res.Polled) != 1 || res.Polled[0] != 3 || res.Decision.Action != AutoNoTarget {
		t.Fatalf("first pass %+v %v", res, err)
	}
	// The next tick polls the account with the oldest data (slot 1, 40%),
	// which is enough to leave the exhausted account.
	f.now = f.now.Add(time.Minute)
	res, err = f.m.AutoOnce(testCtx(t), AutoOptions{Policy: policy, PollEvery: 5 * time.Minute, MaxPolls: 1})
	if err != nil || len(res.Polled) != 1 || res.Polled[0] != 1 || res.Decision.Action != AutoSwitch || res.Switched == nil || res.Switched.To.Number != 1 || res.Switched.From != 3 {
		t.Fatalf("switch pass %+v %v", res, err)
	}
	if f.liveIdentity().AccountUUID != "acct-1" {
		t.Fatal("auto did not switch the live login")
	}
	// Slot 1 is now over the threshold, but the cooldown holds.
	f.setCachedUsage(1, 99, 99)
	res, err = f.m.AutoOnce(testCtx(t), AutoOptions{Policy: policy, PollEvery: time.Hour})
	if err != nil || res.Decision.Action != AutoNone || !strings.Contains(res.Decision.Reason, "cooling down") {
		t.Fatalf("cooldown pass %+v %v", res, err)
	}
	// After the cooldown, the best account below the threshold wins.
	f.now = f.now.Add(10 * time.Minute)
	f.setCachedUsage(1, 99, 99)
	res, err = f.m.AutoOnce(testCtx(t), AutoOptions{Policy: policy, PollEvery: time.Hour})
	if err != nil || res.Switched == nil || res.Switched.To.Number != 2 {
		t.Fatalf("after cooldown %+v %v", res, err)
	}
	data, _ := json.Marshal(res)
	assertNoToken(t, "auto json", string(data))
}

func TestNoTokenInAnyRendering(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(2)
	f.api.setUsage(fakeToken("access", 1), 1, 2)
	f.api.setUsage(fakeToken("access", 2), 3, 4)
	o, err := f.m.Overview(testCtx(t), OverviewOptions{Fetch: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	RenderList(&buf, o)
	state, _ := f.m.Store.LoadAutoState()
	RenderStatus(&buf, o, state)
	data, _ := json.Marshal(o)
	creds, _ := f.m.Store.ReadCredentials(1)
	res, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "1"})
	if err != nil {
		t.Fatal(err)
	}
	resJSON, _ := json.Marshal(res)
	assertNoToken(t, "renderings", buf.String(), string(data), string(resJSON), fmt.Sprintf("%v %+v %#v", creds, creds, creds), fmt.Sprintf("%+v", res))
	// Errors for unreadable files never echo their content.
	if _, err := ParseCredentials([]byte(`{"claudeAiOauth":"` + fakeToken("access", 1) + `"`)); err == nil {
		t.Fatal("parsed garbage")
	} else {
		assertNoToken(t, "parse error", err.Error())
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second: "0s", 45 * time.Second: "45s", 4 * time.Minute: "4m", 3 * time.Hour: "3h",
		3*time.Hour + 12*time.Minute: "3h12m", 53 * time.Hour: "2d5h", 48 * time.Hour: "2d",
	} {
		if got := FormatDuration(d); got != want {
			t.Errorf("%v: %q want %q", d, got, want)
		}
	}
}

func TestDecideAndStrategiesHonourPerAccountLimits(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	slot := func(n int, alias, email string, five float64) Slot {
		return Slot{Number: n, AccountUUID: fmt.Sprint("a", n), Alias: alias, Email: email,
			LastUsage: &UsageCache{Usage: &Usage{FiveHour: &Window{five, reset}}}}
	}
	policy := AutoPolicy{Threshold: 90, Limits: map[string]float64{"HUSEYIN": 40, "cerci@example.com": 40, "9": 50}}
	if got := policy.LimitFor(slot(3, "huseyin", "", 0)); got != 40 {
		t.Fatalf("alias limit %v", got)
	}
	if got := policy.LimitFor(slot(4, "", "Cerci@Example.com", 0)); got != 40 {
		t.Fatalf("email limit %v", got)
	}
	if got := policy.LimitFor(slot(9, "", "", 0)); got != 50 {
		t.Fatalf("number limit %v", got)
	}
	if got := (AutoPolicy{Threshold: 30, Limits: policy.Limits}).LimitFor(slot(3, "huseyin", "", 0)); got != 30 {
		t.Fatalf("a lower threshold wins: %v", got)
	}
	old := AutoState{LastSwitchAt: now.Add(-time.Hour)}
	cases := []struct {
		name   string
		slots  []Slot
		active int
		action string
		to     int
	}{
		{"capped account over its limit switches", []Slot{slot(1, "team", "", 20), slot(3, "huseyin", "", 41)}, 3, AutoSwitch, 1},
		{"capped account under its limit stays", []Slot{slot(1, "team", "", 20), slot(3, "huseyin", "", 39)}, 3, AutoNone, 0},
		{"capped target with no room skipped", []Slot{slot(1, "team", "", 95), slot(3, "huseyin", "", 45), slot(2, "azra", "", 70)}, 1, AutoSwitch, 2},
		{"room not raw usage ranks targets", []Slot{slot(1, "team", "", 95), slot(3, "huseyin", "", 5), slot(2, "azra", "", 40)}, 1, AutoSwitch, 2},
		{"only capped targets full", []Slot{slot(1, "team", "", 95), slot(3, "huseyin", "", 40)}, 1, AutoNoTarget, 0},
	}
	for _, c := range cases {
		d := Decide(&Accounts{Slots: c.slots}, c.active, old, policy, now)
		if d.Action != c.action || d.To != c.to {
			t.Errorf("%s: %+v", c.name, d)
		}
	}

	accounts := &Accounts{Slots: []Slot{slot(1, "team", "", 30), slot(3, "huseyin", "", 10), slot(4, "", "cerci@example.com", 45)}}
	best := strategyCandidates(accounts, &liveLogin{}, StrategyBest, policy.Limits, now)
	if len(best) != 3 || best[0] != 1 || best[1] != 3 {
		t.Fatalf("best with caps %v", best)
	}
	next := strategyCandidates(accounts, &liveLogin{}, StrategyNextAvailable, policy.Limits, now)
	for _, n := range next {
		if n == 4 {
			t.Fatalf("next-available chose an account over its cap: %v", next)
		}
	}
}
