package claudeacct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func offsets(plan phasePlan, now time.Time) map[int]time.Duration {
	out := map[int]time.Duration{}
	for slot, start := range plan.start {
		out[slot] = start.Sub(now)
	}
	return out
}

// assertSpaced checks that the planned starts sit on one grid step apart.
func assertSpaced(t *testing.T, plan phasePlan, n int) {
	t.Helper()
	if plan.step != KeepAlivePeriod/time.Duration(n) {
		t.Fatalf("step %v for %d accounts", plan.step, n)
	}
	var phases []time.Duration
	for _, start := range plan.start {
		phases = append(phases, time.Duration(start.UnixNano())%KeepAlivePeriod)
	}
	sort.Slice(phases, func(i, j int) bool { return phases[i] < phases[j] })
	for i := range phases {
		next := phases[(i+1)%len(phases)]
		gap := (next - phases[i] + KeepAlivePeriod) % KeepAlivePeriod
		if len(phases) > 1 && gap != plan.step {
			t.Fatalf("phases %v not %v apart", phases, plan.step)
		}
	}
}

func TestPlanPhasesFourIdleAccountsStepOneFifteen(t *testing.T) {
	now := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	plan := planPhases([]keepMember{{4, now, true}, {2, now, true}, {1, now, true}, {3, now, true}})
	if plan.step != 75*time.Minute {
		t.Fatalf("step %v", plan.step)
	}
	got := offsets(plan, now)
	want := map[int]time.Duration{1: 0, 2: 75 * time.Minute, 3: 150 * time.Minute, 4: 225 * time.Minute}
	for slot, off := range want {
		if got[slot] != off {
			t.Fatalf("offsets %v, want %v", got, want)
		}
	}
	assertSpaced(t, plan, 4)

	two := planPhases([]keepMember{{1, now, true}, {2, now, true}})
	if two.step != 150*time.Minute || offsets(two, now)[2] != 150*time.Minute {
		t.Fatalf("two accounts: %v", offsets(two, now))
	}
}

func TestPlanPhasesKeepsStaggeredWindows(t *testing.T) {
	now := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	ready := []time.Duration{10 * time.Minute, 85 * time.Minute, 160 * time.Minute, 235 * time.Minute}
	var members []keepMember
	for i, r := range ready {
		members = append(members, keepMember{i + 1, now.Add(r), true})
	}
	plan := planPhases(members)
	if plan.idle != 0 {
		t.Fatalf("staggered windows should need no idle time, got %v", plan.idle)
	}
	for i, r := range ready {
		if plan.start[i+1] != now.Add(r) {
			t.Fatalf("slot %d start %v, want its reset", i+1, plan.start[i+1])
		}
	}
	// A late ping drifts the phase; the plan follows instead of waiting a
	// whole period for the old grid.
	members[0].ready = now.Add(14 * time.Minute)
	plan = planPhases(members)
	if plan.start[1] != now.Add(14*time.Minute) || plan.idle != 3*4*time.Minute {
		t.Fatalf("drifted plan %v idle %v", offsets(plan, now), plan.idle)
	}
	assertSpaced(t, plan, 4)
}

func TestPlanPhasesLiveLikeState(t *testing.T) {
	now := time.Date(2030, 1, 1, 19, 52, 0, 0, time.UTC)
	members := []keepMember{
		{1, now.Add(4*time.Hour + 8*time.Minute), true},
		{2, now, true},
		{3, now.Add(4*time.Hour + 48*time.Minute), true},
		{4, now, true},
	}
	plan := planPhases(members)
	assertSpaced(t, plan, 4)
	for _, m := range members {
		if plan.start[m.slot].Before(m.ready) {
			t.Fatalf("slot %d starts %v before it is ready %v", m.slot, plan.start[m.slot], m.ready)
		}
	}
	// The plan keeps total idle time lowest: the first account to reset
	// restarts at once and the idle ones fill the grid around it.
	if offsets(plan, now)[1] != 4*time.Hour+8*time.Minute || plan.idle != 306*time.Minute {
		t.Fatalf("plan %v idle %v", offsets(plan, now), plan.idle)
	}
}

func TestPlanPhasesUnknownMembersTakeLeftoverPositions(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	plan := planPhases([]keepMember{{1, now, true}, {2, now, false}, {3, now.Add(100 * time.Minute), true}})
	if plan.idle != 0 {
		t.Fatalf("idle %v", plan.idle)
	}
	assertSpaced(t, plan, 3)
	if len(plan.start) != 3 {
		t.Fatalf("unknown member has no position: %v", plan.start)
	}
	if empty := planPhases([]keepMember{{1, now, false}}); len(empty.start) != 0 {
		t.Fatalf("all-unknown plan should be empty: %v", empty.start)
	}
}

func TestPlanPhasesRotationMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for round := 0; round < 400; round++ {
		n := 2 + rng.Intn(6)
		step := KeepAlivePeriod / time.Duration(n)
		// Rotations are optimal when every window is known; with unknown
		// members they are only a valid fallback.
		withUnknown := round%2 == 1
		var members []keepMember
		for i := 0; i < n; i++ {
			ready := now
			if rng.Intn(3) > 0 {
				ready = now.Add(time.Duration(rng.Int63n(int64(KeepAlivePeriod))))
			}
			members = append(members, keepMember{i + 1, ready, !withUnknown || rng.Intn(4) > 0})
		}
		for _, m := range members {
			if !m.known {
				continue
			}
			_, brute := bestAssignment(members, m.ready, step)
			assign, rotated := rotatedAssignment(members, m.ready, step)
			seen := map[int]bool{}
			for _, position := range assign {
				seen[position] = true
			}
			if len(seen) != n || rotated < brute || (!withUnknown && rotated != brute) {
				t.Fatalf("round %d anchor %v: rotation %v %v, brute force %v", round, m.ready, assign, rotated, brute)
			}
		}
	}
}

// pingRecorder is a fake Pinger.
type pingRecorder struct {
	mu     sync.Mutex
	tokens []string
	homes  []string
	models []string
	fail   map[string]error
	// after runs after each ping (to start the window in the fake API).
	after func(token string)
}

func (p *pingRecorder) ping(_ context.Context, req PingRequest) error {
	p.mu.Lock()
	p.tokens = append(p.tokens, req.Token)
	p.homes = append(p.homes, req.Home)
	p.models = append(p.models, req.Model)
	p.mu.Unlock()
	if err := p.fail[req.Token]; err != nil {
		return err
	}
	if p.after != nil {
		p.after(req.Token)
	}
	return nil
}

const idleUsage = `{"five_hour":{"utilization":0,"resets_at":null},"seven_day":null}`

// keepFixture has four accounts (4 live) whose cached usage says which
// windows run; the fake API starts a window when its account is pinged.
func keepFixture(t *testing.T, resets map[int]time.Duration) (*fixture, *pingRecorder) {
	t.Helper()
	f := newFixture(t, false)
	f.addAccounts(4)
	accounts, err := f.m.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 4; n++ {
		window := &Window{}
		if off, ok := resets[n]; ok {
			window = &Window{30, f.now.Add(off)}
		}
		accounts.Slot(n).LastUsage = &UsageCache{Usage: &Usage{FiveHour: window}, FetchedAt: f.now, AttemptAt: f.now}
		f.api.mu.Lock()
		f.api.usage[f.account(n).access] = idleUsage
		f.api.mu.Unlock()
	}
	if err := f.m.Store.Save(accounts); err != nil {
		t.Fatal(err)
	}
	rec := &pingRecorder{fail: map[string]error{}}
	rec.after = func(token string) { f.startWindow(token) }
	f.m.Pinger = rec.ping
	return f, rec
}

// startWindow makes the fake usage endpoint report a window started now.
func (f *fixture) startWindow(token string) {
	f.api.mu.Lock()
	defer f.api.mu.Unlock()
	f.api.usage[token] = fmt.Sprintf(`{"five_hour":{"utilization":1,"resets_at":%q},"seven_day":null}`, f.now.Add(KeepAlivePeriod).Format(time.RFC3339))
}

func outputJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestKeepAlivePingsDueIdleAccountAndRecordsWindow(t *testing.T) {
	f, rec := keepFixture(t, map[int]time.Duration{1: time.Hour})
	result, err := f.m.KeepAliveOnce(testCtx(t), KeepAliveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Slot 1 restarts in an hour; of the idle 2, 3 and 4 the first takes
	// the position at now and the others wait for theirs.
	if len(rec.tokens) != 1 || rec.tokens[0] != f.account(2).access {
		t.Fatalf("pinged %d accounts", len(rec.tokens))
	}
	if rec.models[0] != DefaultKeepAliveModel || rec.homes[0] != f.m.Store.KeepAliveHome() {
		t.Fatalf("ping model %q home %q", rec.models[0], rec.homes[0])
	}
	if result.Step != 75*time.Minute {
		t.Fatalf("step %v", result.Step)
	}
	var pinged *KeepAliveSlotView
	for i := range result.Slots {
		if result.Slots[i].Slot == 2 {
			pinged = &result.Slots[i]
		}
	}
	if pinged == nil || !pinged.Pinged || !pinged.Started || !pinged.Active || pinged.Error != "" {
		t.Fatalf("slot 2 view %+v", pinged)
	}
	state, err := f.m.Store.LoadKeepAliveState()
	if err != nil {
		t.Fatal(err)
	}
	entry := state.peek(2)
	if !entry.LastPingAt.Equal(f.now) || !entry.HoldUntil.Equal(f.now.Add(keepAliveSettle)) || entry.Failures != 0 {
		t.Fatalf("state %+v", entry)
	}
	assertMode(t, filepath.Join(f.m.Store.Root(), "keepalive-state.json"), 0o600)
	raw, _ := os.ReadFile(filepath.Join(f.m.Store.Root(), "keepalive-state.json"))
	assertNoToken(t, "keepalive", outputJSON(t, result), string(raw))

	// The next pass waits for the planned starts of 3 and 4.
	rec.tokens = nil
	f.now = f.now.Add(time.Minute)
	if _, err := f.m.KeepAliveOnce(testCtx(t), KeepAliveOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(rec.tokens) != 0 {
		t.Fatalf("pinged again before any planned start: %d", len(rec.tokens))
	}
}

func TestKeepAliveRefetchesStaleUsageBeforePinging(t *testing.T) {
	f, rec := keepFixture(t, map[int]time.Duration{1: time.Hour, 3: 150 * time.Minute, 4: 225 * time.Minute})
	// Slot 2 looks idle on old data, but its window started since.
	f.startWindow(f.account(2).access)
	f.now = f.now.Add(5 * time.Minute)
	result, err := f.m.KeepAliveOnce(testCtx(t), KeepAliveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.tokens) != 0 {
		t.Fatalf("pinged an account whose window had started")
	}
	for _, view := range result.Slots {
		if view.Slot == 2 && !view.Active {
			t.Fatalf("fresh usage not used: %+v", view)
		}
	}
}

func TestKeepAliveLiveAccountUsesLiveToken(t *testing.T) {
	f, rec := keepFixture(t, map[int]time.Duration{1: 75 * time.Minute, 2: 150 * time.Minute, 3: 225 * time.Minute})
	if _, err := f.m.KeepAliveOnce(testCtx(t), KeepAliveOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(rec.tokens) != 1 || rec.tokens[0] != f.liveTokens().AccessToken {
		t.Fatalf("live account not pinged with the live token")
	}
	before := f.liveTokens()

	// An expired live token is never refreshed by bp: the account is skipped.
	f2, rec2 := keepFixture(t, map[int]time.Duration{1: 75 * time.Minute, 2: 150 * time.Minute, 3: 225 * time.Minute})
	expired := f2.account(4)
	expired.expiresAt = f2.now.Add(-time.Minute).UnixMilli()
	f2.writeLive(expired)
	result, err := f2.m.KeepAliveOnce(testCtx(t), KeepAliveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec2.tokens) != 0 {
		t.Fatalf("pinged with an expired live token")
	}
	refreshes, _ := f2.api.calls()
	if len(refreshes) != 0 {
		t.Fatalf("refreshed the live login")
	}
	for _, view := range result.Slots {
		if view.Slot == 4 && !strings.Contains(view.Skipped, "expired") {
			t.Fatalf("slot 4 view %+v", view)
		}
	}
	if after := f.liveTokens(); after.AccessToken != before.AccessToken || after.RefreshToken != before.RefreshToken {
		t.Fatalf("live credentials changed")
	}
}

func TestKeepAliveRefreshesExpiringStoredLogin(t *testing.T) {
	f, rec := keepFixture(t, map[int]time.Duration{1: 75 * time.Minute, 3: 150 * time.Minute, 4: 225 * time.Minute})
	creds, err := f.m.Store.ReadCredentials(2)
	if err != nil {
		t.Fatal(err)
	}
	tokens := creds.Tokens()
	tokens.ExpiresAt = f.now.Add(time.Minute).UnixMilli()
	creds.SetTokens(tokens)
	if err := f.m.Store.WriteCredentials(2, creds); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.KeepAliveOnce(testCtx(t), KeepAliveOptions{}); err != nil {
		t.Fatal(err)
	}
	rotated := f.slotTokens(2).AccessToken
	if rotated == f.account(2).access || len(rec.tokens) != 1 || rec.tokens[0] != rotated {
		t.Fatalf("stored login not refreshed and used for the ping")
	}
}

func TestKeepAliveFailureBacksOff(t *testing.T) {
	f, rec := keepFixture(t, map[int]time.Duration{1: 75 * time.Minute, 3: 150 * time.Minute, 4: 225 * time.Minute})
	rec.fail[f.account(2).access] = errors.New("claude failed: exit status 1")
	result, err := f.m.KeepAliveOnce(testCtx(t), KeepAliveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := f.m.Store.LoadKeepAliveState()
	entry := state.peek(2)
	if entry.Failures != 1 || !entry.HoldUntil.Equal(f.now.Add(keepAliveRetry)) || entry.Error == "" {
		t.Fatalf("state after failure %+v", entry)
	}
	if result.Slots[1].Error == "" || result.Slots[1].Pinged {
		t.Fatalf("view %+v", result.Slots[1])
	}

	// Held while backing off.
	rec.tokens = nil
	f.now = f.now.Add(time.Minute)
	if _, err := f.m.KeepAliveOnce(testCtx(t), KeepAliveOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(rec.tokens) != 0 {
		t.Fatalf("retried during backoff")
	}
	// Retried after it, with a longer wait on another failure.
	f.now = f.now.Add(2 * time.Minute)
	if _, err := f.m.KeepAliveOnce(testCtx(t), KeepAliveOptions{}); err != nil {
		t.Fatal(err)
	}
	state, _ = f.m.Store.LoadKeepAliveState()
	if entry := state.peek(2); len(rec.tokens) != 1 || entry.Failures != 2 || !entry.HoldUntil.Equal(f.now.Add(2*keepAliveRetry)) {
		t.Fatalf("second failure %+v after %d pings", entry, len(rec.tokens))
	}
	for i := 0; i < 10; i++ {
		state.slot(2).failed(f.now, "x")
	}
	if wait := state.peek(2).HoldUntil.Sub(f.now); wait != keepAliveMaxGap {
		t.Fatalf("backoff not capped: %v", wait)
	}
}

func TestKeepAliveAnsweredWithoutWindowIsAnError(t *testing.T) {
	f, rec := keepFixture(t, map[int]time.Duration{1: 75 * time.Minute, 3: 150 * time.Minute, 4: 225 * time.Minute})
	rec.after = nil
	result, err := f.m.KeepAliveOnce(testCtx(t), KeepAliveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.tokens) != 1 || !strings.Contains(result.Slots[1].Error, "no five-hour window") {
		t.Fatalf("view %+v", result.Slots[1])
	}
}

func TestKeepAliveDryRunAndDisabled(t *testing.T) {
	f, rec := keepFixture(t, nil)
	if _, err := f.m.SetDisabled(testCtx(t), 2, true); err != nil {
		t.Fatal(err)
	}
	result, err := f.m.KeepAliveOnce(testCtx(t), KeepAliveOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.tokens) != 0 {
		t.Fatalf("dry run pinged")
	}
	if _, err := os.Stat(filepath.Join(f.m.Store.Root(), "keepalive-state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote state: %v", err)
	}
	if result.Step != 100*time.Minute || len(result.Slots) != 3 {
		t.Fatalf("disabled account planned: step %v slots %d", result.Step, len(result.Slots))
	}
	due := 0
	for _, view := range result.Slots {
		if view.Slot == 2 {
			t.Fatalf("disabled account in plan")
		}
		if view.Due {
			due++
		}
	}
	if due != 1 {
		t.Fatalf("%d due in the dry run, want 1", due)
	}

	if _, err := f.m.KeepAliveOnce(testCtx(t), KeepAliveOptions{Model: "sonnet"}); err != nil {
		t.Fatal(err)
	}
	if len(rec.tokens) != 1 || rec.tokens[0] != f.account(1).access || rec.models[0] != "sonnet" {
		t.Fatalf("pings %d", len(rec.tokens))
	}
}

func TestExecPingerRunsIsolatedClaudeAndRedacts(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "keep-home")
	record := filepath.Join(dir, "record")
	script := `#!/bin/sh
{ printf '%s\n' "$PWD"; printf '%s\n' "$@"; env | grep -E '^(CLAUDE|ANTHROPIC)' | sort; } > "` + record + `"
if [ "$FAIL" = 1 ]; then echo "bad token $CLAUDE_CODE_OAUTH_TOKEN" >&2; exit 3; fi
echo OK
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("ANTHROPIC_API_KEY", "nope")
	t.Setenv("FAIL", "")
	token := fakeToken("access", 9)
	pinger := ExecPinger(Env{UserHome: func() (string, error) { return dir, nil }})
	if err := pinger(testCtx(t), PingRequest{Token: token, Model: "haiku", Home: home}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(record)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if lines[0] != home {
		t.Fatalf("cwd %q", lines[0])
	}
	got := strings.Join(lines[1:], "|")
	for _, want := range []string{"-p|--model|haiku|", "--tools||", "--strict-mcp-config", "--setting-sources||", "--no-session-persistence", "|ok|", "CLAUDE_CONFIG_DIR=" + home, "CLAUDE_CODE_OAUTH_TOKEN=" + token} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, strings.ReplaceAll(got, token, "<t>"))
		}
	}
	if strings.Contains(got, "CHILD_SESSION") || strings.Contains(got, "ANTHROPIC") {
		t.Fatalf("inherited Claude env leaked into the ping")
	}
	assertMode(t, home, 0o700)

	t.Setenv("FAIL", "1")
	err := pinger(testCtx(t), PingRequest{Token: token, Model: "haiku", Home: home})
	if err == nil || !strings.Contains(err.Error(), "bad token <token>") {
		t.Fatalf("error %v", err)
	}
	assertNoToken(t, "ping error", err.Error())

	t.Setenv("PATH", "/nonexistent")
	if _, err := ClaudeBinary(Env{UserHome: func() (string, error) { return dir, nil }}); err == nil {
		t.Fatalf("found claude without PATH or ~/.local/bin")
	}
	if err := os.MkdirAll(filepath.Join(dir, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "claude"), filepath.Join(dir, ".local", "bin", "claude")); err != nil {
		t.Fatal(err)
	}
	if path, err := ClaudeBinary(Env{UserHome: func() (string, error) { return dir, nil }}); err != nil || path != filepath.Join(dir, ".local", "bin", "claude") {
		t.Fatalf("~/.local/bin fallback: %q %v", path, err)
	}
}

// TestPlanPhasesSimulatedDay runs the planner once a minute for two days:
// four accounts start idle, each ping starts a window whose reset the API
// floors to ten minutes (as observed). The windows settle 75 minutes apart
// and stay there with little idle time.
func TestPlanPhasesSimulatedDay(t *testing.T) {
	start := time.Date(2030, 1, 1, 9, 3, 0, 0, time.UTC)
	resets := map[int]time.Time{}
	idle := map[int]time.Duration{}
	pings := 0
	for now := start; now.Before(start.Add(48 * time.Hour)); now = now.Add(time.Minute) {
		var members []keepMember
		for slot := 1; slot <= 4; slot++ {
			ready := now
			if now.Before(resets[slot]) {
				ready = resets[slot]
			}
			members = append(members, keepMember{slot, ready, true})
		}
		plan := planPhases(members)
		for slot := 1; slot <= 4; slot++ {
			if now.Before(resets[slot]) {
				continue
			}
			if now.After(start.Add(24 * time.Hour)) {
				idle[slot] += time.Minute
			}
			if !plan.start[slot].After(now.Add(keepAliveSlack)) {
				resets[slot] = now.Add(KeepAlivePeriod).Truncate(10 * time.Minute)
				pings++
			}
		}
	}
	var phases []time.Duration
	for _, reset := range resets {
		phases = append(phases, time.Duration(reset.UnixNano())%KeepAlivePeriod)
	}
	sort.Slice(phases, func(i, j int) bool { return phases[i] < phases[j] })
	for i := range phases {
		gap := (phases[(i+1)%4] - phases[i] + KeepAlivePeriod) % KeepAlivePeriod
		if gap < 65*time.Minute || gap > 85*time.Minute {
			t.Fatalf("resets not staggered: phases %v", phases)
		}
	}
	for slot, d := range idle {
		// Flooring shortens each window by up to ten minutes; the plan
		// may hold an account that long to keep the spacing.
		if d > 24*time.Hour/KeepAlivePeriod*15*time.Minute {
			t.Fatalf("slot %d idle %v on the second day", slot, d)
		}
	}
	t.Logf("pings %d, second-day idle %v, phases %v", pings, idle, phases)
}

func TestPlanKeepsPreviousGridWhileNearlyAsGood(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	members := []keepMember{{1, now, true}, {2, now.Add(2*time.Hour + 20*time.Minute), true}}
	best := planPhasesNear(members, time.Time{})
	if best.idle != 10*time.Minute || !best.anchor.Equal(now) {
		t.Fatalf("best = idle %s anchor %s", best.idle, best.anchor)
	}
	near := planPhasesNear(members, now.Add(5*time.Minute))
	if near.idle != 20*time.Minute || !near.start[1].Equal(now.Add(5*time.Minute)) {
		t.Fatalf("near = idle %s start %v", near.idle, near.start)
	}
	far := planPhasesNear(members, now.Add(30*time.Minute))
	if far.idle != best.idle || !far.anchor.Equal(best.anchor) {
		t.Fatalf("far kept a worse grid: idle %s", far.idle)
	}
}

func TestKeepAliveHidesFailureFromBeforeRunningWindow(t *testing.T) {
	f, _ := keepFixture(t, map[int]time.Duration{1: 75 * time.Minute, 3: 150 * time.Minute, 4: 225 * time.Minute})
	// Slot 1's window started 225 minutes ago.
	for _, c := range []struct {
		pinged time.Duration
		shown  bool
	}{{-4 * time.Hour, false}, {-time.Hour, true}} {
		state := KeepAliveState{}
		entry := state.slot(1)
		entry.LastPingAt, entry.Error = f.now.Add(c.pinged), "claude failed: exit status 1"
		if err := f.m.Store.SaveKeepAliveState(state); err != nil {
			t.Fatal(err)
		}
		result, err := f.m.KeepAliveOnce(testCtx(t), KeepAliveOptions{DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if view := result.Slots[0]; !view.Active || (view.Error != "") != c.shown {
			t.Fatalf("pinged %s: view %+v", c.pinged, view)
		}
	}
}
