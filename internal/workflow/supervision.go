package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// TokenUse is an agent's token usage over a time span. AsOf is how far the
// source had collected usage when it answered; zero when it cannot say.
type TokenUse struct {
	Input      int64
	Output     int64
	CacheWrite int64
	CacheRead  int64
	AsOf       time.Time
}

// Weighted is the budget measure: input + output + cache writes + cache reads
// weighted by percent/100.
func (u TokenUse) Weighted(percent int) int64 {
	return u.Input + u.Output + u.CacheWrite + u.CacheRead*int64(percent)/100
}

// TokenMeter is an optional Driver capability. Tokens reports the agent's
// usage in [since, until); a zero until means now. known is false when the
// harness records no usage or the source cannot vouch for its numbers; the
// engine then never guesses and does not enforce a budget for that agent.
//
// The source may lag: AsOf says how far it had collected usage. Usage in the
// interval after AsOf is not counted yet.
type TokenMeter interface {
	Tokens(ctx context.Context, agent string, since, until time.Time) (TokenUse, bool, error)
}

// TokenMeasure re-measures an agent's usage over a closed interval for
// reporting. It answers known=false when the source has nothing to say (no
// record for the agent, raw usage already expired, no source at all).
type TokenMeasure func(agent string, since, until time.Time) (TokenUse, bool)

// Settled reports whether a recorded unit's tokens are final: the source had
// collected past the unit's finish when it was measured. A row without the
// measured interval's start cannot be re-measured and counts as settled.
func (u UnitTokens) Settled(finished time.Time) bool {
	if u.Since == nil {
		return true
	}
	return u.AsOf != nil && !u.AsOf.Before(finished)
}

// SettleTokens re-measures the token usage of rows that were not final when
// they were written (as_of before finished_at) and returns copies with the
// re-measured numbers. A row whose re-measure is unknown, or lower than what
// was recorded (raw usage partly expired), keeps its recorded numbers. A row
// without tokens was unknown at finish and stays unknown. unsettled counts
// rows whose re-measure is still not final.
func SettleTokens(rows []TimeRow, percent int, measure TokenMeasure) ([]TimeRow, int) {
	settled := make([]TimeRow, len(rows))
	copy(settled, rows)
	unsettled := 0
	for index, row := range settled {
		if row.Tokens == nil || row.Tokens.Settled(row.FinishedAt) {
			continue
		}
		if measure == nil {
			unsettled++
			continue
		}
		use, known := measure(row.Agent, *row.Tokens.Since, row.FinishedAt)
		if !known || use.Weighted(percent) < row.Tokens.Weighted {
			unsettled++
			continue
		}
		tokens := unitTokensFrom(use, percent, *row.Tokens.Since)
		settled[index].Tokens = tokens
		if !tokens.Settled(row.FinishedAt) {
			unsettled++
		}
	}
	return settled, unsettled
}

func unitTokensFrom(use TokenUse, percent int, since time.Time) *UnitTokens {
	tokens := &UnitTokens{Input: use.Input, Output: use.Output, CacheWrite: use.CacheWrite, CacheRead: use.CacheRead, Weighted: use.Weighted(percent), Since: timePointer(since.UTC())}
	if !use.AsOf.IsZero() {
		tokens.AsOf = timePointer(use.AsOf.UTC())
	}
	return tokens
}

// spentUnit is a finished unit of this run as the run budget counts it.
type spentUnit struct {
	agent    string
	since    time.Time
	until    time.Time
	weighted int64
	settled  bool
}

// Event states written by supervision. They carry no unit key, so unit
// bookkeeping (latestEvents, Status counts) ignores them.
const (
	eventBreakerOpen     = "breaker_open"
	eventBreakerHalfOpen = "breaker_half_open"
	eventBreakerClosed   = "breaker_closed"
	eventRunPaused       = "run_paused"
	eventTokensUnknown   = "tokens_unknown"
)

// statusBreakersWaiting prefixes the run status while every agent's breaker is
// open. It is a "waiting:" status, so daemon startup restores the run.
const statusBreakersWaiting = "waiting: all agent breakers open"

// BreakerView is an open or half-open breaker as bp workflow status shows it.
type BreakerView struct {
	Agent      string    `json:"agent"`
	State      string    `json:"state"`
	Failures   int       `json:"failures"`
	ReopensAt  time.Time `json:"reopens_at"`
	LastUnit   string    `json:"last_unit,omitempty"`
	LastReason string    `json:"last_reason,omitempty"`
}

// agentBreaker is derived from the terminal unit events in units.jsonl, so it
// survives a worker restart without a state file. A torn line is skipped by
// the log reader and simply does not count.
type agentBreaker struct {
	consecutive   int
	lastFailure   time.Time
	lastUnit      string
	lastReason    string
	openAnnounced bool
	halfAnnounced bool
}

func (b *agentBreaker) open(failures int) bool { return failures > 0 && b.consecutive >= failures }

type breakerConfig struct {
	failures int
	openFor  time.Duration
}

type meltdownConfig struct {
	failures int
	window   time.Duration
	text     string
}

func breakerSettings(d Definition) *breakerConfig {
	if d.Supervise == nil || d.Supervise.Breaker == nil {
		return nil
	}
	return &breakerConfig{failures: d.Supervise.Breaker.Failures, openFor: duration(d.Supervise.Breaker.OpenFor, 15*time.Minute)}
}

func meltdownSettings(d Definition) *meltdownConfig {
	if d.Supervise == nil || d.Supervise.Meltdown == nil {
		return nil
	}
	return &meltdownConfig{failures: d.Supervise.Meltdown.Failures, window: duration(d.Supervise.Meltdown.Window, 30*time.Minute), text: d.Supervise.Meltdown.Window}
}

// deriveBreakers replays terminal unit events at or after since. Each failed
// unit extends its agent's consecutive count and is a new opening once the
// count reaches the threshold; any success closes the breaker.
func deriveBreakers(events []Event, since time.Time) map[string]*agentBreaker {
	breakers := map[string]*agentBreaker{}
	get := func(agent string) *agentBreaker {
		b := breakers[agent]
		if b == nil {
			b = &agentBreaker{}
			breakers[agent] = b
		}
		return b
	}
	for _, event := range events {
		if event.Agent == "" || event.At.Before(since) {
			continue
		}
		switch {
		case event.Key != "" && terminalState(event.State):
			b := get(event.Agent)
			if event.State == "failed" {
				b.consecutive++
				b.lastFailure, b.lastUnit, b.lastReason = event.At, event.Key, event.Reason
				b.openAnnounced, b.halfAnnounced = false, false
			} else {
				b.consecutive = 0
			}
		case event.State == eventBreakerOpen:
			get(event.Agent).openAnnounced = true
		case event.State == eventBreakerHalfOpen:
			get(event.Agent).halfAnnounced = true
		}
	}
	return breakers
}

// OpenBreakers lists the agents whose breaker is open or half-open now.
func OpenBreakers(d Definition, run Run, events []Event, now time.Time) []BreakerView {
	config := breakerSettings(d)
	if config == nil {
		return nil
	}
	var views []BreakerView
	for agent, b := range deriveBreakers(events, run.SupervisionStart()) {
		if !b.open(config.failures) {
			continue
		}
		view := BreakerView{Agent: agent, State: "open", Failures: b.consecutive, ReopensAt: b.lastFailure.Add(config.openFor), LastUnit: b.lastUnit, LastReason: b.lastReason}
		if !now.Before(view.ReopensAt) {
			view.State = "half-open"
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Agent < views[j].Agent })
	return views
}

// supervision is the engine's in-memory view of breakers, meltdown and token
// spend for the run it is executing. It is rebuilt from the logs at Run start.
type supervision struct {
	mu            sync.Mutex
	def           Definition
	breaker       *breakerConfig
	meltdown      *meltdownConfig
	since         time.Time
	agents        map[string]*agentBreaker
	failures      []time.Time
	unknownTokens map[string]bool
	finished      []spentUnit
	inflight      map[string]time.Time
	unitCeiling   int64
	runCeiling    int64
	percent       int
}

func newSupervision(d Definition, run Run, events []Event, times []TimeRow) *supervision {
	s := &supervision{def: d, breaker: breakerSettings(d), meltdown: meltdownSettings(d), since: run.SupervisionStart(),
		unknownTokens: map[string]bool{}, inflight: map[string]time.Time{}, percent: d.CacheReadWeight()}
	s.agents = deriveBreakers(events, s.since)
	for _, event := range events {
		if event.Key != "" && event.State == "failed" && !event.At.Before(s.since) {
			s.failures = append(s.failures, event.At)
		}
		if event.State == eventTokensUnknown && event.Agent != "" {
			s.unknownTokens[event.Agent] = true
		}
	}
	if d.Budget != nil && d.Budget.UnitTokens != nil {
		s.unitCeiling = *d.Budget.UnitTokens
	}
	if d.Budget != nil && d.Budget.RunTokens != nil {
		s.runCeiling = *d.Budget.RunTokens
	}
	if run.RunTokens > 0 {
		s.runCeiling = run.RunTokens
	}
	for _, row := range times {
		if row.Run == run.ID && row.Tokens != nil {
			s.finished = append(s.finished, spentFromRow(row))
		}
	}
	return s
}

func (s *supervision) budgeted() bool { return s.unitCeiling > 0 || s.runCeiling > 0 }

func (s *supervision) agent(name string) *agentBreaker {
	b := s.agents[name]
	if b == nil {
		b = &agentBreaker{}
		s.agents[name] = b
	}
	return b
}

// gate reports whether the agent may take a new unit now. halfOpen marks the
// single trial unit after open_for; announce is true the first time it is seen.
func (s *supervision) gate(agent string, now time.Time) (allowed, halfOpen, announce bool, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.breaker == nil {
		return true, false, false, time.Time{}
	}
	b := s.agent(agent)
	if !b.open(s.breaker.failures) {
		return true, false, false, time.Time{}
	}
	until = b.lastFailure.Add(s.breaker.openFor)
	if now.Before(until) {
		return false, false, false, until
	}
	announce = !b.halfAnnounced
	b.halfAnnounced = true
	return true, true, announce, until
}

// allOpen reports whether every agent is waiting on an open breaker and when
// the earliest trial becomes possible.
func (s *supervision) allOpen(agents []string, now time.Time) (bool, string, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.breaker == nil || len(agents) == 0 {
		return false, "", time.Time{}
	}
	nextAgent, next := "", time.Time{}
	for _, agent := range agents {
		b := s.agent(agent)
		if !b.open(s.breaker.failures) {
			return false, "", time.Time{}
		}
		until := b.lastFailure.Add(s.breaker.openFor)
		if !now.Before(until) {
			return false, "", time.Time{}
		}
		if next.IsZero() || until.Before(next) {
			nextAgent, next = agent, until
		}
	}
	return true, nextAgent, next
}

type finishOutcome struct {
	opened      bool
	closed      bool
	consecutive int
	until       time.Time
	meltdown    string
}

// recordFinish updates breaker and meltdown state after a terminal unit event
// has been written.
func (s *supervision) recordFinish(agent, unit, result, reason string, finished time.Time) finishOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	var outcome finishOutcome
	b := s.agent(agent)
	threshold := 0
	if s.breaker != nil {
		threshold = s.breaker.failures
	}
	wasOpen := b.open(threshold)
	if result == "failed" {
		b.consecutive++
		b.lastFailure, b.lastUnit, b.lastReason = finished, unit, reason
		b.openAnnounced, b.halfAnnounced = false, false
		if b.open(threshold) {
			outcome.opened, outcome.consecutive, outcome.until = true, b.consecutive, finished.Add(s.breaker.openFor)
			b.openAnnounced = true
		}
		s.failures = append(s.failures, finished)
		if s.meltdown != nil {
			floor := finished.Add(-s.meltdown.window)
			if floor.Before(s.since) {
				floor = s.since
			}
			count := 0
			for _, at := range s.failures {
				if !at.Before(floor) {
					count++
				}
			}
			if count >= s.meltdown.failures {
				outcome.meltdown = fmt.Sprintf("meltdown: %d units failed within %s; last: %s on %s: %s", count, s.meltdown.text, unit, agent, oneLine(reason))
			}
		}
	} else {
		b.consecutive = 0
		outcome.closed = wasOpen
	}
	return outcome
}

// pendingOpenings returns breakers that are open in the log but whose opening
// was never announced (the worker died between the failure and the event).
func (s *supervision) pendingOpenings() map[string]agentBreaker {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := map[string]agentBreaker{}
	if s.breaker == nil {
		return result
	}
	for agent, b := range s.agents {
		if b.open(s.breaker.failures) && !b.openAnnounced {
			b.openAnnounced = true
			result[agent] = *b
		}
	}
	return result
}

func (s *supervision) setInflight(agent string, started time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if started.IsZero() {
		delete(s.inflight, agent)
		return
	}
	s.inflight[agent] = started
}

func (s *supervision) inflightUnits() map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]time.Time, len(s.inflight))
	for agent, started := range s.inflight {
		result[agent] = started
	}
	return result
}

func spentFromRow(row TimeRow) spentUnit {
	unit := spentUnit{agent: row.Agent, until: row.FinishedAt, weighted: row.Tokens.Weighted, settled: row.Tokens.Settled(row.FinishedAt)}
	if row.Tokens.Since != nil {
		unit.since = *row.Tokens.Since
	}
	return unit
}

func (s *supervision) addFinished(row TimeRow) {
	s.mu.Lock()
	s.finished = append(s.finished, spentFromRow(row))
	s.mu.Unlock()
}

// unsettledUnits returns the finished units whose tokens may still grow, by
// index into the finished list.
func (s *supervision) unsettledUnits() map[int]spentUnit {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := map[int]spentUnit{}
	for index, unit := range s.finished {
		if !unit.settled {
			result[index] = unit
		}
	}
	return result
}

// remeasured records a fresh measure of an unsettled unit. A lower figure
// than recorded is ignored: usage does not shrink.
func (s *supervision) remeasured(index int, weighted int64, settled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index >= len(s.finished) {
		return
	}
	if weighted > s.finished[index].weighted {
		s.finished[index].weighted = weighted
	}
	s.finished[index].settled = settled
}

func (s *supervision) finishedSpend() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	for _, unit := range s.finished {
		total += unit.weighted
	}
	return total
}

// markUnknown reports whether this is the first unknown-token observation for
// the agent in this run.
func (s *supervision) markUnknown(agent string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unknownTokens[agent] {
		return false
	}
	s.unknownTokens[agent] = true
	return true
}

func oneLine(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	return truncateUTF8(value, 300)
}

// RunSpend sums the budget measure of the run's finished units in times.jsonl,
// settled through Store.Measure, and returns the effective run ceiling (0 when
// none is configured).
func (s *Store) RunSpend(id string) (spent, ceiling int64, err error) {
	run, err := s.LoadRun(id)
	if err != nil {
		return 0, 0, err
	}
	d, err := ReadDefinition(s.SnapshotDir(id))
	if err != nil {
		return 0, 0, err
	}
	rows, _, err := s.Times(id)
	if err != nil {
		return 0, 0, err
	}
	rows, _ = SettleTokens(rows, d.CacheReadWeight(), s.Measure)
	return weightedSpend(rows), runCeiling(d, run), nil
}

func weightedSpend(rows []TimeRow) int64 {
	var spent int64
	for _, row := range rows {
		if row.Tokens != nil {
			spent += row.Tokens.Weighted
		}
	}
	return spent
}

func runCeiling(d Definition, run Run) int64 {
	if run.RunTokens > 0 {
		return run.RunTokens
	}
	if d.Budget != nil && d.Budget.RunTokens != nil {
		return *d.Budget.RunTokens
	}
	return 0
}

// SettledTimeRows is TimeRows with each run's rows settled through
// Store.Measure at that run's cache-read weight. unsettled counts rows whose
// tokens are not final yet.
func (s *Store) SettledTimeRows(selector string) ([]TimeRow, int, error) {
	rows, err := s.TimeRows(selector)
	if err != nil {
		return nil, 0, err
	}
	byRun := map[string][]int{}
	var order []string
	for index, row := range rows {
		if _, ok := byRun[row.Run]; !ok {
			order = append(order, row.Run)
		}
		byRun[row.Run] = append(byRun[row.Run], index)
	}
	result := make([]TimeRow, len(rows))
	copy(result, rows)
	unsettled := 0
	for _, runID := range order {
		percent := DefaultCacheReadPercent
		if validRunID(runID) {
			if d, err := ReadDefinition(s.SnapshotDir(runID)); err == nil {
				percent = d.CacheReadWeight()
			}
		}
		part := make([]TimeRow, 0, len(byRun[runID]))
		for _, index := range byRun[runID] {
			part = append(part, rows[index])
		}
		settled, count := SettleTokens(part, percent, s.Measure)
		unsettled += count
		for position, index := range byRun[runID] {
			result[index] = settled[position]
		}
	}
	return result, unsettled, nil
}

// RunBudgetReason is the pause reason for an exhausted run budget.
func RunBudgetReason(spent, ceiling int64) string {
	return fmt.Sprintf("run token budget exhausted (spent %d of %d)", spent, ceiling)
}

// UnitBudgetReason is the unit reason for an exhausted unit budget.
func UnitBudgetReason(spent, ceiling int64) string {
	return fmt.Sprintf("unit token budget exhausted (spent %d of %d)", spent, ceiling)
}
