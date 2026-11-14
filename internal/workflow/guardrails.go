package workflow

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// GuardrailSpec holds behavioral rules evaluated over the tool calls an agent
// makes while it works on a unit. Every rule is optional; at least one must be
// set. Rules never interrupt an agent: they warn while it works and stop
// further rounds at an idle boundary.
type GuardrailSpec struct {
	// ToolCalls limits the tool calls made in one unit, over all its rounds.
	ToolCalls *CountRule `yaml:"tool_calls,omitempty" json:"tool_calls,omitempty"`
	// RepeatedToolCall limits consecutive identical calls (same tool, same
	// input).
	RepeatedToolCall *StreakRule `yaml:"repeated_tool_call,omitempty" json:"repeated_tool_call,omitempty"`
	// ToolFailures limits consecutive failed tool results.
	ToolFailures *StreakRule `yaml:"tool_failures,omitempty" json:"tool_failures,omitempty"`
}

// CountRule warns and halts at a count. Pointers keep an explicit zero
// distinguishable from an absent key so that zero can be rejected.
type CountRule struct {
	Warn *int `yaml:"warn,omitempty" json:"warn,omitempty"`
	Halt *int `yaml:"halt,omitempty" json:"halt,omitempty"`
}

// StreakRule warns and halts at a streak length. Tool, when set, limits the
// rule to that tool's calls.
type StreakRule struct {
	Warn *int   `yaml:"warn,omitempty" json:"warn,omitempty"`
	Halt *int   `yaml:"halt,omitempty" json:"halt,omitempty"`
	Tool string `yaml:"tool,omitempty" json:"tool,omitempty"`
}

// Guardrail rule names, as written in workflow.yaml and in events.
const (
	RuleToolCalls        = "tool_calls"
	RuleRepeatedToolCall = "repeated_tool_call"
	RuleToolFailures     = "tool_failures"
)

func validateGuardrails(spec *GuardrailSpec) error {
	if spec == nil {
		return nil
	}
	if spec.ToolCalls == nil && spec.RepeatedToolCall == nil && spec.ToolFailures == nil {
		return errors.New("guardrails needs at least one of tool_calls, repeated_tool_call, tool_failures")
	}
	if rule := spec.ToolCalls; rule != nil {
		if err := validateLevels("guardrails."+RuleToolCalls, rule.Warn, rule.Halt); err != nil {
			return err
		}
	}
	for name, rule := range map[string]*StreakRule{RuleRepeatedToolCall: spec.RepeatedToolCall, RuleToolFailures: spec.ToolFailures} {
		if rule == nil {
			continue
		}
		if err := validateLevels("guardrails."+name, rule.Warn, rule.Halt); err != nil {
			return err
		}
		if rule.Tool != "" && (strings.TrimSpace(rule.Tool) != rule.Tool || strings.IndexFunc(rule.Tool, unicode.IsControl) >= 0) {
			return fmt.Errorf("guardrails.%s.tool must be a tool name without surrounding spaces or control characters, got %q", name, rule.Tool)
		}
	}
	return nil
}

func validateLevels(field string, warn, halt *int) error {
	if warn == nil && halt == nil {
		return fmt.Errorf("%s needs warn or halt", field)
	}
	if warn != nil && *warn <= 0 {
		return fmt.Errorf("%s.warn must be a positive integer, got %d", field, *warn)
	}
	if halt != nil && *halt <= 0 {
		return fmt.Errorf("%s.halt must be a positive integer, got %d", field, *halt)
	}
	if warn != nil && halt != nil && *warn >= *halt {
		return fmt.Errorf("%s.warn (%d) must be below halt (%d)", field, *warn, *halt)
	}
	return nil
}

// ToolOutcome is what a tool call's result said. A call whose result has not
// arrived (yet) is pending.
type ToolOutcome int8

const (
	ToolPending ToolOutcome = iota
	ToolSucceeded
	ToolFailed
)

// ToolEvent is one tool call in an agent's main conversation.
type ToolEvent struct {
	At time.Time
	// ID is the harness's tool call id; readers use it to pair results with
	// calls and to drop duplicates.
	ID   string
	Tool string
	// Input is a stable hash of the canonical tool input. Two calls are
	// identical when Tool and Input are equal.
	Input   string
	Outcome ToolOutcome
}

// Activity is an agent's tool activity since a point in time, in call order.
// A reader that bounds its memory folds the oldest calls into Earlier and
// keeps the rest in Events; Tally over both is exact either way.
type Activity struct {
	Earlier ToolTally
	Events  []ToolEvent
}

// Tally folds Earlier and Events into one summary.
func (a Activity) Tally() ToolTally {
	tally := a.Earlier.Clone()
	for _, event := range a.Events {
		tally.Add(event)
	}
	return tally
}

// ActivityMeter is an optional Driver capability. Activity reports the tool
// calls the agent made in its main conversation (its own subagents excluded)
// since a time. known is false when the harness is not supported or its
// transcript cannot be found; the engine then never guesses and does not
// enforce guardrails for that agent.
type ActivityMeter interface {
	Activity(ctx context.Context, agent string, since time.Time) (Activity, bool, error)
}

// ToolTally is a rule-independent summary of tool calls in call order: total
// calls, identical-call streaks and failure streaks, overall and per tool.
// The zero value is empty and ready to use.
type ToolTally struct {
	calls int
	// Identical-call streak: the current run of equal calls and the longest
	// runs, overall and per tool.
	lastTool, lastInput string
	repeat              int
	longestRepeat       int
	longestRepeatTool   string
	repeatByTool        map[string]int
	// Failure streaks in call order. A pending call neither extends nor
	// resets a streak; a success resets it. Per tool, only that tool's
	// results count.
	failRun           int
	longestFail       int
	failRunByTool     map[string]int
	longestFailByTool map[string]int
}

// Add folds one call into the tally.
func (t *ToolTally) Add(event ToolEvent) {
	if t.repeatByTool == nil {
		t.repeatByTool, t.failRunByTool, t.longestFailByTool = map[string]int{}, map[string]int{}, map[string]int{}
	}
	t.calls++
	if t.repeat > 0 && event.Tool == t.lastTool && event.Input == t.lastInput {
		t.repeat++
	} else {
		t.lastTool, t.lastInput, t.repeat = event.Tool, event.Input, 1
	}
	if t.repeat > t.longestRepeat {
		t.longestRepeat, t.longestRepeatTool = t.repeat, event.Tool
	}
	if t.repeat > t.repeatByTool[event.Tool] {
		t.repeatByTool[event.Tool] = t.repeat
	}
	switch event.Outcome {
	case ToolFailed:
		t.failRun++
		t.failRunByTool[event.Tool]++
		t.longestFail = max(t.longestFail, t.failRun)
		t.longestFailByTool[event.Tool] = max(t.longestFailByTool[event.Tool], t.failRunByTool[event.Tool])
	case ToolSucceeded:
		t.failRun = 0
		t.failRunByTool[event.Tool] = 0
	}
}

// Clone returns an independent copy.
func (t ToolTally) Clone() ToolTally {
	clone := t
	clone.repeatByTool, clone.failRunByTool, clone.longestFailByTool = copyCounts(t.repeatByTool), copyCounts(t.failRunByTool), copyCounts(t.longestFailByTool)
	return clone
}

func copyCounts(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	result := make(map[string]int, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

// Calls is the number of tool calls.
func (t ToolTally) Calls() int { return t.calls }

// LongestRepeat is the longest run of identical calls, of tool when it is not
// empty, with the tool that made it.
func (t ToolTally) LongestRepeat(tool string) (int, string) {
	if tool != "" {
		return t.repeatByTool[tool], tool
	}
	return t.longestRepeat, t.longestRepeatTool
}

// LongestFailures is the longest run of failed results, counting only tool's
// results when tool is not empty.
func (t ToolTally) LongestFailures(tool string) int {
	if tool != "" {
		return t.longestFailByTool[tool]
	}
	return t.longestFail
}

// GuardLevel is a rule's severity: ok < warn < halt.
type GuardLevel int

const (
	GuardOK GuardLevel = iota
	GuardWarn
	GuardHalt
)

func (l GuardLevel) String() string {
	switch l {
	case GuardWarn:
		return "warn"
	case GuardHalt:
		return "halt"
	default:
		return "ok"
	}
}

func parseGuardLevel(value string) GuardLevel {
	switch value {
	case "warn":
		return GuardWarn
	case "halt":
		return GuardHalt
	default:
		return GuardOK
	}
}

// RuleVerdict is one rule's verdict. Value is the measured count or streak;
// Limit is the threshold of the level reached (0 for ok).
type RuleVerdict struct {
	Rule        string
	Level       GuardLevel
	Value       int
	Limit       int
	Explanation string
}

// evaluateGuardrails judges a tally against every configured rule, in the
// order tool_failures, repeated_tool_call, tool_calls. priorCalls adds calls
// made before the tally's window (the rounds before a revision) to the
// tool_calls rule; the streak rules see only the window.
func evaluateGuardrails(spec *GuardrailSpec, tally ToolTally, priorCalls int) []RuleVerdict {
	if spec == nil {
		return nil
	}
	var verdicts []RuleVerdict
	if rule := spec.ToolFailures; rule != nil {
		value := tally.LongestFailures(rule.Tool)
		verdict := judge(RuleToolFailures, value, rule.Warn, rule.Halt)
		if rule.Tool != "" {
			verdict.Explanation = fmt.Sprintf("tool %s failed %d times in a row", rule.Tool, value)
		} else {
			verdict.Explanation = fmt.Sprintf("%d tool calls failed in a row", value)
		}
		verdicts = append(verdicts, verdict)
	}
	if rule := spec.RepeatedToolCall; rule != nil {
		value, tool := tally.LongestRepeat(rule.Tool)
		verdict := judge(RuleRepeatedToolCall, value, rule.Warn, rule.Halt)
		if tool == "" {
			tool = "tool"
		}
		verdict.Explanation = fmt.Sprintf("the same %s call was made %d times in a row", tool, value)
		verdicts = append(verdicts, verdict)
	}
	if rule := spec.ToolCalls; rule != nil {
		value := tally.Calls() + priorCalls
		verdict := judge(RuleToolCalls, value, rule.Warn, rule.Halt)
		verdict.Explanation = fmt.Sprintf("the unit made %d tool calls", value)
		verdicts = append(verdicts, verdict)
	}
	return verdicts
}

func judge(rule string, value int, warn, halt *int) RuleVerdict {
	verdict := RuleVerdict{Rule: rule, Value: value}
	switch {
	case halt != nil && value >= *halt:
		verdict.Level, verdict.Limit = GuardHalt, *halt
	case warn != nil && value >= *warn:
		verdict.Level, verdict.Limit = GuardWarn, *warn
	}
	return verdict
}

// worstVerdict merges verdicts by severity; ties keep the earlier rule.
func worstVerdict(verdicts []RuleVerdict) RuleVerdict {
	worst := RuleVerdict{Level: GuardOK}
	for _, verdict := range verdicts {
		if verdict.Level > worst.Level {
			worst = verdict
		}
	}
	return worst
}

// GuardrailReason is the end reason of a unit stopped by a guardrail.
func GuardrailReason(explanation string) string { return "guardrail: " + explanation }

// RowActivity is a unit's tool activity in times.jsonl, present only when
// guardrails are configured and the activity was known at finish.
type RowActivity struct {
	ToolCalls int `json:"tool_calls"`
	// LongestRepeat is the longest run of identical calls (any tool).
	LongestRepeat int `json:"longest_repeat"`
	// LongestFailures is the longest run of failed tool results (any tool).
	LongestFailures int `json:"longest_failures"`
	// Verdict (warn or halt) and Rule are the unit's most severe guardrail
	// verdict at finish; both are omitted when every rule was ok.
	Verdict string `json:"verdict,omitempty"`
	Rule    string `json:"rule,omitempty"`
}

// GuardrailRecord is carried by guardrail_warn and guardrail_halt events. The
// unit is named here, not in the event's key, so unit bookkeeping ignores the
// event.
type GuardrailRecord struct {
	Unit  string `json:"unit"`
	Rule  string `json:"rule"`
	Level string `json:"level"`
	Value int    `json:"value"`
	Limit int    `json:"limit"`
}

// Event states written by guardrails. Like the supervision events they carry
// no unit key.
const (
	eventGuardrailWarn     = "guardrail_warn"
	eventGuardrailHalt     = "guardrail_halt"
	eventGuardrailsUnknown = "guardrails_unknown"
)

// streakRule reports the rules whose counters restart with a revision round.
func streakRule(rule string) bool { return rule == RuleRepeatedToolCall || rule == RuleToolFailures }

// announcedGuardrails returns the highest level already announced per rule
// for the current attempt of unit: announcements before the unit's latest
// terminal event belong to an earlier attempt (--retry-failed), and a claimed
// revision restarts the streak rules, whose counters restart with it.
func announcedGuardrails(events []Event, unit string) map[string]GuardLevel {
	announced := map[string]GuardLevel{}
	for _, event := range events {
		switch {
		case event.Key == unit && terminalState(event.State):
			announced = map[string]GuardLevel{}
		case event.Key == unit && event.State == StateRevising:
			for rule := range announced {
				if streakRule(rule) {
					delete(announced, rule)
				}
			}
		case event.Guardrail != nil && event.Guardrail.Unit == unit:
			if level := parseGuardLevel(event.Guardrail.Level); level > announced[event.Guardrail.Rule] {
				announced[event.Guardrail.Rule] = level
			}
		}
	}
	return announced
}

// GuardrailMark is an announced guardrail level of an in-flight unit, as bp
// workflow status shows it.
type GuardrailMark struct {
	Rule  string `json:"rule"`
	Level string `json:"level"`
}

// guardrailMarks lists the warn and halt levels announced for unit's current
// attempt, most severe first.
func guardrailMarks(events []Event, unit string) []GuardrailMark {
	announced := announcedGuardrails(events, unit)
	rules := make([]string, 0, len(announced))
	for rule, level := range announced {
		if level > GuardOK {
			rules = append(rules, rule)
		}
	}
	sort.Slice(rules, func(i, j int) bool {
		if announced[rules[i]] != announced[rules[j]] {
			return announced[rules[i]] > announced[rules[j]]
		}
		return rules[i] < rules[j]
	})
	marks := make([]GuardrailMark, 0, len(rules))
	for _, rule := range rules {
		marks = append(marks, GuardrailMark{Rule: rule, Level: announced[rule].String()})
	}
	return marks
}

// ActivityStats summarizes the rows that carry tool activity. Warned and
// Halted count units by their final verdict; HaltsByRule breaks the halts
// down by rule.
type ActivityStats struct {
	Units       int            `json:"units"`
	MedianCalls float64        `json:"median_tool_calls"`
	P90Calls    float64        `json:"p90_tool_calls"`
	Warned      int            `json:"warned"`
	Halted      int            `json:"halted"`
	HaltsByRule map[string]int `json:"halts_by_rule,omitempty"`
}

func summarizeActivity(rows []TimeRow) *ActivityStats {
	stats := &ActivityStats{HaltsByRule: map[string]int{}}
	var calls []float64
	for _, row := range rows {
		if row.Activity == nil {
			continue
		}
		stats.Units++
		calls = append(calls, float64(row.Activity.ToolCalls))
		switch row.Activity.Verdict {
		case "warn":
			stats.Warned++
		case "halt":
			stats.Halted++
			stats.HaltsByRule[row.Activity.Rule]++
		}
	}
	if stats.Units == 0 {
		return nil
	}
	stats.MedianCalls, stats.P90Calls = median(calls), percentile(calls, .90)
	return stats
}
