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
}

type AgentStatus struct {
	Agent       string        `json:"agent"`
	CurrentUnit string        `json:"current_unit,omitempty"`
	Elapsed     time.Duration `json:"elapsed,omitempty"`
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
	_, timesTorn, err := s.Times(id)
	if err != nil {
		return RunStatus{}, err
	}
	latest := latestEvents(events)
	view := RunStatus{ID: run.ID, Workflow: run.Workflow, Owner: run.Owner, Status: run.Status, LastError: run.LastError, UpdatedAt: run.UpdatedAt, TornLog: unitsTorn || timesTorn}
	active := map[string]Event{}
	for _, event := range events {
		if event.Key == "" {
			continue
		}
		if !terminalState(event.State) {
			active[event.Agent] = event
		} else {
			delete(active, event.Agent)
		}
	}
	for _, agent := range run.Agents {
		item := AgentStatus{Agent: agent, CurrentUnit: run.Current[agent]}
		if event, ok := active[agent]; ok && !event.At.IsZero() {
			item.Elapsed = s.now().Sub(event.At)
			if item.Elapsed < 0 {
				item.Elapsed = 0
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
