package workflow

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"text/template/parse"
	"time"

	"go.yaml.in/yaml/v3"
)

// DefaultRole is the role of a step without an agent key and of every agent
// given to bp workflow start as a plain name.
const DefaultRole = "default"

// MaxSteps bounds the length of a pipeline.
const MaxSteps = 16

// StateStepDone is the unit event a multi-step run writes when one step
// finished done or weak and the unit moves on to its next step. It is not
// terminal: the unit is between steps.
const StateStepDone = "step_done"

// StateSentBack is the unit event a multi-step run writes when a step's
// validator exited 4 and the step has send_back: the attempt ended and the
// unit goes back to the step send_back.to names. It is not terminal.
const StateSentBack = "sent_back"

// StateStepFailed is the unit event a multi-step run writes when a step
// attempt failed and a fallback role of the step is left to try: the step
// starts again from scratch on an agent of that role. It is not terminal.
const StateStepFailed = "step_failed"

// Attempt reasons: why a step attempt was dispatched (StepRecord and
// StepRow attempt_reason, and every event of the attempt).
const (
	// AttemptFirst is the step's first attempt for the unit.
	AttemptFirst = "first"
	// AttemptRerun is a later step running again in order after an earlier
	// step was redone (after a send-back).
	AttemptRerun = "rerun"
	// AttemptSendBack is the redo of the step a later step sent the unit
	// back to.
	AttemptSendBack = "send_back"
	// AttemptFallback is the step started from scratch on an agent of a
	// fallback role after an attempt failed.
	AttemptFallback = "fallback"
	// AttemptRetry is a --retry-failed restart of the step the unit failed
	// at.
	AttemptRetry = "retry"
)

// betweenSteps reports whether a unit event leaves the unit between two step
// attempts: not terminal, nothing in flight, waiting for its next work item.
func betweenSteps(state string) bool {
	return state == StateStepDone || state == StateSentBack || state == StateStepFailed
}

var stepNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ValidRole reports whether name may be a step name or a role: lowercase
// letters, digits, '-' and '_', starting with a letter or digit.
func ValidRole(name string) bool { return stepNamePattern.MatchString(name) }

// StepSpec is one step of a pipeline: a prompt sent to an agent of Agent's
// role, with its own output and validator. The fields mean exactly what the
// top-level keys of a definition without steps mean.
type StepSpec struct {
	Name     string       `yaml:"name" json:"name"`
	Agent    string       `yaml:"agent,omitempty" json:"agent,omitempty"`
	Prompt   PromptSpec   `yaml:"prompt" json:"prompt"`
	Output   OutputSpec   `yaml:"output,omitempty" json:"output,omitempty"`
	Validate ValidateSpec `yaml:"validate,omitempty" json:"validate,omitempty"`
	// SendBack lets the step's validator send the unit back to an earlier
	// step by exiting 4.
	SendBack *SendBackSpec `yaml:"send_back,omitempty" json:"send_back,omitempty"`
	// Fallback lists, in order, the roles that retry the step from scratch
	// when an attempt fails.
	Fallback []string `yaml:"fallback,omitempty" json:"fallback,omitempty"`
}

// SendBackSpec is a step's send_back key: the earlier step a validator exit
// 4 sends the unit back to, and how often the step may do so per unit
// attempt (default 1).
type SendBackSpec struct {
	To  string `yaml:"to" json:"to"`
	Max *int   `yaml:"max,omitempty" json:"max,omitempty"`
}

// Limit is the number of send-backs the step may issue per unit attempt.
func (s SendBackSpec) Limit() int {
	if s.Max == nil {
		return 1
	}
	return *s.Max
}

// Role returns the step's role, DefaultRole when the step names none.
func (s StepSpec) Role() string {
	if s.Agent == "" {
		return DefaultRole
	}
	return s.Agent
}

// StepResult is what a template sees of a step the unit already finished:
// .Steps.<name>.Output and friends.
type StepResult struct {
	Output string
	Result string
	Agent  string
	Rounds int
	Reason string
}

// StepAttempt is what a template sees as .Previous: the previous attempt of
// the current step for this unit (zero for a first attempt and in a
// workflow without steps). Result is done, weak, failed or sent_back.
type StepAttempt struct {
	Agent  string
	Result string
	Reason string
	Output string
}

// MultiStep reports whether the definition has a steps list.
func (d Definition) MultiStep() bool { return len(d.Steps) > 0 }

// Pipeline returns the steps a unit goes through. A definition without
// steps has one unnamed step of the default role built from its top-level
// prompt, output and validate keys.
func (d Definition) Pipeline() []StepSpec {
	if len(d.Steps) > 0 {
		return d.Steps
	}
	return []StepSpec{{Prompt: d.Prompt, Output: d.Output, Validate: d.Validate}}
}

// StepIndex returns the position of the named step, or -1. The unnamed step
// of a definition without steps is at 0.
func (d Definition) StepIndex(name string) int {
	if len(d.Steps) == 0 {
		if name == "" {
			return 0
		}
		return -1
	}
	for index, step := range d.Steps {
		if step.Name == name {
			return index
		}
	}
	return -1
}

// forStep returns the definition as the engine sees it while it runs step
// index: the step's prompt, output and validate replace the top-level ones.
func (d Definition) forStep(index int) Definition {
	if len(d.Steps) == 0 {
		return d
	}
	step := d.Steps[index]
	d.Prompt, d.Output, d.Validate = step.Prompt, step.Output, step.Validate
	return d
}

// Roles returns the distinct roles the pipeline uses: the steps' roles in
// step order, then the fallback roles no step runs as its own role.
func (d Definition) Roles() []string {
	var roles []string
	seen := map[string]bool{}
	add := func(role string) {
		if !seen[role] {
			seen[role] = true
			roles = append(roles, role)
		}
	}
	for _, step := range d.Pipeline() {
		add(step.Role())
	}
	for _, step := range d.Steps {
		for _, role := range step.Fallback {
			add(role)
		}
	}
	return roles
}

// servesStep reports whether an agent of role may run step: the step's own
// role or one of its fallback roles.
func servesStep(step StepSpec, role string) bool {
	if step.Role() == role {
		return true
	}
	for _, fallback := range step.Fallback {
		if fallback == role {
			return true
		}
	}
	return false
}

func (d Definition) stepNames() []string {
	names := make([]string, 0, len(d.Steps))
	for _, step := range d.Steps {
		names = append(names, step.Name)
	}
	return names
}

func stepPrefix(name string) string { return "steps." + name + "." }

// checkStepKeys rejects a steps list that is present but empty and a steps
// list combined with the top-level keys a step replaces. It reads the raw
// document because an empty list and an absent key decode alike.
func checkStepKeys(data []byte) error {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil // The typed decode already reported the document.
	}
	value, present := raw["steps"]
	if !present {
		return nil
	}
	if list, ok := value.([]any); !ok || len(list) == 0 {
		return errors.New("steps must list at least one step")
	}
	var clash []string
	for _, key := range []string{"prompt", "output", "validate"} {
		if _, ok := raw[key]; ok {
			clash = append(clash, key)
		}
	}
	if len(clash) > 0 {
		return fmt.Errorf("steps cannot be combined with top-level %s; move them into the steps", strings.Join(clash, ", "))
	}
	return nil
}

func validateSteps(d Definition) error {
	var clash []string
	if d.Prompt != (PromptSpec{}) {
		clash = append(clash, "prompt")
	}
	if d.Output != (OutputSpec{}) {
		clash = append(clash, "output")
	}
	if !reflect.DeepEqual(d.Validate, ValidateSpec{}) {
		clash = append(clash, "validate")
	}
	if len(clash) > 0 {
		return fmt.Errorf("steps cannot be combined with top-level %s; move them into the steps", strings.Join(clash, ", "))
	}
	if len(d.Steps) > MaxSteps {
		return fmt.Errorf("steps lists %d steps; at most %d are allowed", len(d.Steps), MaxSteps)
	}
	names := d.stepNames()
	seen := map[string]bool{}
	for index, step := range d.Steps {
		if step.Name == "" {
			return fmt.Errorf("steps[%d].name is required", index)
		}
		if !stepNamePattern.MatchString(step.Name) {
			return fmt.Errorf("steps[%d].name %q is invalid; use lowercase letters, digits, '-' and '_', starting with a letter or digit", index, step.Name)
		}
		if seen[step.Name] {
			return fmt.Errorf("steps[%d].name %q is used twice", index, step.Name)
		}
		seen[step.Name] = true
		if step.Agent != "" && !stepNamePattern.MatchString(step.Agent) {
			return fmt.Errorf("steps.%s.agent %q is not a valid role; use lowercase letters, digits, '-' and '_', starting with a letter or digit", step.Name, step.Agent)
		}
		prefix := stepPrefix(step.Name)
		if err := validateStage(prefix, step.Prompt, step.Output, step.Validate); err != nil {
			return err
		}
		if err := checkStageReferences(prefix, names, index, step.Output, step.Validate); err != nil {
			return err
		}
		if err := validateSendBack(d, index); err != nil {
			return err
		}
		if err := validateFallback(step); err != nil {
			return err
		}
	}
	return nil
}

// validateSendBack checks a step's send_back: it names an earlier step that
// has a follow-up template (the redo is sent through it), and max is a
// positive integer.
func validateSendBack(d Definition, index int) error {
	step := d.Steps[index]
	spec := step.SendBack
	if spec == nil {
		return nil
	}
	field := stepPrefix(step.Name) + "send_back"
	if index == 0 {
		return fmt.Errorf("%s is not allowed on the first step: there is no earlier step to send the unit back to", field)
	}
	if spec.To == "" {
		return fmt.Errorf("%s.to is required", field)
	}
	target := d.StepIndex(spec.To)
	switch {
	case target < 0:
		return fmt.Errorf("%s.to refers to unknown step %q", field, spec.To)
	case target >= index:
		return fmt.Errorf("%s.to must name an earlier step; %q does not run before %q", field, spec.To, step.Name)
	case d.Steps[target].Prompt.Followup == "":
		return fmt.Errorf("%s.to names step %q, which has no prompt.followup; the redo is sent through it", field, spec.To)
	}
	if spec.Max != nil && *spec.Max < 1 {
		return fmt.Errorf("%s.max must be a positive integer, got %d", field, *spec.Max)
	}
	return nil
}

// validateFallback checks a step's fallback list: valid role names, none of
// them the step's own role, none listed twice.
func validateFallback(step StepSpec) error {
	field := stepPrefix(step.Name) + "fallback"
	seen := map[string]bool{}
	for _, role := range step.Fallback {
		switch {
		case !stepNamePattern.MatchString(role):
			return fmt.Errorf("%s lists %q, which is not a valid role; use lowercase letters, digits, '-' and '_', starting with a letter or digit", field, role)
		case role == step.Role():
			return fmt.Errorf("%s lists %q, the step's own role; a fallback role must differ from it", field, role)
		case seen[role]:
			return fmt.Errorf("%s lists role %q twice", field, role)
		}
		seen[role] = true
	}
	return nil
}

// checkStageReferences checks the inline templates of one stage (output.path
// and validator arguments) for references to steps that do not finish
// before it. position is the stage's step index; -1 means a definition
// without steps, where no .Steps.<name> reference can resolve.
func checkStageReferences(prefix string, names []string, position int, output OutputSpec, validate ValidateSpec) error {
	if output.Path != "" {
		tmpl, err := template.New("output.path").Parse(output.Path)
		if err == nil {
			if err := checkTemplateSteps(prefix+"output.path", tmpl, names, position); err != nil {
				return err
			}
		}
	}
	for index, argument := range validate.Command {
		tmpl, err := template.New("arg").Parse(argument)
		if err != nil {
			continue
		}
		if err := checkTemplateSteps(fmt.Sprintf("%svalidate.command[%d]", prefix, index), tmpl, names, position); err != nil {
			return err
		}
	}
	return nil
}

// checkTemplateSteps walks every parse tree of tmpl (including {{define}}
// blocks) and rejects a .Steps.<name> reference to a step that is unknown,
// is the current step, or runs later. The walk is static: it sees the
// spellings .Steps.x, $.Steps.x, (.Steps).x and index .Steps "x"; a
// reference made through a rebound dot ({{with .Steps}}{{.x}}{{end}}) is
// only caught by the dry render of bp workflow check.
func checkTemplateSteps(field string, tmpl *template.Template, names []string, position int) error {
	var refs []string
	for _, item := range tmpl.Templates() {
		if item.Tree != nil && item.Tree.Root != nil {
			collectStepRefs(item.Tree.Root, &refs)
		}
	}
	for _, name := range refs {
		index := -1
		for i, candidate := range names {
			if candidate == name {
				index = i
			}
		}
		switch {
		case position < 0:
			return fmt.Errorf("%s refers to .Steps.%s, but the workflow has no steps", field, name)
		case index < 0:
			return fmt.Errorf("%s refers to unknown step %q", field, name)
		case index >= position:
			return fmt.Errorf("%s refers to step %q, which does not finish before step %q", field, name, names[position])
		}
	}
	return nil
}

func collectStepRefs(node parse.Node, refs *[]string) {
	if node == nil || reflect.ValueOf(node).IsNil() {
		return
	}
	switch n := node.(type) {
	case *parse.ListNode:
		for _, child := range n.Nodes {
			collectStepRefs(child, refs)
		}
	case *parse.ActionNode:
		collectStepRefs(n.Pipe, refs)
	case *parse.IfNode:
		collectBranch(&n.BranchNode, refs)
	case *parse.RangeNode:
		collectBranch(&n.BranchNode, refs)
	case *parse.WithNode:
		collectBranch(&n.BranchNode, refs)
	case *parse.TemplateNode:
		collectStepRefs(n.Pipe, refs)
	case *parse.PipeNode:
		for _, decl := range n.Decl {
			collectStepRefs(decl, refs)
		}
		for _, cmd := range n.Cmds {
			collectStepRefs(cmd, refs)
		}
	case *parse.CommandNode:
		if len(n.Args) >= 3 {
			if ident, ok := n.Args[0].(*parse.IdentifierNode); ok && ident.Ident == "index" && isStepsNode(n.Args[1]) {
				if name, ok := n.Args[2].(*parse.StringNode); ok {
					*refs = append(*refs, name.Text)
				}
			}
		}
		for _, arg := range n.Args {
			collectStepRefs(arg, refs)
		}
	case *parse.FieldNode:
		if len(n.Ident) >= 2 && n.Ident[0] == "Steps" {
			*refs = append(*refs, n.Ident[1])
		}
	case *parse.VariableNode:
		if len(n.Ident) >= 3 && n.Ident[0] == "$" && n.Ident[1] == "Steps" {
			*refs = append(*refs, n.Ident[2])
		}
	case *parse.ChainNode:
		if isStepsNode(n.Node) && len(n.Field) > 0 {
			*refs = append(*refs, n.Field[0])
		}
		collectStepRefs(n.Node, refs)
	}
}

func collectBranch(branch *parse.BranchNode, refs *[]string) {
	collectStepRefs(branch.Pipe, refs)
	if branch.List != nil {
		collectStepRefs(branch.List, refs)
	}
	if branch.ElseList != nil {
		collectStepRefs(branch.ElseList, refs)
	}
}

// isStepsNode reports whether node is .Steps or $.Steps, bare or wrapped in
// parentheses.
func isStepsNode(node parse.Node) bool {
	switch n := node.(type) {
	case *parse.FieldNode:
		return len(n.Ident) == 1 && n.Ident[0] == "Steps"
	case *parse.VariableNode:
		return len(n.Ident) == 2 && n.Ident[0] == "$" && n.Ident[1] == "Steps"
	case *parse.PipeNode:
		return len(n.Decl) == 0 && len(n.Cmds) == 1 && len(n.Cmds[0].Args) == 1 && isStepsNode(n.Cmds[0].Args[0])
	}
	return false
}

// ValidateRoles checks the agents of a run against the pipeline's roles:
// every role a step uses, as its own role or as a fallback role, has at
// least one agent, and every agent's role is used by some step in one of
// those ways. roles maps agent to role; an agent missing from it has
// DefaultRole.
func ValidateRoles(d Definition, agents []string, roles map[string]string) error {
	used := map[string]bool{}
	for _, role := range d.Roles() {
		used[role] = true
	}
	have := map[string]bool{}
	for _, agent := range agents {
		role := roles[agent]
		if role == "" {
			role = DefaultRole
		}
		if !used[role] {
			known := d.Roles()
			sort.Strings(known)
			if role == DefaultRole {
				return fmt.Errorf("agent %s has no role, but no step uses the default role; pass --agent <role>=%s (roles: %s)", agent, agent, strings.Join(known, ", "))
			}
			return fmt.Errorf("agent %s has role %q, which no step uses (roles: %s)", agent, role, strings.Join(known, ", "))
		}
		have[role] = true
	}
	for _, role := range d.Roles() {
		if !have[role] {
			if role == DefaultRole {
				return fmt.Errorf("role %q has no agent; pass --agent <name>", role)
			}
			return fmt.Errorf("role %q has no agent; pass --agent %s=<name>", role, role)
		}
	}
	return nil
}

// RunRoleMissing checks that every role the definition uses, as a step's
// role or as a fallback role, has at least one agent in the run. A run
// created by an older binary, or whose run.json was edited, may not: its
// units would wait for that role forever. The error names the role and the
// steps that use it.
func RunRoleMissing(d Definition, run Run) error {
	have := map[string]bool{}
	for _, agent := range run.Agents {
		have[run.RoleOf(agent)] = true
	}
	for _, role := range d.Roles() {
		if have[role] {
			continue
		}
		var users []string
		for _, step := range d.Steps {
			switch {
			case step.Role() == role:
				users = append(users, "step "+step.Name)
			case contains(step.Fallback, role):
				users = append(users, "step "+step.Name+" (fallback)")
			}
		}
		return fmt.Errorf("run %s has no agent for role %q, which %s uses; its units could never run that step; start a new run with --agent %s=<name>", run.ID, role, strings.Join(users, ", "), role)
	}
	return nil
}

// sampleStepResults stands in for the results of the steps before index
// during a dry render: every earlier step finished done after one round.
func sampleStepResults(d Definition, index int, outputs map[string]string) map[string]StepResult {
	results := map[string]StepResult{}
	for i := 0; i < index && i < len(d.Steps); i++ {
		step := d.Steps[i]
		results[step.Name] = StepResult{Output: outputs[step.Name], Result: "done", Agent: "agent", Rounds: 1}
	}
	return results
}

// CheckSampleTemplates renders every template with the first unit as a dry
// run. Each step sees the earlier steps as finished done.
func CheckSampleTemplates(workflowDir, workdir string, d Definition) error {
	units, err := LoadUnits(d, workdir)
	if err != nil {
		return err
	}
	if len(units) == 0 {
		return errors.New("units file contains no units")
	}
	unit := units[0]
	outputs := map[string]string{}
	started := time.Now().UTC().Format(time.RFC3339)
	for index, step := range d.Pipeline() {
		label := ""
		if d.MultiStep() {
			label = stepPrefix(step.Name)
		}
		data := TemplateData{Unit: unit.Data, Key: unit.Key, Round: 1, Attempt: 1, Workdir: workdir, Started: started, Step: step.Name, Steps: sampleStepResults(d, index, outputs)}
		if _, err := RenderFile(workflowDir, step.Prompt.Template, data); err != nil {
			return fmt.Errorf("render %sprompt with first unit: %w", label, err)
		}
		if step.Output.Path != "" {
			out, err := RenderText("output.path", step.Output.Path, data)
			if err != nil {
				return fmt.Errorf("render %soutput.path with first unit: %w", label, err)
			}
			data.Output = out
			outputs[step.Name] = out
		}
		if step.Prompt.Followup != "" {
			if _, err := RenderFile(workflowDir, step.Prompt.Followup, data); err != nil {
				return fmt.Errorf("render %sfollowup with first unit: %w", label, err)
			}
		}
		if d.MultiStep() {
			// A later attempt (rerun, send-back redo, fallback, retry) sees
			// .Attempt above 1 and .Previous: branches on them are checked
			// too.
			later := data
			later.Attempt, later.Previous = 2, StepAttempt{Agent: "sample-agent", Result: "failed", Reason: "sample reason", Output: data.Output}
			if _, err := RenderFile(workflowDir, step.Prompt.Template, later); err != nil {
				return fmt.Errorf("render %sprompt with first unit as attempt 2: %w", label, err)
			}
			if step.Prompt.Followup != "" {
				if _, err := RenderFile(workflowDir, step.Prompt.Followup, later); err != nil {
					return fmt.Errorf("render %sfollowup with first unit as attempt 2: %w", label, err)
				}
			}
		}
		for i, arg := range step.Validate.Command {
			if _, err := RenderText(fmt.Sprintf("validate.command[%d]", i), arg, data); err != nil {
				return fmt.Errorf("render %svalidate.command[%d] with first unit: %w", label, i, err)
			}
		}
	}
	return nil
}
