package workflow

import (
	"fmt"
	"math"
	"sort"
	"time"
)

type Counts struct {
	Done    int `json:"done"`
	Weak    int `json:"weak"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
	// AwaitingApproval counts parked units waiting for a human decision.
	// A unit with a pending revision counts as pending.
	AwaitingApproval int `json:"awaiting_approval,omitempty"`
	// Ready counts, per next step, the units between steps (workflows with
	// steps only). They are included in Pending.
	Ready map[string]int `json:"ready,omitempty"`
}

type AgentStatus struct {
	Agent       string `json:"agent"`
	CurrentUnit string `json:"current_unit,omitempty"`
	// Role is the agent's role (workflows with steps).
	Role string `json:"role,omitempty"`
	// CurrentStep is the step of the current unit (workflows with steps).
	CurrentStep string        `json:"current_step,omitempty"`
	Elapsed     time.Duration `json:"elapsed,omitempty"`
	// Guardrails lists the warn and halt levels announced for the current
	// unit, most severe first.
	Guardrails []GuardrailMark `json:"guardrails,omitempty"`
}

type RunStatus struct {
	ID        string        `json:"id"`
	Workflow  string        `json:"workflow"`
	Owner     string        `json:"owner"`
	Status    string        `json:"status"`
	Counts    Counts        `json:"counts"`
	Agents    []AgentStatus `json:"agents"`
	LastError string        `json:"last_error,omitempty"`
	TornLog   bool          `json:"torn_log,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`
	// PauseReason explains a "pausing" or "paused" run.
	PauseReason string `json:"pause_reason,omitempty"`
	// Breakers lists agents whose circuit breaker is open or half-open.
	Breakers []BreakerView `json:"breakers,omitempty"`
	// RunTokens and RunTokensSpent are set when a run token ceiling applies.
	// Spend includes parked units.
	RunTokens      int64 `json:"run_tokens,omitempty"`
	RunTokensSpent int64 `json:"run_tokens_spent,omitempty"`
}

func (s *Store) Status(id string) (RunStatus, error) {
	run, err := s.LoadRun(id)
	if err != nil {
		return RunStatus{}, err
	}
	events, unitsTorn, err := s.Events(id)
	if err != nil {
		return RunStatus{}, err
	}
	times, timesTorn, err := s.Times(id)
	if err != nil {
		return RunStatus{}, err
	}
	latest := latestEvents(events)
	view := RunStatus{ID: run.ID, Workflow: run.Workflow, Owner: run.Owner, Status: run.Status, LastError: run.LastError, UpdatedAt: run.UpdatedAt, TornLog: unitsTorn || timesTorn, PauseReason: run.PauseReason}
	// A snapshot that no longer parses only hides the supervision view.
	var definition *Definition
	if d, err := ReadDefinition(s.SnapshotDir(id)); err == nil {
		definition = &d
		view.Breakers = OpenBreakers(d, run, events, s.now())
		if ceiling := runCeiling(d, run); ceiling > 0 {
			settled, _ := SettleTokens(spendRowsFrom(times, events), d.CacheReadWeight(), s.Measure)
			view.RunTokens, view.RunTokensSpent = ceiling, weightedSpend(settled)
		}
	}
	active := map[string]Event{}
	for _, event := range events {
		if event.Key == "" || event.Decision != nil {
			// Decisions (and revision requests) are not agent activity.
			continue
		}
		switch {
		case parkedState(event.State) || event.State == StateStepDone:
			if current, ok := active[event.Agent]; ok && current.Key == event.Key {
				delete(active, event.Agent)
			}
		case !terminalState(event.State):
			active[event.Agent] = event
		default:
			delete(active, event.Agent)
		}
	}
	for _, agent := range run.Agents {
		item := AgentStatus{Agent: agent, CurrentUnit: run.Current[agent]}
		if run.Roles != nil {
			item.Role = run.RoleOf(agent)
		}
		if event, ok := active[agent]; ok && !event.At.IsZero() {
			item.Elapsed = s.now().Sub(event.At)
			if item.Elapsed < 0 {
				item.Elapsed = 0
			}
		}
		if event, ok := active[agent]; ok {
			item.Guardrails = guardrailMarks(events, event.Key)
			if event.Key == item.CurrentUnit {
				item.CurrentStep = event.Step
			}
		}
		view.Agents = append(view.Agents, item)
	}
	for _, key := range run.UnitKeys {
		switch latest[key].State {
		case "done":
			view.Counts.Done++
		case "weak":
			view.Counts.Weak++
		case "failed":
			view.Counts.Failed++
		case StateAwaitingApproval:
			view.Counts.AwaitingApproval++
		case StateStepDone:
			view.Counts.Pending++
			if definition != nil {
				if next := definition.StepIndex(latest[key].Step) + 1; next > 0 && next < len(definition.Steps) {
					if view.Counts.Ready == nil {
						view.Counts.Ready = map[string]int{}
					}
					view.Counts.Ready[definition.Steps[next].Name]++
				}
			}
		default:
			view.Counts.Pending++
		}
	}
	return view, nil
}

type GroupStats struct {
	Count      int     `json:"count"`
	Done       int     `json:"done"`
	Weak       int     `json:"weak"`
	Failed     int     `json:"failed"`
	MedianWork float64 `json:"median_work_s,omitempty"`
	P90Work    float64 `json:"p90_work_s,omitempty"`
}

type SlowUnit struct {
	Unit    string  `json:"unit"`
	Agent   string  `json:"agent"`
	Model   string  `json:"model,omitempty"`
	Result  string  `json:"result"`
	WorkSec float64 `json:"work_s"`
}

type TimeStats struct {
	Count        int                   `json:"count"`
	Done         int                   `json:"done"`
	Weak         int                   `json:"weak"`
	Failed       int                   `json:"failed"`
	MedianWork   float64               `json:"median_work_s,omitempty"`
	P90Work      float64               `json:"p90_work_s,omitempty"`
	ByAgent      map[string]GroupStats `json:"by_agent,omitempty"`
	ByModel      map[string]GroupStats `json:"by_model,omitempty"`
	Rounds       map[int]int           `json:"rounds,omitempty"`
	CompactTotal float64               `json:"compact_total_s,omitempty"`
	Slowest      []SlowUnit            `json:"slowest,omitempty"`
	// Tokens is present only when at least one row has token data.
	Tokens *TokenStats `json:"tokens,omitempty"`
	// Approvals is present when units waited for a human decision.
	Approvals *ApprovalStats `json:"approvals,omitempty"`
	// Activity is present when at least one row has tool activity.
	Activity *ActivityStats `json:"activity,omitempty"`
	// Steps is present when rows carry steps, one entry per step in order
	// of first appearance.
	Steps []StepStats `json:"steps,omitempty"`
}

// StepStats summarizes one step over the rows that carry steps. Units counts
// the units that went through the step (each step attempt once); Failed
// counts the attempts that failed there; MedianQueue is the median time a
// unit waited, ready, for an agent of the step's role.
type StepStats struct {
	Name        string  `json:"name"`
	Units       int     `json:"units"`
	Failed      int     `json:"failed"`
	MedianWork  float64 `json:"median_work_s"`
	P90Work     float64 `json:"p90_work_s"`
	MedianQueue float64 `json:"median_queue_s"`
}

func summarizeSteps(rows []TimeRow) []StepStats {
	var order []string
	work, queue := map[string][]float64{}, map[string][]float64{}
	failed := map[string]int{}
	seen := map[string]bool{}
	for _, row := range rows {
		for _, step := range row.Steps {
			// Each step attempt is in one row; count it once even if a
			// hand-edited log repeats it.
			id := row.Run + "\x00" + stepSpendID(row.Unit, step.Name, step.Attempt)
			if seen[id] {
				continue
			}
			seen[id] = true
			if _, ok := work[step.Name]; !ok {
				order = append(order, step.Name)
			}
			work[step.Name] = append(work[step.Name], step.WorkSeconds)
			queue[step.Name] = append(queue[step.Name], step.QueuedSeconds)
			if step.Result == "failed" {
				failed[step.Name]++
			}
		}
	}
	var stats []StepStats
	for _, name := range order {
		stats = append(stats, StepStats{Name: name, Units: len(work[name]), Failed: failed[name], MedianWork: median(work[name]), P90Work: percentile(work[name], .90), MedianQueue: median(queue[name])})
	}
	return stats
}

// ApprovalStats counts decided units in times.jsonl. A revised unit counts
// as revised and, if it was decided afterwards, also as approved or rejected.
// MedianWait is over every unit that waited for a decision. Waiting (parked,
// undecided units) is set by readers of units.jsonl; SummarizeTimes leaves it
// zero.
type ApprovalStats struct {
	Approved   int     `json:"approved"`
	Rejected   int     `json:"rejected"`
	Revised    int     `json:"revised"`
	MedianWait float64 `json:"median_wait_s"`
	Waiting    int     `json:"waiting,omitempty"`
}

func summarizeApprovals(rows []TimeRow) *ApprovalStats {
	stats := &ApprovalStats{}
	var waits []float64
	for _, row := range rows {
		if row.Approval == nil {
			continue
		}
		switch row.Approval.Decision {
		case "approved":
			stats.Approved++
		case "rejected":
			stats.Rejected++
		}
		if row.Approval.Revisions > 0 {
			stats.Revised++
		}
		waits = append(waits, row.Approval.WaitSeconds)
	}
	if len(waits) == 0 {
		return nil
	}
	stats.MedianWait = median(waits)
	return stats
}

// TokenGroup sums token usage for one agent or model.
type TokenGroup struct {
	Units    int   `json:"units"`
	Total    int64 `json:"total"`
	Weighted int64 `json:"weighted"`
}

// CostlyUnit is one of the most expensive units by the budget measure.
type CostlyUnit struct {
	Unit     string `json:"unit"`
	Agent    string `json:"agent"`
	Model    string `json:"model,omitempty"`
	Result   string `json:"result"`
	Total    int64  `json:"total"`
	Weighted int64  `json:"weighted"`
}

// TokenStats summarizes the rows that carry token data. Total is every token
// counted; Weighted is the budget measure recorded with each row.
type TokenStats struct {
	Units          int                   `json:"units"`
	Total          int64                 `json:"total"`
	Weighted       int64                 `json:"weighted"`
	Input          int64                 `json:"in"`
	Output         int64                 `json:"out"`
	CacheWrite     int64                 `json:"cache_write"`
	CacheRead      int64                 `json:"cache_read"`
	MedianTotal    float64               `json:"median_total"`
	P90Total       float64               `json:"p90_total"`
	MedianWeighted float64               `json:"median_weighted"`
	P90Weighted    float64               `json:"p90_weighted"`
	ByAgent        map[string]TokenGroup `json:"by_agent,omitempty"`
	ByModel        map[string]TokenGroup `json:"by_model,omitempty"`
	Costliest      []CostlyUnit          `json:"costliest,omitempty"`
	// Unsettled counts units whose tokens are not final yet (set by readers
	// that settle rows; SummarizeTimes leaves it zero).
	Unsettled int `json:"unsettled,omitempty"`
}

func summarizeTokens(rows []TimeRow) *TokenStats {
	stats := &TokenStats{ByAgent: map[string]TokenGroup{}, ByModel: map[string]TokenGroup{}}
	var totals, weighted []float64
	for _, row := range rows {
		if row.Tokens == nil {
			continue
		}
		t := *row.Tokens
		stats.Units++
		stats.Total += t.Total()
		stats.Weighted += t.Weighted
		stats.Input += t.Input
		stats.Output += t.Output
		stats.CacheWrite += t.CacheWrite
		stats.CacheRead += t.CacheRead
		totals = append(totals, float64(t.Total()))
		weighted = append(weighted, float64(t.Weighted))
		group := stats.ByAgent[row.Agent]
		group.Units, group.Total, group.Weighted = group.Units+1, group.Total+t.Total(), group.Weighted+t.Weighted
		stats.ByAgent[row.Agent] = group
		if row.Model != "" {
			group := stats.ByModel[row.Model]
			group.Units, group.Total, group.Weighted = group.Units+1, group.Total+t.Total(), group.Weighted+t.Weighted
			stats.ByModel[row.Model] = group
		}
		stats.Costliest = append(stats.Costliest, CostlyUnit{Unit: row.Unit, Agent: row.Agent, Model: row.Model, Result: row.Result, Total: t.Total(), Weighted: t.Weighted})
	}
	if stats.Units == 0 {
		return nil
	}
	stats.MedianTotal, stats.P90Total = median(totals), percentile(totals, .90)
	stats.MedianWeighted, stats.P90Weighted = median(weighted), percentile(weighted, .90)
	sort.SliceStable(stats.Costliest, func(i, j int) bool { return stats.Costliest[i].Weighted > stats.Costliest[j].Weighted })
	if len(stats.Costliest) > 5 {
		stats.Costliest = stats.Costliest[:5]
	}
	return stats
}

func SummarizeTimes(rows []TimeRow) TimeStats {
	stats := TimeStats{Count: len(rows), ByAgent: map[string]GroupStats{}, ByModel: map[string]GroupStats{}, Rounds: map[int]int{}}
	allWork := make([]float64, 0, len(rows))
	agentWork, modelWork := map[string][]float64{}, map[string][]float64{}
	for _, row := range rows {
		switch row.Result {
		case "done":
			stats.Done++
		case "weak":
			stats.Weak++
		case "failed":
			stats.Failed++
		}
		stats.Rounds[row.Rounds]++
		stats.CompactTotal += row.CompactSeconds
		allWork = append(allWork, row.WorkSeconds)
		agentWork[row.Agent] = append(agentWork[row.Agent], row.WorkSeconds)
		if row.Model != "" {
			modelWork[row.Model] = append(modelWork[row.Model], row.WorkSeconds)
		}
		stats.Slowest = append(stats.Slowest, SlowUnit{Unit: row.Unit, Agent: row.Agent, Model: row.Model, Result: row.Result, WorkSec: row.WorkSeconds})
	}
	stats.MedianWork, stats.P90Work = median(allWork), percentile(allWork, .90)
	for name, values := range agentWork {
		group := GroupStats{Count: len(values), MedianWork: median(values), P90Work: percentile(values, .90)}
		for _, row := range rows {
			if row.Agent == name {
				switch row.Result {
				case "done":
					group.Done++
				case "weak":
					group.Weak++
				case "failed":
					group.Failed++
				}
			}
		}
		stats.ByAgent[name] = group
	}
	for model, values := range modelWork {
		group := GroupStats{Count: len(values), MedianWork: median(values), P90Work: percentile(values, .90)}
		for _, row := range rows {
			if row.Model == model {
				switch row.Result {
				case "done":
					group.Done++
				case "weak":
					group.Weak++
				case "failed":
					group.Failed++
				}
			}
		}
		stats.ByModel[model] = group
	}
	sort.Slice(stats.Slowest, func(i, j int) bool { return stats.Slowest[i].WorkSec > stats.Slowest[j].WorkSec })
	if len(stats.Slowest) > 5 {
		stats.Slowest = stats.Slowest[:5]
	}
	stats.Tokens = summarizeTokens(rows)
	stats.Approvals = summarizeApprovals(rows)
	stats.Activity = summarizeActivity(rows)
	stats.Steps = summarizeSteps(rows)
	return stats
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyOf := append([]float64(nil), values...)
	sort.Float64s(copyOf)
	middle := len(copyOf) / 2
	if len(copyOf)%2 == 1 {
		return copyOf[middle]
	}
	return (copyOf[middle-1] + copyOf[middle]) / 2
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyOf := append([]float64(nil), values...)
	sort.Float64s(copyOf)
	index := int(math.Ceil(p*float64(len(copyOf)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(copyOf) {
		index = len(copyOf) - 1
	}
	return copyOf[index]
}

func (s TimeStats) String() string {
	return fmt.Sprintf("count=%d done=%d weak=%d failed=%d median_work=%.1fs p90_work=%.1fs compact=%.1fs", s.Count, s.Done, s.Weak, s.Failed, s.MedianWork, s.P90Work, s.CompactTotal)
}

func (s *Store) TimeRows(selector string) ([]TimeRow, error) {
	if run, err := s.LoadRun(selector); err == nil {
		rows, _, err := s.Times(run.ID)
		return rows, err
	}
	runs, err := s.Runs()
	if err != nil {
		return nil, err
	}
	var rows []TimeRow
	found := false
	for _, run := range runs {
		if run.Workflow != selector {
			continue
		}
		found = true
		part, _, err := s.Times(run.ID)
		if err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	if !found {
		return nil, fmt.Errorf("no workflow run or workflow named %q", selector)
	}
	return rows, nil
}
