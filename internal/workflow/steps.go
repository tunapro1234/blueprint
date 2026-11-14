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

// Roles returns the distinct roles the pipeline uses, in step order.
func (d Definition) Roles() []string {
	var roles []string
	seen := map[string]bool{}
	for _, step := range d.Pipeline() {
		if !seen[step.Role()] {
			seen[step.Role()] = true
			roles = append(roles, step.Role())
		}
	}
	return roles
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
// every role a step uses has at least one agent, and every agent's role is
// used by some step. roles maps agent to role; an agent missing from it has
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
		data := TemplateData{Unit: unit.Data, Key: unit.Key, Round: 1, Workdir: workdir, Started: started, Step: step.Name, Steps: sampleStepResults(d, index, outputs)}
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
		for i, arg := range step.Validate.Command {
			if _, err := RenderText(fmt.Sprintf("validate.command[%d]", i), arg, data); err != nil {
				return fmt.Errorf("render %svalidate.command[%d] with first unit: %w", label, i, err)
			}
		}
	}
	return nil
}
