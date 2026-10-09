package claudeacct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Keepalive keeps every usable account's five-hour window running and
// staggers the windows so their resets are spread evenly over the period.
//
// A five-hour window starts with the first request after the previous one
// ran out, so an idle account has no window and no reset to wait for. A
// keepalive pass sends a one-word prompt through Claude Code to each account
// whose window has run out, at the moment the phase plan gives it. With N
// accounts the plan puts the window starts KeepAlivePeriod/N apart (4
// accounts: 1h15m), so under heavy use a fresh reset is never more than one
// step away.
//
// bp can only delay a window, never shorten one. The plan is therefore
// rebuilt on every pass from the times each account can next start a window
// (now when idle, its reset when running): of all evenly spaced grids, it
// takes the one that keeps accounts idle for the least total time. When the
// windows are already staggered that grid is the current one and idle
// accounts are pinged as soon as their window runs out.

const (
	// KeepAlivePeriod is the length of the five-hour window.
	KeepAlivePeriod = 5 * time.Hour
	// DefaultKeepAliveModel is the model the keepalive prompt uses.
	DefaultKeepAliveModel = "haiku"
	// keepAliveSlack lets a planned start count as due a little early, so
	// a pass a few seconds before the planned time does not wait a tick.
	keepAliveSlack = 30 * time.Second
	// keepAliveSettle is how long a successful ping holds off another one
	// while the usage endpoint catches up with the new window.
	keepAliveSettle  = 10 * time.Minute
	keepAliveRetry   = 2 * time.Minute
	keepAliveMaxGap  = 30 * time.Minute
	keepAliveTimeout = 2 * time.Minute
	// keepAliveFresh is the oldest usage a ping is decided on.
	keepAliveFresh = 2 * time.Minute
	// keepAliveBruteForce is the largest account count whose plan is found
	// by trying every assignment; larger counts keep the accounts' order.
	keepAliveBruteForce = 7
)

// keepMember is one account in the phase plan.
type keepMember struct {
	slot int
	// ready is the earliest start of the account's next window: now when
	// it has none, its reset while one runs.
	ready time.Time
	// known is false when the account's five-hour window is unknown; it
	// takes whichever position is left and costs nothing.
	known bool
}

// phasePlan assigns each account the start of its next window.
type phasePlan struct {
	step   time.Duration
	anchor time.Time
	start map[int]time.Time
	// idle is the total time the plan leaves accounts without a window.
	idle time.Duration
}

// planPhases finds the evenly spaced grid (KeepAlivePeriod/N apart) and the
// assignment of accounts to it that keeps the total idle time lowest. Some
// account in an optimal plan starts exactly when it is ready, so the grid
// anchors tried are the members' ready times. Ties go to the first anchor and
// assignment tried (members in slot order), so the plan is deterministic.
func planPhases(members []keepMember) phasePlan { return planPhasesNear(members, time.Time{}) }

// keepAliveStickiness is how much more idle time a plan may cost before the
// grid of the previous plan is given up. Without it two nearly equal grids
// take turns as time moves and the planned starts jump between ticks.
const keepAliveStickiness = 10 * time.Minute

// planPhasesNear is planPhases that keeps the previous grid (prev, any point
// on it) while it costs at most keepAliveStickiness more than the best one.
func planPhasesNear(members []keepMember, prev time.Time) phasePlan {
	n := len(members)
	plan := phasePlan{start: map[int]time.Time{}}
	if n == 0 {
		return plan
	}
	sorted := append([]keepMember(nil), members...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].slot < sorted[j].slot })
	plan.step = KeepAlivePeriod / time.Duration(n)
	var anchors []time.Time
	for _, m := range sorted {
		if m.known {
			anchors = append(anchors, m.ready)
		}
	}
	if len(anchors) == 0 {
		return plan
	}
	best := time.Duration(-1)
	var bestAnchor time.Time
	var bestAssign []int
	for _, anchor := range anchors {
		assign, cost := assignPositions(sorted, anchor, plan.step)
		if best < 0 || cost < best {
			best, bestAnchor, bestAssign = cost, anchor, assign
		}
	}
	if !prev.IsZero() {
		if assign, cost := assignPositions(sorted, prev, plan.step); cost <= best+keepAliveStickiness {
			best, bestAnchor, bestAssign = cost, prev, assign
		}
	}
	plan.idle = best
	plan.anchor = bestAnchor
	for i, m := range sorted {
		grid := bestAnchor.Add(time.Duration(bestAssign[i]) * plan.step)
		if !m.known {
			plan.start[m.slot] = grid
			continue
		}
		plan.start[m.slot] = m.ready.Add(waitFor(grid, m.ready))
	}
	return plan
}

// waitFor is how long after ready the grid position next comes round.
func waitFor(grid, ready time.Time) time.Duration {
	wait := grid.Sub(ready) % KeepAlivePeriod
	if wait < 0 {
		wait += KeepAlivePeriod
	}
	return wait
}

// assignPositions gives each member a distinct grid position, returning the
// position per member and the total wait.
func assignPositions(members []keepMember, anchor time.Time, step time.Duration) ([]int, time.Duration) {
	if len(members) <= keepAliveBruteForce {
		return bestAssignment(members, anchor, step)
	}
	return rotatedAssignment(members, anchor, step)
}

// positionCost is how long a member waits for a grid position; an unknown
// member costs nothing anywhere.
func positionCost(m keepMember, anchor time.Time, step time.Duration, position int) time.Duration {
	if !m.known {
		return 0
	}
	return waitFor(anchor.Add(time.Duration(position)*step), m.ready)
}

// bestAssignment tries every assignment.
func bestAssignment(members []keepMember, anchor time.Time, step time.Duration) ([]int, time.Duration) {
	positions := make([]int, len(members))
	for i := range positions {
		positions[i] = i
	}
	best := time.Duration(-1)
	var bestAssign []int
	permute(positions, 0, func(p []int) {
		var total time.Duration
		for i, position := range p {
			total += positionCost(members[i], anchor, step, position)
		}
		if best < 0 || total < best {
			best, bestAssign = total, append([]int(nil), p...)
		}
	})
	return bestAssign, best
}

// rotatedAssignment keeps the order in which members become ready (after
// the anchor) and tries each rotation of it, for counts too large to try
// every assignment.
func rotatedAssignment(members []keepMember, anchor time.Time, step time.Duration) ([]int, time.Duration) {
	n := len(members)
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return waitFor(members[order[i]].ready, anchor) < waitFor(members[order[j]].ready, anchor)
	})
	best := time.Duration(-1)
	var bestAssign []int
	for shift := 0; shift < n; shift++ {
		assign := make([]int, n)
		var total time.Duration
		for rank, member := range order {
			assign[member] = (rank + shift) % n
			total += positionCost(members[member], anchor, step, assign[member])
		}
		if best < 0 || total < best {
			best, bestAssign = total, assign
		}
	}
	return bestAssign, best
}

// permute calls visit with every permutation of p[k:] (Heap-free swap
// recursion, lexicographic enough for deterministic ties).
func permute(p []int, k int, visit func([]int)) {
	if k == len(p) {
		visit(p)
		return
	}
	for i := k; i < len(p); i++ {
		p[k], p[i] = p[i], p[k]
		permute(p, k+1, visit)
		p[k], p[i] = p[i], p[k]
	}
}

// windowState reads a slot's five-hour window at now.
func windowState(s Slot, now time.Time) (known, active bool, resetsAt time.Time) {
	u := slotUsage(s)
	if u == nil || u.FiveHour == nil {
		return false, false, time.Time{}
	}
	w := u.FiveHour
	if w.ResetsAt.IsZero() || !now.Before(w.ResetsAt) {
		return true, false, time.Time{}
	}
	return true, true, w.ResetsAt
}

// KeepAliveState is keepalive-state.json: the outcome of past pings.
type KeepAliveState struct {
	Slots map[string]*KeepAliveSlot `json:"slots,omitempty"`
	// Anchor is a point on the grid the last plan used, kept so the next
	// plan stays on it (see keepAliveStickiness).
	Anchor time.Time `json:"anchor,omitempty"`
}

// KeepAliveSlot is one account's ping history.
type KeepAliveSlot struct {
	LastPingAt time.Time `json:"lastPingAt,omitempty"`
	Error      string    `json:"error,omitempty"`
	Failures   int       `json:"failures,omitempty"`
	// HoldUntil keeps the account from being pinged again: after a ping
	// while usage catches up, after a failure until the retry.
	HoldUntil time.Time `json:"holdUntil,omitempty"`
}

func (s *KeepAliveState) slot(n int) *KeepAliveSlot {
	if s.Slots == nil {
		s.Slots = map[string]*KeepAliveSlot{}
	}
	key := strconv.Itoa(n)
	if s.Slots[key] == nil {
		s.Slots[key] = &KeepAliveSlot{}
	}
	return s.Slots[key]
}

func (s *KeepAliveState) peek(n int) KeepAliveSlot {
	if entry := s.Slots[strconv.Itoa(n)]; entry != nil {
		return *entry
	}
	return KeepAliveSlot{}
}

func (k *KeepAliveSlot) failed(now time.Time, reason string) {
	k.Failures++
	k.Error = reason
	wait := keepAliveRetry << min(k.Failures-1, 4)
	if wait > keepAliveMaxGap {
		wait = keepAliveMaxGap
	}
	k.HoldUntil = now.Add(wait)
}

func (s *Store) keepAliveStateFile() string { return filepath.Join(s.root, "keepalive-state.json") }

// KeepAliveHome is the private Claude Code config home keepalive prompts run
// in, so they never touch the live login or its history.
func (s *Store) KeepAliveHome() string { return filepath.Join(s.root, "keepalive-home") }

// LoadKeepAliveState reads keepalive-state.json; missing means no pings yet.
func (s *Store) LoadKeepAliveState() (KeepAliveState, error) {
	var state KeepAliveState
	data, err := os.ReadFile(s.keepAliveStateFile())
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return KeepAliveState{}, fmt.Errorf("parse %s: %v", s.keepAliveStateFile(), err)
	}
	return state, nil
}

// SaveKeepAliveState writes keepalive-state.json atomically.
func (s *Store) SaveKeepAliveState(state KeepAliveState) error {
	if err := s.ensureRoot(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.keepAliveStateFile(), append(data, '\n'), secretFileMode)
}

// PingRequest is one keepalive prompt.
type PingRequest struct {
	// Token is the account's OAuth access token. It is handed to Claude
	// Code through the environment and never logged or returned.
	Token string
	Model string
	// Home is the private config home and working directory to run in.
	Home string
}

// Pinger sends one keepalive prompt. Tests inject a fake; production runs
// Claude Code (ExecPinger).
type Pinger func(ctx context.Context, req PingRequest) error

// ClaudeBinary finds the claude executable: PATH first, then the native
// installer's ~/.local/bin (a daemon's PATH often lacks it).
func ClaudeBinary(env Env) (string, error) {
	if path, err := exec.LookPath("claude"); err == nil {
		return path, nil
	}
	userHome := env.UserHome
	if userHome == nil {
		userHome = os.UserHomeDir
	}
	if home, err := userHome(); err == nil && home != "" {
		candidate := filepath.Join(home, ".local", "bin", "claude")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", errors.New("claude executable not found in PATH or ~/.local/bin")
}

// ExecPinger runs `claude -p` with the account's token in a private config
// home: no tools, no MCP servers, no settings, no session saved, one word
// asked and answered.
func ExecPinger(env Env) Pinger {
	return func(ctx context.Context, req PingRequest) error {
		binary, err := ClaudeBinary(env)
		if err != nil {
			return err
		}
		if err := ensurePrivateDir(req.Home); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, keepAliveTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-p",
			"--model", req.Model,
			"--no-session-persistence",
			"--tools", "",
			"--strict-mcp-config",
			"--disable-slash-commands",
			"--setting-sources", "",
			"--system-prompt", "Reply with the single word OK.",
			"ok")
		cmd.Dir = req.Home
		cmd.Env = pingEnv(os.Environ(), req)
		var stderr bytes.Buffer
		cmd.Stdout = nil
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			detail := strings.TrimSpace(stderr.String())
			if req.Token != "" {
				detail = strings.ReplaceAll(detail, req.Token, "<token>")
			}
			if len(detail) > 200 {
				detail = detail[:200]
			}
			if ctx.Err() != nil {
				return errors.New("claude timed out")
			}
			if detail != "" {
				return fmt.Errorf("claude failed: %v: %s", err, detail)
			}
			return fmt.Errorf("claude failed: %v", err)
		}
		return nil
	}
}

// pingEnv drops every inherited Claude Code and Anthropic variable (a leaked
// session or API key would change which account pays) and sets the token
// and the private config home.
func pingEnv(base []string, req PingRequest) []string {
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "CLAUDE") || strings.HasPrefix(upper, "ANTHROPIC") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "CLAUDE_CODE_OAUTH_TOKEN="+req.Token, "CLAUDE_CONFIG_DIR="+req.Home)
}

// KeepAliveOptions controls one keepalive pass.
type KeepAliveOptions struct {
	Model string
	// DryRun plans without pinging.
	DryRun bool
}

// KeepAliveSlotView is one account in a keepalive pass.
type KeepAliveSlotView struct {
	Slot  int    `json:"slot"`
	Email string `json:"email"`
	Alias string `json:"alias,omitempty"`
	// Known is false while the account's five-hour window is unknown.
	Known    bool      `json:"known"`
	Active   bool      `json:"active"`
	ResetsAt time.Time `json:"resetsAt,omitempty"`
	// NextStart is when the plan starts the account's next window.
	NextStart time.Time `json:"nextStart,omitempty"`
	Due       bool      `json:"due,omitempty"`
	// Attempted is set when this pass sent the prompt; Pinged when it was
	// answered.
	Attempted bool `json:"attempted,omitempty"`
	Pinged    bool `json:"pinged,omitempty"`
	// Started is set when usage confirmed the pinged window.
	Started    bool      `json:"started,omitempty"`
	Skipped    string    `json:"skipped,omitempty"`
	LastPingAt time.Time `json:"lastPingAt,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// KeepAliveResult is the plan and what a pass did.
type KeepAliveResult struct {
	Step   time.Duration       `json:"step"`
	Anchor time.Time           `json:"anchor,omitempty"`
	Idle  time.Duration       `json:"idle"`
	Slots []KeepAliveSlotView `json:"slots"`
}

type keepAliveJob struct {
	slot  int
	token string
}

// KeepAliveOnce plans the window starts and pings every account whose
// planned start has come and whose window has run out. Disabled and dead
// accounts are left alone. The live account is pinged with the live access
// token and never refreshed; while that token is expired the account is
// skipped (Claude Code refreshes it on its next request). Pings run outside
// the store lock; their usage is fetched afterwards to confirm the window.
func (m *Manager) KeepAliveOnce(ctx context.Context, opts KeepAliveOptions) (KeepAliveResult, error) {
	if opts.Model == "" {
		opts.Model = DefaultKeepAliveModel
	}
	result, jobs, state, err := m.planKeepAlive(ctx, opts)
	if err != nil || opts.DryRun || len(jobs) == 0 {
		return result, err
	}
	pinger := m.Pinger
	if pinger == nil {
		pinger = ExecPinger(m.Env)
	}
	outcome := map[int]error{}
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		outcome[job.slot] = pinger(ctx, PingRequest{Token: job.token, Model: opts.Model, Home: m.Store.KeepAliveHome()})
	}
	unlock, err := m.lockStore(ctx)
	if err != nil {
		return result, err
	}
	defer unlock()
	accounts, err := m.Store.Load()
	if err != nil {
		return result, err
	}
	live, err := m.readLive()
	if err != nil {
		return result, err
	}
	var pinged []int
	for n, pingErr := range outcome {
		if pingErr == nil && accounts.Slot(n) != nil {
			pinged = append(pinged, n)
		}
	}
	sort.Ints(pinged)
	if len(pinged) > 0 {
		m.refreshUsage(ctx, accounts, live, pinged)
		if err := m.Store.Save(accounts); err != nil {
			return result, err
		}
	}
	now := m.now()
	for i := range result.Slots {
		view := &result.Slots[i]
		pingErr, tried := outcome[view.Slot]
		if !tried {
			continue
		}
		entry := state.slot(view.Slot)
		entry.LastPingAt = now
		view.LastPingAt = now
		view.Attempted = true
		if pingErr != nil {
			entry.failed(now, pingErr.Error())
			view.Error = entry.Error
			continue
		}
		view.Pinged = true
		slot := accounts.Slot(view.Slot)
		if slot == nil {
			continue
		}
		known, active, resetsAt := windowState(*slot, now)
		view.Known, view.Active, view.ResetsAt = known, active, resetsAt
		if active {
			view.Started = true
			entry.Error, entry.Failures, entry.HoldUntil = "", 0, now.Add(keepAliveSettle)
			continue
		}
		if cache := slot.LastUsage; cache != nil && cache.Error != "" && !cache.AttemptAt.Before(now.Add(-time.Minute)) {
			// The window may well have started; usage could not say.
			entry.Error, entry.HoldUntil = "", now.Add(keepAliveSettle)
			continue
		}
		entry.failed(now, "the prompt was answered but no five-hour window started")
		view.Error = entry.Error
	}
	if err := m.Store.SaveKeepAliveState(state); err != nil {
		return result, err
	}
	return result, nil
}

// planKeepAlive builds the plan under the store lock and collects the tokens
// of the accounts to ping (refreshing a stored login that is about to expire,
// as a usage fetch would). An account that looks due on usage older than
// keepAliveFresh is refetched first, so a window someone has started since is
// not pinged again, and the plan is rebuilt on the fresh data.
func (m *Manager) planKeepAlive(ctx context.Context, opts KeepAliveOptions) (KeepAliveResult, []keepAliveJob, KeepAliveState, error) {
	unlock, err := m.lockStore(ctx)
	if err != nil {
		return KeepAliveResult{}, nil, KeepAliveState{}, err
	}
	defer unlock()
	accounts, err := m.Store.Load()
	if err != nil {
		return KeepAliveResult{}, nil, KeepAliveState{}, err
	}
	live, err := m.readLive()
	if err != nil {
		return KeepAliveResult{}, nil, KeepAliveState{}, err
	}
	state, err := m.Store.LoadKeepAliveState()
	if err != nil {
		return KeepAliveResult{}, nil, KeepAliveState{}, err
	}
	result := buildKeepAlive(accounts, state, m.now())
	if !opts.DryRun {
		var stale []int
		for _, view := range result.Slots {
			pingable := view.Due && view.Skipped == ""
			if (pingable || !view.Known) && accounts.Slot(view.Slot).LastUsage.due(m.now(), keepAliveFresh) {
				stale = append(stale, view.Slot)
			}
		}
		if len(stale) > 0 {
			m.refreshUsage(ctx, accounts, live, stale)
			if err := m.Store.Save(accounts); err != nil {
				return result, nil, state, err
			}
			result = buildKeepAlive(accounts, state, m.now())
		}
	}
	if opts.DryRun {
		return result, nil, state, nil
	}
	if !result.Anchor.IsZero() && !result.Anchor.Equal(state.Anchor) {
		state.Anchor = result.Anchor
		if err := m.Store.SaveKeepAliveState(state); err != nil {
			return result, nil, state, err
		}
	}
	now := m.now()
	current := live.liveSlot(accounts)
	var jobs []keepAliveJob
	changed := false
	for i := range result.Slots {
		view := &result.Slots[i]
		if !view.Due || view.Skipped != "" {
			continue
		}
		token, reason := m.keepAliveToken(ctx, accounts, accounts.Slot(view.Slot), current, live, now, &changed)
		if reason != "" {
			view.Skipped = reason
			continue
		}
		jobs = append(jobs, keepAliveJob{slot: view.Slot, token: token})
	}
	if changed {
		if err := m.Store.Save(accounts); err != nil {
			return result, nil, state, err
		}
	}
	return result, jobs, state, nil
}

// buildKeepAlive plans the usable accounts' next windows at now and marks
// which are due for a ping.
func buildKeepAlive(accounts *Accounts, state KeepAliveState, now time.Time) KeepAliveResult {
	var members []keepMember
	for _, s := range accounts.Slots {
		if !s.Usable() {
			continue
		}
		known, _, resetsAt := windowState(s, now)
		ready := now
		if !resetsAt.IsZero() {
			ready = resetsAt
		}
		members = append(members, keepMember{slot: s.Number, ready: ready, known: known})
	}
	plan := planPhasesNear(members, state.Anchor)
	result := KeepAliveResult{Step: plan.step, Anchor: plan.anchor, Idle: plan.idle}
	for _, s := range accounts.Slots {
		if !s.Usable() {
			continue
		}
		known, active, resetsAt := windowState(s, now)
		past := state.peek(s.Number)
		view := KeepAliveSlotView{Slot: s.Number, Email: s.Email, Alias: s.Alias, Known: known, Active: active, ResetsAt: resetsAt,
			NextStart: plan.start[s.Number], LastPingAt: past.LastPingAt, Error: past.Error}
		if active && past.Error != "" && !past.LastPingAt.After(resetsAt.Add(-KeepAlivePeriod)) {
			// A failure from before the running window started is history,
			// not the account's state.
			view.Error = ""
		}
		switch {
		case !known:
			view.Skipped = "five-hour window unknown until its usage is fetched"
		case active:
		case view.NextStart.After(now.Add(keepAliveSlack)):
		case now.Before(past.HoldUntil):
			view.Due = true
			view.Skipped = "held until " + past.HoldUntil.Format(time.RFC3339)
		default:
			view.Due = true
		}
		result.Slots = append(result.Slots, view)
	}
	return result
}

// keepAliveToken returns the access token to ping a slot with, or why it
// cannot be pinged now.
func (m *Manager) keepAliveToken(ctx context.Context, accounts *Accounts, s, current *Slot, live *liveLogin, now time.Time, changed *bool) (string, string) {
	if current != nil && current.Number == s.Number {
		if live.parsed == nil {
			return "", "the live login has no readable token"
		}
		tokens := live.parsed.Tokens()
		if tokens.AccessToken == "" || expiresWithin(tokens, now, time.Minute) {
			return "", "the live login's token has expired; Claude Code refreshes it on its next request"
		}
		return tokens.AccessToken, ""
	}
	creds, err := m.Store.ReadCredentials(s.Number)
	if err != nil {
		return "", "stored login unreadable"
	}
	tokens := creds.Tokens()
	if expiresWithin(tokens, now, refreshMargin) {
		slot := accounts.Slot(s.Number)
		rotated, err := m.refreshStored(ctx, slot, creds)
		*changed = true
		if err != nil {
			slot.LastUsage = recordUsage(slot.LastUsage, now, nil, err)
			return "", "login refresh failed"
		}
		tokens = rotated
	}
	return tokens.AccessToken, ""
}
