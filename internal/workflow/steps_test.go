package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// stepBlock is one step of a test pipeline. Its prompt is "<name> {{.Key}}",
// its follow-up "fix <name> [{{.Problems}}] {{.Key}}" and its validator
// gets [check, <step>, <extra>, <key>], so the fake validator can tell steps
// and units apart.
func stepBlock(name, role, output, extra string, rounds, weakAfter int) string {
	block := "  - name: " + name + "\n"
	if role != "" {
		block += "    agent: " + role + "\n"
	}
	block += "    prompt:\n      template: " + name + ".md\n      followup: " + name + "-fix.md\n"
	if output != "" {
		block += "    output:\n      path: \"" + output + "\"\n"
	}
	return block + fmt.Sprintf("    validate:\n      command: [check, \"{{.Step}}\", %q, \"{{.Key}}\"]\n      rounds: %d\n      weak_after: %d\n", extra, rounds, weakAfter)
}

const stepsHeader = "name: pipeline\nversion: 1\nunits:\n  file: units.json\n  key: id\ntimeouts:\n  start: 1000h\n  unit: 1000h\nsteps:\n"

// pipelineYAML is a two-step coder/reviewer pipeline. The code step writes
// out/<key>.txt; the review step's prompt reads its output and agent and its
// validator gets the code step's result.
func pipelineYAML(coder, reviewer string, rounds, weakAfter int, extra string) string {
	return stepsHeader + stepBlock("code", coder, "out/{{.Key}}.txt", "none", rounds, weakAfter) + stepBlock("review", reviewer, "", "{{.Steps.code.Result}}", rounds, weakAfter) + extra
}

func stepFiles(names ...string) map[string]string {
	files := map[string]string{}
	for _, name := range names {
		files[name+".md"] = name + " {{.Key}}"
		files[name+"-fix.md"] = "fix " + name + " [{{.Problems}}] {{.Key}}"
	}
	return files
}

func pipelineFiles() map[string]string {
	files := stepFiles("code", "review")
	files["review.md"] = "review {{.Steps.code.Output}} {{.Steps.code.Agent}} {{.Key}}"
	return files
}

// stepChecks is a fake validator for pipelines. code decides the exit code
// per step, unit and call (0 when nil).
type stepChecks struct {
	mu    sync.Mutex
	args  [][]string
	calls map[string]int
	code  func(step, key string, call int) int
}

func (c *stepChecks) validate(_ context.Context, _ string, args []string, _ time.Duration) (int, string, string, error) {
	step, key := args[1], args[len(args)-1]
	c.mu.Lock()
	c.args = append(c.args, append([]string(nil), args...))
	c.calls[step+"/"+key]++
	call := c.calls[step+"/"+key]
	code := c.code
	c.mu.Unlock()
	if code == nil {
		return 0, "", "", nil
	}
	return code(step, key, call), "problems in " + step + " " + key, "", nil
}

func (c *stepChecks) argsFor(step, key string) [][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result [][]string
	for _, args := range c.args {
		if args[1] == step && args[len(args)-1] == key {
			result = append(result, args)
		}
	}
	return result
}

type pipeline struct {
	store   *Store
	run     Run
	driver  *scriptedDriver
	clock   *fakeClock
	workdir string
	checks  *stepChecks
	mu      sync.Mutex
	// log is every delivered prompt as "<compactions so far>|<agent>|<text>".
	log []string
}

// newPipeline creates a run of a workflow with steps. The fake driver writes
// out/<key>.txt whenever it delivers a code prompt.
func newPipeline(t *testing.T, yamlText string, files map[string]string, keys, agents []string, roles map[string]string) *pipeline {
	t.Helper()
	root := t.TempDir()
	source, workdir := filepath.Join(root, "workflow-src"), filepath.Join(root, "workdir")
	for _, dir := range []string{source, filepath.Join(workdir, "out")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	files["workflow.yaml"] = yamlText
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(source, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workdir, "units.json"), []byte(unitsJSON(keys...)), 0600); err != nil {
		t.Fatal(err)
	}
	definition, err := ReadDefinition(source)
	if err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock()
	store := NewStore(filepath.Join(root, "state"))
	store.Now = clock.Now
	run, err := store.CreateRunWithRoles(source, definition, workdir, agents, roles, "owner")
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline{store: store, run: run, driver: newScriptedDriver(clock), clock: clock, workdir: workdir, checks: &stepChecks{calls: map[string]int{}}}
	p.driver.fakeDriver.onSend = func(agent, text string) {
		if strings.HasPrefix(text, "code ") || strings.HasPrefix(text, "fix code ") {
			_ = os.WriteFile(filepath.Join(workdir, "out", promptKey(text)+".txt"), []byte("work"), 0600)
		}
		p.driver.fakeDriver.mu.Lock()
		compactions := p.driver.fakeDriver.compactCount
		p.driver.fakeDriver.mu.Unlock()
		p.mu.Lock()
		p.log = append(p.log, fmt.Sprintf("%d|%s|%s", compactions, agent, text))
		p.mu.Unlock()
	}
	return p
}

func (p *pipeline) engine(driver Driver, notes *notifications) *Engine {
	if driver == nil {
		driver = p.driver
	}
	engine := supervisedEngine(p.store, driver, p.clock, notes)
	// Idle workers poll; yield a little real time so they do not race the
	// fake clock ahead of the working agents.
	engine.Sleep = func(_ context.Context, d time.Duration) error {
		p.clock.Advance(d)
		time.Sleep(20 * time.Microsecond)
		return nil
	}
	engine.Validator = p.checks.validate
	return engine
}

func (p *pipeline) runEngine(t *testing.T, retryFailed bool) {
	t.Helper()
	if err := p.engine(nil, nil).Run(context.Background(), p.run.ID, retryFailed); err != nil {
		t.Fatal(err)
	}
}

func (p *pipeline) sends() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]string, len(p.log))
	for i, entry := range p.log {
		result[i] = strings.SplitN(entry, "|", 2)[1]
	}
	return result
}

func (p *pipeline) compactionsAt(index int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.SplitN(p.log[index], "|", 2)[0]
}

// readPipeline reads a definition with its prompt files in place.
func readPipeline(t *testing.T, yamlText string, files map[string]string) (Definition, error) {
	t.Helper()
	root := t.TempDir()
	writeDefinition(t, root, yamlText, "", "")
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return ReadDefinition(root)
}

func stepNamesOf(row TimeRow) []string {
	var names []string
	for _, step := range row.Steps {
		names = append(names, fmt.Sprintf("%s/%s/%s/%d", step.Name, step.Agent, step.Result, step.Attempt))
	}
	return names
}

func coderReviewer() ([]string, map[string]string) {
	return []string{"agent-a", "agent-b"}, map[string]string{"agent-a": "coder", "agent-b": "reviewer"}
}

func TestStepDefinitionRules(t *testing.T) {
	steps := func(blocks ...string) string { return stepsHeader + strings.Join(blocks, "") }
	plain := func(name, role string) string { return stepBlock(name, role, "", "none", 2, 1) }
	for _, test := range []struct {
		name  string
		yaml  string
		files map[string]string
		want  string
	}{
		{"steps with top-level prompt", steps(plain("code", "")) + "prompt:\n  template: code.md\n", stepFiles("code"), "steps cannot be combined with top-level prompt; move them into the steps"},
		{"steps with top-level output and validate", steps(plain("code", "")) + "output:\n  path: x.txt\nvalidate:\n  rounds: 2\n", stepFiles("code"), "steps cannot be combined with top-level output, validate"},
		{"empty steps", strings.TrimSuffix(stepsHeader, "\n") + " []\n", nil, "steps must list at least one step"},
		{"missing name", steps(strings.Replace(plain("code", ""), "  - name: code\n", "  - agent: coder\n", 1)), stepFiles("code"), "steps[0].name is required"},
		{"invalid name", steps(plain("code", ""), strings.Replace(plain("review", ""), "name: review", "name: Review", 1)), stepFiles("code", "review"), `steps[1].name "Review" is invalid`},
		{"duplicate name", steps(plain("code", ""), plain("code", "")), stepFiles("code"), `steps[1].name "code" is used twice`},
		{"invalid role", steps(plain("code", "Coder")), stepFiles("code"), `steps.code.agent "Coder" is not a valid role`},
		{"step without prompt", steps("  - name: code\n"), nil, "steps.code.prompt.template is required"},
		{"forward reference in prompt", steps(plain("code", ""), plain("review", "")), func() map[string]string {
			files := stepFiles("code", "review")
			files["code.md"] = "code {{.Steps.review.Output}} {{.Key}}"
			return files
		}(), `steps.code.prompt.template refers to step "review", which does not finish before step "code"`},
		{"self reference in follow-up", steps(plain("code", ""), plain("review", "")), func() map[string]string {
			files := stepFiles("code", "review")
			files["review-fix.md"] = `fix {{index .Steps "review" | printf "%v"}} {{.Key}}`
			return files
		}(), `steps.review.prompt.followup refers to step "review", which does not finish before step "review"`},
		{"unknown step", steps(plain("code", ""), plain("review", "")), func() map[string]string {
			files := stepFiles("code", "review")
			files["review.md"] = "review {{$.Steps.coding.Output}} {{.Key}}"
			return files
		}(), `steps.review.prompt.template refers to unknown step "coding"`},
		{"forward reference in validator", steps(stepBlock("code", "", "", "{{.Steps.review.Result}}", 2, 1), plain("review", "")), stepFiles("code", "review"), `steps.code.validate.command[2] refers to step "review", which does not finish before step "code"`},
		{"forward reference in output path", steps(stepBlock("code", "", "out/{{.Steps.review.Output}}", "none", 2, 1), plain("review", "")), stepFiles("code", "review"), `steps.code.output.path refers to step "review", which does not finish before step "code"`},
		{"steps reference without steps", baseYAML(""), map[string]string{"prompt.md": "score {{.Steps.code.Output}} {{.Key}}"}, "prompt.template refers to .Steps.code, but the workflow has no steps"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeDefinition(t, root, test.yaml, "", `[{"id":"one"}]`)
			for name, text := range test.files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReadDefinition(root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
	t.Run("too many steps", func(t *testing.T) {
		var blocks []string
		files := map[string]string{}
		for i := 0; i <= MaxSteps; i++ {
			name := fmt.Sprintf("s%d", i)
			blocks = append(blocks, plain(name, ""))
			for file, text := range stepFiles(name) {
				files[file] = text
			}
		}
		root := t.TempDir()
		writeDefinition(t, root, steps(blocks...), "", `[{"id":"one"}]`)
		for name, text := range files {
			if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ReadDefinition(root); err == nil || !strings.Contains(err.Error(), "steps lists 17 steps; at most 16 are allowed") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("valid pipeline renders every step with earlier steps done", func(t *testing.T) {
		root := t.TempDir()
		writeDefinition(t, root, pipelineYAML("coder", "reviewer", 2, 1, ""), "", `[{"id":"one"}]`)
		for name, text := range pipelineFiles() {
			if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
		}
		d, err := ReadDefinition(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckSampleTemplates(root, root, d); err != nil {
			t.Fatal(err)
		}
		if !d.MultiStep() || !reflect.DeepEqual(d.Roles(), []string{"coder", "reviewer"}) || d.StepIndex("review") != 1 || d.StepIndex("nope") != -1 {
			t.Fatalf("definition %#v roles %v", d.Steps, d.Roles())
		}
		// Round trip: the steps survive YAML and JSON; top-level stage keys
		// stay absent.
		encoded, err := yaml.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		back, err := readPipeline(t, string(encoded), pipelineFiles())
		if err != nil || !reflect.DeepEqual(back.Steps, d.Steps) {
			t.Fatalf("yaml round trip %v\n%s", err, encoded)
		}
		for _, key := range []string{"\nprompt:", "\noutput:", "\nvalidate:"} {
			if strings.Contains(string(encoded), key) {
				t.Fatalf("yaml carries an empty top-level stage %q:\n%s", key, encoded)
			}
		}
	})
	t.Run("definitions without steps round trip without steps", func(t *testing.T) {
		d, err := parseDefinitionYAML(baseYAML(""))
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := yaml.Marshal(d)
		jsonText, _ := json.Marshal(d)
		if d.MultiStep() || strings.Contains(string(encoded), "steps") || strings.Contains(string(jsonText), "steps") || d.StepIndex("") != 0 {
			t.Fatalf("no-steps definition grew steps:\n%s\n%s", encoded, jsonText)
		}
	})
}

func TestStepRoles(t *testing.T) {
	d, err := readPipeline(t, pipelineYAML("", "reviewer", 2, 1, ""), pipelineFiles())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		agents []string
		roles  map[string]string
		want   string
	}{
		{"every role covered", []string{"a", "b", "c"}, map[string]string{"b": "reviewer", "c": "reviewer"}, ""},
		{"role without agent", []string{"a"}, nil, `role "reviewer" has no agent; pass --agent reviewer=<name>`},
		{"default role without agent", []string{"b"}, map[string]string{"b": "reviewer"}, `role "default" has no agent; pass --agent <name>`},
		{"unused role", []string{"a", "b", "c"}, map[string]string{"b": "reviewer", "c": "tester"}, `agent c has role "tester", which no step uses (roles: default, reviewer)`},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRoles(d, test.agents, test.roles)
			if (test.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
	named, err := readPipeline(t, pipelineYAML("coder", "reviewer", 2, 1, ""), pipelineFiles())
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRoles(named, []string{"a", "b"}, map[string]string{"b": "reviewer"}); err == nil || !strings.Contains(err.Error(), "agent a has no role, but no step uses the default role; pass --agent <role>=a (roles: coder, reviewer)") {
		t.Fatalf("agent without a role: %v", err)
	}
	t.Run("run records roles only with steps", func(t *testing.T) {
		agents, roles := coderReviewer()
		p := newPipeline(t, pipelineYAML("coder", "reviewer", 2, 1, ""), pipelineFiles(), []string{"u1"}, agents, roles)
		if !reflect.DeepEqual(p.run.Roles, roles) || p.run.RoleOf("agent-b") != "reviewer" {
			t.Fatalf("roles %v", p.run.Roles)
		}
		clock := newFakeClock()
		store, run := makeRun(t, clock, baseYAML(""), unitsJSON("u1"), "{{.Key}}", "", "agent-a")
		if run.Roles != nil || run.RoleOf("agent-a") != DefaultRole {
			t.Fatalf("single-step run has roles %v", run.Roles)
		}
		data, err := os.ReadFile(filepath.Join(store.RunPath(run.ID), "run.json"))
		if err != nil || strings.Contains(string(data), `"roles"`) {
			t.Fatalf("run.json %s %v", data, err)
		}
		source := filepath.Dir(filepath.Join(p.store.SnapshotDir(p.run.ID), "workflow.yaml"))
		definition, _ := ReadDefinition(source)
		if _, err := p.store.CreateRunWithRoles(source, definition, p.workdir, []string{"agent-a"}, map[string]string{"agent-a": "coder"}, "owner"); err == nil || !strings.Contains(err.Error(), `role "reviewer" has no agent; pass --agent reviewer=<name>`) {
			t.Fatalf("a run missing a role was created: %v", err)
		}
	})
}

func TestStepsHandOffBetweenRoles(t *testing.T) {
	agents, roles := coderReviewer()
	p := newPipeline(t, pipelineYAML("coder", "reviewer", 2, 1, ""), pipelineFiles(), []string{"u1", "u2"}, agents, roles)
	var observed []AgentStatus
	var observedReady map[string]int
	p.driver.beforeSend = func(agent, key string) {
		if agent == "agent-b" && key == "u1" && observed == nil {
			status, err := p.store.Status(p.run.ID)
			if err != nil {
				t.Error(err)
			}
			observed, observedReady = status.Agents, status.Counts.Ready
		}
	}
	p.runEngine(t, false)
	if codes := p.driver.sendsFor("agent-a"); !reflect.DeepEqual(codes, []string{"code u1", "code u2"}) {
		t.Fatalf("coder sends %q", codes)
	}
	reviews := p.driver.sendsFor("agent-b")
	if len(reviews) != 2 || reviews[0] != "review out/u1.txt agent-a u1" || reviews[1] != "review out/u2.txt agent-a u2" {
		t.Fatalf("reviewer sends %q", reviews)
	}
	if args := p.checks.argsFor("review", "u1"); len(args) != 1 || !reflect.DeepEqual(args[0], []string{"check", "review", "done", "u1"}) {
		t.Fatalf("review validator args %q", args)
	}
	if args := p.checks.argsFor("code", "u1"); len(args) != 1 || !reflect.DeepEqual(args[0], []string{"check", "code", "none", "u1"}) {
		t.Fatalf("code validator args %q", args)
	}
	for _, agent := range observed {
		if agent.Agent == "agent-b" && (agent.CurrentUnit != "u1" || agent.CurrentStep != "review") {
			t.Fatalf("status while reviewing: %#v", observed)
		}
	}
	if observed == nil || observedReady["review"] > 1 {
		t.Fatalf("status was not observed or ready counts are wrong: %#v %v", observed, observedReady)
	}
	events, _, err := p.store.Events(p.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Unit != "" && (event.Step == "" || event.Attempt != 1) {
			t.Fatalf("unit event without step: %#v", event)
		}
	}
	stepDone := eventsWithState(t, p.store, p.run.ID, StateStepDone)
	if len(stepDone) != 2 || stepDone[0].Step != "code" || stepDone[0].StepRecord == nil || stepDone[0].StepRecord.Row.Agent != "agent-a" || stepDone[0].StepRecord.Output != "out/u1.txt" || stepDone[0].StepRecord.Row.Result != "done" {
		t.Fatalf("step_done events %#v", stepDone)
	}
	finals := eventsWithState(t, p.store, p.run.ID, "done")
	if len(finals) != 2 || finals[0].Step != "review" || finals[0].Agent != "agent-b" {
		t.Fatalf("done events %#v", finals)
	}
	rows := timesByUnit(t, p.store, p.run.ID)
	row := rows["u1"]
	if row.Result != "done" || row.Agent != "agent-b" || row.Rounds != 2 || len(row.RoundSeconds) != 2 || !reflect.DeepEqual(stepNamesOf(row), []string{"code/agent-a/done/1", "review/agent-b/done/1"}) {
		t.Fatalf("u1 row %#v steps %v", row, stepNamesOf(row))
	}
	if row.Steps[0].Output != "out/u1.txt" || row.Steps[1].QueuedSeconds < 0 || row.Steps[0].Rounds != 1 {
		t.Fatalf("u1 step rows %#v", row.Steps)
	}
	if runStatus(t, p.store, p.run.ID) != "done" {
		t.Fatalf("status %s", runStatus(t, p.store, p.run.ID))
	}
	stats := SummarizeTimes([]TimeRow{rows["u1"], rows["u2"]})
	if len(stats.Steps) != 2 || stats.Steps[0].Name != "code" || stats.Steps[1].Name != "review" || stats.Steps[0].Units != 2 || stats.Steps[1].Units != 2 || stats.Steps[1].Failed != 0 {
		t.Fatalf("step stats %#v", stats.Steps)
	}
}

func TestStepsStickyContinuationSkipsCompaction(t *testing.T) {
	p := newPipeline(t, pipelineYAML("", "", 2, 1, "compact:\n  every_units: 1\n"), pipelineFiles(), []string{"u1", "u2"}, []string{"agent-a"}, nil)
	p.runEngine(t, false)
	want := []string{"agent-a|code u1", "agent-a|review out/u1.txt agent-a u1", "agent-a|code u2", "agent-a|review out/u2.txt agent-a u2"}
	if got := p.sends(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sends %q", got)
	}
	// every_units counts work items; the same agent's next step of a unit
	// is never preceded by a compaction.
	if p.compactionsAt(0) != p.compactionsAt(1) || p.compactionsAt(2) != p.compactionsAt(3) || p.compactionsAt(1) == p.compactionsAt(2) {
		t.Fatalf("compactions between sends: %q", p.log)
	}
	if p.driver.compactWhileActive {
		t.Fatal("compacted a working agent")
	}
	row := timesByUnit(t, p.store, p.run.ID)["u1"]
	if row.Steps[1].QueuedSeconds != 0 || row.Steps[1].Agent != "agent-a" {
		t.Fatalf("sticky step row %#v", row.Steps[1])
	}
	// The compaction before code u2 reset the count; two work items since.
	run, _ := p.store.LoadRun(p.run.ID)
	if run.AgentUnits["agent-a"] != 2 {
		t.Fatalf("agent units count work items: %v", run.AgentUnits)
	}
}

func TestStepsContinuationBeforeNewUnits(t *testing.T) {
	yamlText := stepsHeader + stepBlock("code", "", "", "none", 2, 1) + stepBlock("review", "reviewer", "", "none", 2, 1) + stepBlock("polish", "", "", "none", 2, 1)
	p := newPipeline(t, yamlText, stepFiles("code", "review", "polish"), []string{"u1", "u2", "u3"}, []string{"agent-a", "agent-b"}, map[string]string{"agent-b": "reviewer"})
	waited := false
	p.driver.beforeSend = func(agent, key string) {
		if agent != "agent-a" || key != "u2" || waited {
			return
		}
		waited = true
		// Hold agent-a's second unit until u1 finished review, so u1's
		// polish and u3's code are both ready when agent-a is free.
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if latestState(t, p.store, p.run.ID)["u1"].Step == "review" && latestState(t, p.store, p.run.ID)["u1"].State == StateStepDone {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Error("u1 never finished review")
	}
	p.runEngine(t, false)
	if got := p.driver.sendsFor("agent-a"); len(got) != 6 || got[0] != "code u1" || got[1] != "code u2" || got[2] != "polish u1" {
		t.Fatalf("agent-a did not take the continuation before a new unit: %q", got)
	}
	for _, key := range []string{"u1", "u2", "u3"} {
		if row := timesByUnit(t, p.store, p.run.ID)[key]; row.Result != "done" || len(row.Steps) != 3 {
			t.Fatalf("%s row %#v", key, row)
		}
	}
}

func TestStepsWeakHandsOffAndFailedEndsUnit(t *testing.T) {
	agents, roles := coderReviewer()
	p := newPipeline(t, pipelineYAML("coder", "reviewer", 1, 1, ""), pipelineFiles(), []string{"u1", "u2", "u3"}, agents, roles)
	p.checks.code = func(step, key string, _ int) int {
		switch {
		case step == "code" && key == "u1":
			return 3 // weak
		case step == "code" && key == "u2":
			return 2 // failed
		}
		return 0
	}
	p.runEngine(t, false)
	reviews := p.driver.sendsFor("agent-b")
	if len(reviews) != 2 || promptKey(reviews[0]) != "u1" || promptKey(reviews[1]) != "u3" {
		t.Fatalf("reviews %q", reviews)
	}
	if args := p.checks.argsFor("review", "u1"); len(args) != 1 || args[0][2] != "weak" {
		t.Fatalf("review saw the code result %q", args)
	}
	rows := timesByUnit(t, p.store, p.run.ID)
	if row := rows["u1"]; row.Result != "weak" || row.Reason != "step code weak: problems in code u1" || !reflect.DeepEqual(stepNamesOf(row), []string{"code/agent-a/weak/1", "review/agent-b/done/1"}) {
		t.Fatalf("weak unit row %#v %v", row, stepNamesOf(row))
	}
	if row := rows["u2"]; row.Result != "failed" || row.Agent != "agent-a" || row.Reason != "validator exited with code 2" || !reflect.DeepEqual(stepNamesOf(row), []string{"code/agent-a/failed/1"}) {
		t.Fatalf("failed unit row %#v %v", row, stepNamesOf(row))
	}
	if failed := latestState(t, p.store, p.run.ID)["u2"]; failed.State != "failed" || failed.Step != "code" {
		t.Fatalf("failed event %#v", failed)
	}
	status, err := p.store.Status(p.run.ID)
	if err != nil || status.Counts.Done != 1 || status.Counts.Weak != 1 || status.Counts.Failed != 1 || status.Counts.Pending != 0 {
		t.Fatalf("counts %#v %v", status.Counts, err)
	}
	stats := SummarizeTimes([]TimeRow{rows["u1"], rows["u2"], rows["u3"]})
	if stats.Steps[0].Failed != 1 || stats.Steps[0].Units != 3 || stats.Steps[1].Units != 2 {
		t.Fatalf("step stats %#v", stats.Steps)
	}
}

func TestStepsBreakerCountsTheFailingStepAgent(t *testing.T) {
	agents, roles := coderReviewer()
	p := newPipeline(t, pipelineYAML("coder", "reviewer", 1, 1, "supervise:\n  breaker:\n    failures: 2\n    open_for: 30m\n"), pipelineFiles(), []string{"u1", "u2", "u3"}, agents, roles)
	p.checks.code = func(step, _ string, _ int) int {
		if step == "review" {
			return 2
		}
		return 0
	}
	engine := p.engine(nil, nil)
	var mu sync.Mutex
	var statuses []string
	sleep := engine.Sleep
	engine.Sleep = func(ctx context.Context, d time.Duration) error {
		if run, err := p.store.LoadRun(p.run.ID); err == nil && strings.HasPrefix(run.Status, statusBreakersWaiting) {
			mu.Lock()
			statuses = append(statuses, run.Status)
			mu.Unlock()
		}
		return sleep(ctx, d)
	}
	if err := engine.Run(context.Background(), p.run.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, opened := range eventsWithState(t, p.store, p.run.ID, eventBreakerOpen) {
		if opened.Agent != "agent-b" {
			t.Fatalf("a review failure opened %s's breaker", opened.Agent)
		}
	}
	if len(eventsWithState(t, p.store, p.run.ID, eventBreakerOpen)) == 0 {
		t.Fatal("the reviewer's breaker never opened")
	}
	if len(statuses) == 0 || !strings.HasPrefix(statuses[0], statusBreakersWaiting+" for role reviewer; next trial agent-b at ") {
		t.Fatalf("waiting status does not name the role: %q", statuses)
	}
	for key, row := range timesByUnit(t, p.store, p.run.ID) {
		if row.Result != "failed" || row.Agent != "agent-b" || len(row.Steps) != 2 {
			t.Fatalf("%s row %#v", key, row)
		}
	}
	if len(p.driver.sendsFor("agent-a")) != 3 || runStatus(t, p.store, p.run.ID) != "done" {
		t.Fatalf("coder sends %q status %s", p.driver.sendsFor("agent-a"), runStatus(t, p.store, p.run.ID))
	}
}

func TestStepsStopAtBoundaryAndResume(t *testing.T) {
	p := newPipeline(t, pipelineYAML("", "", 2, 1, "compact:\n  every_units: 1\n"), pipelineFiles(), []string{"u1", "u2"}, []string{"agent-a"}, nil)
	p.checks.code = func(step, key string, _ int) int {
		if step == "code" && key == "u1" {
			if _, err := p.store.RequestStop(p.run.ID); err != nil {
				t.Error(err)
			}
		}
		return 0
	}
	p.runEngine(t, false)
	if got := p.sends(); !reflect.DeepEqual(got, []string{"agent-a|code u1"}) {
		t.Fatalf("a stop did not hold at the step boundary: %q", got)
	}
	status, err := p.store.Status(p.run.ID)
	if err != nil || status.Status != "stopped" || status.Counts.Ready["review"] != 1 || status.Counts.Pending != 2 {
		t.Fatalf("status after stop %#v %v", status, err)
	}
	// A torn last line (a crash mid-append) must not lose the step.
	logPath := filepath.Join(p.store.RunPath(p.run.ID), "units.jsonl")
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"unit":"u1","state":"wor`); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := p.store.Resume(p.run.ID, ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
	p.checks.code = nil
	p.runEngine(t, false)
	want := []string{"agent-a|code u1", "agent-a|review out/u1.txt agent-a u1", "agent-a|code u2", "agent-a|review out/u2.txt agent-a u2"}
	if got := p.sends(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sends after resume %q", got)
	}
	if p.compactionsAt(0) != p.compactionsAt(1) {
		t.Fatalf("compacted between two steps of u1 across a restart: %q", p.log)
	}
	rows := timesByUnit(t, p.store, p.run.ID)
	if !reflect.DeepEqual(stepNamesOf(rows["u1"]), []string{"code/agent-a/done/1", "review/agent-a/done/1"}) || rows["u1"].Result != "done" || runStatus(t, p.store, p.run.ID) != "done" {
		t.Fatalf("u1 row %#v status %s", rows["u1"], runStatus(t, p.store, p.run.ID))
	}
}

func TestStepsRecoverInFlightSecondStep(t *testing.T) {
	agents, roles := coderReviewer()
	p := newPipeline(t, pipelineYAML("coder", "reviewer", 2, 1, ""), pipelineFiles(), []string{"u1"}, agents, roles)
	ctx, cancel := context.WithCancel(context.Background())
	p.driver.beforeSend = func(agent, _ string) {
		if agent == "agent-b" {
			cancel()
		}
	}
	if err := p.engine(nil, nil).Run(ctx, p.run.ID, false); err == nil {
		t.Fatal("a cancelled run returned no error")
	}
	if latest := latestState(t, p.store, p.run.ID)["u1"]; latest.Step != "review" || terminalState(latest.State) {
		t.Fatalf("u1 is not in flight at review: %#v", latest)
	}
	p.driver.beforeSend = nil
	p.runEngine(t, false)
	if len(p.driver.sendsFor("agent-a")) != 1 || len(p.driver.sendsFor("agent-b")) != 1 {
		t.Fatalf("recovery resent a step: a=%q b=%q", p.driver.sendsFor("agent-a"), p.driver.sendsFor("agent-b"))
	}
	row := timesByUnit(t, p.store, p.run.ID)["u1"]
	if row.Result != "done" || !reflect.DeepEqual(stepNamesOf(row), []string{"code/agent-a/done/1", "review/agent-b/done/1"}) {
		t.Fatalf("recovered row %#v %v", row, stepNamesOf(row))
	}
}

func TestStepsRetryFailedRestartsAtFailedStep(t *testing.T) {
	agents, roles := coderReviewer()
	p := newPipeline(t, pipelineYAML("coder", "reviewer", 1, 1, ""), pipelineFiles(), []string{"u1", "u2"}, agents, roles)
	p.checks.code = func(step, key string, call int) int {
		if step == "review" && key == "u1" && call == 1 {
			// Stop while u2 still waits, so the run can be resumed.
			if _, err := p.store.RequestStop(p.run.ID); err != nil {
				t.Error(err)
			}
			return 2
		}
		return 0
	}
	p.runEngine(t, false)
	if failed := latestState(t, p.store, p.run.ID)["u1"]; failed.State != "failed" || failed.Step != "review" {
		t.Fatalf("u1 %#v", failed)
	}
	if _, err := p.store.Resume(p.run.ID, ResumeOptions{RetryFailed: true}); err != nil {
		t.Fatal(err)
	}
	if err := p.engine(nil, nil).Run(context.Background(), p.run.ID, true); err != nil {
		t.Fatal(err)
	}
	if codes := p.driver.sendsFor("agent-a"); len(codes) != 2 || promptKey(codes[0]) != "u1" || promptKey(codes[1]) != "u2" {
		t.Fatalf("--retry-failed reran the code step: %q", codes)
	}
	reviews, retried := p.driver.sendsFor("agent-b"), 0
	for _, review := range reviews {
		if review == "review out/u1.txt agent-a u1" {
			retried++
		}
	}
	if len(reviews) != 3 || retried != 2 {
		t.Fatalf("reviews %q", reviews)
	}
	if done := latestState(t, p.store, p.run.ID)["u1"]; done.State != "done" || done.Step != "review" || done.Attempt != 2 {
		t.Fatalf("u1 %#v", done)
	}
	rows, _, err := p.store.Times(p.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var u1 []TimeRow
	for _, row := range rows {
		if row.Unit == "u1" {
			u1 = append(u1, row)
		}
	}
	// One row per attempt: the retry's row holds only the step it ran.
	if len(u1) != 2 || !reflect.DeepEqual(stepNamesOf(u1[0]), []string{"code/agent-a/done/1", "review/agent-b/failed/1"}) || !reflect.DeepEqual(stepNamesOf(u1[1]), []string{"review/agent-b/done/2"}) || u1[1].Result != "done" || u1[1].Rounds != 1 || u1[1].Agent != "agent-b" || runStatus(t, p.store, p.run.ID) != "done" {
		t.Fatalf("u1 rows %#v status %s", u1, runStatus(t, p.store, p.run.ID))
	}
	stats := SummarizeTimes(rows)
	if stats.Count != 3 || stats.Done != 2 || stats.Failed != 1 || stats.Steps[0].Units != 2 || stats.Steps[1].Units != 3 || stats.Steps[1].Failed != 1 {
		t.Fatalf("step stats count each attempt once: %#v", stats.Steps)
	}
}

func TestStepsUnitBudgetCoversAllSteps(t *testing.T) {
	agents, roles := coderReviewer()
	p := newPipeline(t, pipelineYAML("coder", "reviewer", 3, 3, "budget:\n  unit_tokens: 400000\n"), pipelineFiles(), []string{"u1"}, agents, roles)
	p.driver.metered = true
	p.driver.perSend = TokenUse{Input: 200000, Output: 50000}
	p.checks.code = func(step, _ string, _ int) int {
		if step == "review" {
			return 1
		}
		return 0
	}
	p.runEngine(t, false)
	row := timesByUnit(t, p.store, p.run.ID)["u1"]
	// One send per step: the code step's 250k plus the review's 250k pass
	// the 400k ceiling, so the review gets no follow-up.
	if len(p.driver.sendsFor("agent-b")) != 1 || row.Result != "failed" || row.Reason != UnitBudgetReason(500000, 400000) {
		t.Fatalf("sends=%q row %#v", p.driver.sendsFor("agent-b"), row)
	}
	if row.Tokens == nil || row.Tokens.Input != 400000 || row.Steps[0].Tokens == nil || row.Steps[0].Tokens.Input != 200000 {
		t.Fatalf("unit tokens do not cover both steps: %#v", row.Tokens)
	}
}

func TestStepsRunBudgetAdmitsEachWorkItem(t *testing.T) {
	p := newPipeline(t, pipelineYAML("", "", 1, 1, "budget:\n  run_tokens: 600000\n"), pipelineFiles(), []string{"u1", "u2"}, []string{"agent-a"}, nil)
	p.driver.metered = true
	p.driver.perSend = TokenUse{Input: 200000, Output: 50000}
	p.runEngine(t, false)
	// 250k after code u1, 500k after review u1, 750k after code u2: the
	// sticky review of u2 is not admitted.
	if got := p.sends(); len(got) != 3 || got[2] != "agent-a|code u2" {
		t.Fatalf("sends %q", got)
	}
	run, err := p.store.LoadRun(p.run.ID)
	if err != nil || run.Status != "paused" || !strings.Contains(run.PauseReason, "run token budget exhausted") {
		t.Fatalf("run %s %q %v", run.Status, run.PauseReason, err)
	}
	if latest := latestState(t, p.store, p.run.ID)["u2"]; latest.State != StateStepDone || latest.Step != "code" {
		t.Fatalf("u2 %#v", latest)
	}
}

func TestStepsGuardrailHaltInSecondStep(t *testing.T) {
	agents, roles := coderReviewer()
	p := newPipeline(t, pipelineYAML("coder", "reviewer", 3, 3, failuresGuard), pipelineFiles(), []string{"u1"}, agents, roles)
	driver := newToolDriver(p.driver)
	p.checks.code = func(step, _ string, _ int) int {
		if step == "review" {
			driver.add("agent-b", toolCalls(p.clock.Now(), 10, "Bash", "same", ToolFailed)...)
			return 1
		}
		return 0
	}
	notes := &notifications{}
	if err := p.engine(driver, notes).Run(context.Background(), p.run.ID, false); err != nil {
		t.Fatal(err)
	}
	row := timesByUnit(t, p.store, p.run.ID)["u1"]
	if row.Result != "failed" || row.Reason != "guardrail: tool Bash failed 10 times in a row" || len(p.driver.sendsFor("agent-b")) != 1 || row.Steps[1].Activity == nil || row.Steps[1].Activity.Verdict != "halt" {
		t.Fatalf("row %#v", row)
	}
	halts := eventsWithState(t, p.store, p.run.ID, eventGuardrailHalt)
	if len(halts) != 1 || halts[0].Step != "review" || halts[0].Guardrail.Step != "review" || halts[0].Guardrail.Unit != "u1" {
		t.Fatalf("halt events %#v", halts)
	}
	if notes.count("guardrail halt on unit u1 step review") != 1 || notes.count("the step ends the unit failed") != 1 {
		t.Fatalf("notifications %q", notes.messages)
	}
	if latest := latestState(t, p.store, p.run.ID)["u1"]; latest.State != "failed" || latest.Step != "review" {
		t.Fatalf("u1 %#v", latest)
	}
}

func TestStepsApprovals(t *testing.T) {
	agents, roles := coderReviewer()
	approve := func(on string) string { return "approve:\n  on: [" + on + "]\n" }
	t.Run("park at the last step", func(t *testing.T) {
		p := newPipeline(t, pipelineYAML("coder", "reviewer", 1, 1, approve("weak")), pipelineFiles(), []string{"u1"}, agents, roles)
		p.checks.code = func(step, _ string, _ int) int {
			if step == "review" {
				return 3
			}
			return 0
		}
		p.runEngine(t, false)
		parked := latestState(t, p.store, p.run.ID)["u1"]
		if parked.State != StateAwaitingApproval || parked.Step != "review" || parked.Park == nil || parked.Park.StepRecord == nil || parked.Park.Proposed != "weak" {
			t.Fatalf("parked %#v", parked)
		}
		approvals, err := p.store.Approvals(p.run.ID)
		if err != nil || len(approvals) != 1 || approvals[0].Step != "review" || approvals[0].Agent != "agent-b" {
			t.Fatalf("approvals %#v %v", approvals, err)
		}
		report, err := p.store.Decide(p.run.ID, DecideOptions{Action: DecisionApprove, Units: []string{"u1"}, By: "boss"})
		if err != nil || report.Outcomes[0].Result != "weak" || report.Outcomes[0].Step != "review" || report.Outcomes[0].NextStep != "" {
			t.Fatalf("decide %#v %v", report, err)
		}
		row := timesByUnit(t, p.store, p.run.ID)["u1"]
		if row.Result != "weak" || row.Approval == nil || row.Approval.Decision != "approved" || !reflect.DeepEqual(stepNamesOf(row), []string{"code/agent-a/done/1", "review/agent-b/weak/1"}) {
			t.Fatalf("row %#v %v", row, stepNamesOf(row))
		}
	})
	failedCode := func(p *pipeline) {
		p.checks.code = func(step, key string, call int) int {
			if step == "code" && call == 1 {
				return 2
			}
			return 0
		}
	}
	t.Run("approve a failed middle step continues as weak", func(t *testing.T) {
		p := newPipeline(t, pipelineYAML("coder", "reviewer", 1, 1, approve("failed")), pipelineFiles(), []string{"u1"}, agents, roles)
		failedCode(p)
		p.runEngine(t, false)
		if parked := latestState(t, p.store, p.run.ID)["u1"]; parked.State != StateAwaitingApproval || parked.Step != "code" || len(p.driver.sendsFor("agent-b")) != 0 {
			t.Fatalf("u1 did not park at code: %#v", parked)
		}
		report, err := p.store.Decide(p.run.ID, DecideOptions{Action: DecisionApprove, Units: []string{"u1"}, By: "boss"})
		if err != nil || report.Outcomes[0].Step != "code" || report.Outcomes[0].NextStep != "review" || report.Outcomes[0].Result != "weak" {
			t.Fatalf("decide %#v %v", report, err)
		}
		if latest := latestState(t, p.store, p.run.ID)["u1"]; latest.State != StateStepDone || latest.Step != "code" || latest.Decision == nil {
			t.Fatalf("approval did not continue the unit: %#v", latest)
		}
		if _, err := p.store.Resume(p.run.ID, ResumeOptions{}); err != nil {
			t.Fatal(err)
		}
		p.runEngine(t, false)
		row := timesByUnit(t, p.store, p.run.ID)["u1"]
		if row.Result != "weak" || row.Reason != "step code weak: validator exited with code 2" || row.Approval == nil || row.Approval.Decision != "approved" || !reflect.DeepEqual(stepNamesOf(row), []string{"code/agent-a/weak/1", "review/agent-b/done/1"}) {
			t.Fatalf("row %#v %v", row, stepNamesOf(row))
		}
		if reviews := p.driver.sendsFor("agent-b"); len(reviews) != 1 || reviews[0] != "review out/u1.txt agent-a u1" {
			t.Fatalf("reviews %q", reviews)
		}
		if args := p.checks.argsFor("review", "u1"); len(args) != 1 || args[0][2] != "weak" {
			t.Fatalf("review validator args %q", args)
		}
	})
	t.Run("reject a failed middle step ends the unit", func(t *testing.T) {
		p := newPipeline(t, pipelineYAML("coder", "reviewer", 1, 1, approve("failed")), pipelineFiles(), []string{"u1"}, agents, roles)
		failedCode(p)
		p.runEngine(t, false)
		if _, err := p.store.Decide(p.run.ID, DecideOptions{Action: DecisionReject, Units: []string{"u1"}, Text: "no", By: "boss"}); err != nil {
			t.Fatal(err)
		}
		if latest := latestState(t, p.store, p.run.ID)["u1"]; latest.State != "failed" || latest.Step != "code" {
			t.Fatalf("u1 %#v", latest)
		}
		if runStatus(t, p.store, p.run.ID) != "done" || len(p.driver.sendsFor("agent-b")) != 0 {
			t.Fatalf("status %s", runStatus(t, p.store, p.run.ID))
		}
	})
	t.Run("edit at a failed middle step reaches its agent", func(t *testing.T) {
		p := newPipeline(t, pipelineYAML("coder", "reviewer", 1, 1, approve("failed")), pipelineFiles(), []string{"u1"}, agents, roles)
		failedCode(p)
		p.runEngine(t, false)
		if _, err := p.store.Decide(p.run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "use tabs", By: "boss"}); err != nil {
			t.Fatal(err)
		}
		if _, err := p.store.Resume(p.run.ID, ResumeOptions{}); err != nil {
			t.Fatal(err)
		}
		p.runEngine(t, false)
		if codes := p.driver.sendsFor("agent-a"); len(codes) != 2 || codes[1] != "fix code [use tabs] u1" {
			t.Fatalf("coder sends %q", codes)
		}
		row := timesByUnit(t, p.store, p.run.ID)["u1"]
		if row.Result != "done" || len(p.driver.sendsFor("agent-b")) != 1 || !reflect.DeepEqual(stepNamesOf(row), []string{"code/agent-a/done/1", "review/agent-b/done/1"}) || row.Approval == nil || row.Approval.Revisions != 1 {
			t.Fatalf("row %#v %v approval %#v", row, stepNamesOf(row), row.Approval)
		}
	})
	t.Run("edit at the last step and cancel an unclaimed revision", func(t *testing.T) {
		p := newPipeline(t, pipelineYAML("coder", "reviewer", 1, 1, approve("weak")), pipelineFiles(), []string{"u1", "u2"}, agents, roles)
		p.checks.code = func(step, _ string, call int) int {
			if step == "review" && call == 1 {
				return 3
			}
			return 0
		}
		p.runEngine(t, false)
		for _, key := range []string{"u1", "u2"} {
			if _, err := p.store.Decide(p.run.ID, DecideOptions{Action: DecisionRevise, Units: []string{key}, Text: "shorter", By: "boss"}); err != nil {
				t.Fatal(err)
			}
		}
		report, err := p.store.Decide(p.run.ID, DecideOptions{Action: DecisionApprove, Units: []string{"u2"}, By: "boss"})
		if err != nil || !report.Outcomes[0].CancelledRevision || report.Outcomes[0].Step != "review" {
			t.Fatalf("cancel %#v %v", report, err)
		}
		if _, err := p.store.Resume(p.run.ID, ResumeOptions{}); err != nil {
			t.Fatal(err)
		}
		p.runEngine(t, false)
		reviews := p.driver.sendsFor("agent-b")
		if len(reviews) != 3 || reviews[2] != "fix review [shorter] u1" || len(p.driver.sendsFor("agent-a")) != 2 {
			t.Fatalf("reviews %q", reviews)
		}
		rows := timesByUnit(t, p.store, p.run.ID)
		if rows["u1"].Result != "done" || rows["u2"].Result != "weak" || rows["u1"].Steps[1].Rounds != 2 {
			t.Fatalf("rows %#v", rows)
		}
	})
	t.Run("edit needs the parked step's follow-up", func(t *testing.T) {
		yamlText := strings.Replace(pipelineYAML("coder", "reviewer", 1, 1, approve("weak")), "      followup: review-fix.md\n", "", 1)
		p := newPipeline(t, yamlText, pipelineFiles(), []string{"u1"}, agents, roles)
		p.checks.code = func(step, _ string, _ int) int {
			if step == "review" {
				return 3
			}
			return 0
		}
		p.runEngine(t, false)
		if _, err := p.store.Decide(p.run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "x", By: "boss"}); err == nil || !strings.Contains(err.Error(), "step review has no prompt.followup") {
			t.Fatalf("edit without a follow-up: %v", err)
		}
	})
}

func TestStepRowsLoadWithoutSteps(t *testing.T) {
	var row TimeRow
	if err := json.Unmarshal([]byte(`{"unit":"u1","agent":"a","result":"done","rounds":1}`), &row); err != nil || row.Steps != nil {
		t.Fatalf("old row %#v %v", row, err)
	}
	encoded, _ := json.Marshal(row)
	if strings.Contains(string(encoded), "steps") {
		t.Fatalf("row without steps encodes them: %s", encoded)
	}
	if stats := SummarizeTimes([]TimeRow{row}); stats.Steps != nil {
		t.Fatalf("stats without steps %#v", stats.Steps)
	}
}

func TestStepsMeltdownCountsFailedUnits(t *testing.T) {
	p := newPipeline(t, pipelineYAML("", "", 1, 1, "supervise:\n  meltdown:\n    failures: 2\n    window: 30m\n"), pipelineFiles(), []string{"u1", "u2", "u3"}, []string{"agent-a"}, nil)
	p.checks.code = func(step, _ string, _ int) int {
		if step == "review" {
			return 2
		}
		return 0
	}
	p.runEngine(t, false)
	// Finished code steps are not failures; two failed units are.
	run, err := p.store.LoadRun(p.run.ID)
	if err != nil || run.Status != "paused" || !strings.HasPrefix(run.PauseReason, "meltdown: 2 units failed within 30m; last: u2 on agent-a") {
		t.Fatalf("run %s %q %v", run.Status, run.PauseReason, err)
	}
	if got := p.sends(); len(got) != 4 {
		t.Fatalf("sends %q", got)
	}
}

func TestStepsExampleDefinitionIsValid(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "examples", "workflow-steps")
	definition, err := ReadDefinition(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !definition.MultiStep() || len(definition.Steps) != 2 || strings.Join(definition.Roles(), ",") != "coder,reviewer" {
		t.Fatalf("example does not show steps and roles: %#v", definition.Steps)
	}
	if err := CheckSampleTemplates(dir, dir, definition); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRoles(definition, []string{"a", "b"}, map[string]string{"a": "coder", "b": "reviewer"}); err != nil {
		t.Fatal(err)
	}
}

// Every step attempt of a run is in exactly one times row, and each row's
// totals are the sums of its own steps: after a retry at step 2, after
// approving a failed middle step and after an accepted --edit revision at a
// middle step.
func TestStepsEachStepAttemptInOneRow(t *testing.T) {
	agents, roles := coderReviewer()
	p := newPipeline(t, pipelineYAML("coder", "reviewer", 1, 1, "approve:\n  on: [failed]\n"), pipelineFiles(), []string{"u1", "u2", "u3"}, agents, roles)
	p.driver.metered = true
	p.driver.perSend = TokenUse{Input: 1000, Output: 100}
	p.checks.code = func(step, key string, call int) int {
		switch {
		case step == "review" && key == "u1" && call == 1:
			return 2
		case step == "code" && key != "u1" && call == 1:
			return 2
		}
		return 0
	}
	p.runEngine(t, false)
	for key, step := range map[string]string{"u1": "review", "u2": "code", "u3": "code"} {
		if parked := latestState(t, p.store, p.run.ID)[key]; parked.State != StateAwaitingApproval || parked.Step != step {
			t.Fatalf("%s did not park at %s: %#v", key, step, parked)
		}
	}
	if _, err := p.store.Decide(p.run.ID, DecideOptions{Action: DecisionApprove, Units: []string{"u2"}, By: "boss"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.store.Decide(p.run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u3"}, Text: "again", By: "boss"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.store.Decide(p.run.ID, DecideOptions{Action: DecisionReject, Units: []string{"u1"}, Text: "no", By: "boss"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.store.Resume(p.run.ID, ResumeOptions{RetryFailed: true}); err != nil {
		t.Fatal(err)
	}
	if err := p.engine(nil, nil).Run(context.Background(), p.run.ID, true); err != nil {
		t.Fatal(err)
	}
	if runStatus(t, p.store, p.run.ID) != "done" || len(p.driver.sendsFor("agent-a")) != 4 {
		t.Fatalf("status %s coder sends %q", runStatus(t, p.store, p.run.ID), p.driver.sendsFor("agent-a"))
	}
	// The retried review still saw the kept code step.
	reviews, u1Reviews := p.driver.sendsFor("agent-b"), 0
	for _, review := range reviews {
		if review == "review out/u1.txt agent-a u1" {
			u1Reviews++
		}
	}
	if u1Reviews != 2 {
		t.Fatalf("reviews %q", reviews)
	}
	rows, _, err := p.store.Times(p.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	var keys []string
	for _, row := range rows {
		var work, validate, wait float64
		var rounds int
		var input, output int64
		for _, step := range row.Steps {
			id := fmt.Sprintf("%s/%s/%d", row.Unit, step.Name, step.Attempt)
			if seen[id] == 0 {
				keys = append(keys, id)
			}
			seen[id]++
			work, validate, wait, rounds = work+step.WorkSeconds, validate+step.ValidateSeconds, wait+step.WaitSeconds, rounds+step.Rounds
			if step.Tokens == nil {
				t.Fatalf("%s has no tokens", id)
			}
			input, output = input+step.Tokens.Input, output+step.Tokens.Output
		}
		near := func(a, b float64) bool { return a-b < 1e-6 && b-a < 1e-6 }
		if !near(row.WorkSeconds, work) || !near(row.ValidateSeconds, validate) || !near(row.WaitSeconds, wait) || row.Rounds != rounds || len(row.RoundSeconds) != rounds || row.Tokens == nil || row.Tokens.Input != input || row.Tokens.Output != output {
			t.Fatalf("%s row totals are not the sums of its steps: %#v tokens %#v", row.Unit, row, row.Tokens)
		}
	}
	sort.Strings(keys)
	want := []string{"u1/code/1", "u1/review/1", "u1/review/2", "u2/code/1", "u2/review/1", "u3/code/1", "u3/review/1"}
	if !reflect.DeepEqual(keys, want) || len(rows) != 4 {
		t.Fatalf("step attempts %v (rows %d)", seen, len(rows))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("%s is in %d rows", id, count)
		}
	}
	byUnit := map[string][]TimeRow{}
	for _, row := range rows {
		byUnit[row.Unit] = append(byUnit[row.Unit], row)
	}
	if u3 := byUnit["u3"][0]; u3.Result != "done" || u3.Steps[0].Rounds != 2 || u3.Approval == nil || u3.Approval.Revisions != 1 {
		t.Fatalf("u3 row %#v", u3)
	}
	if u2 := byUnit["u2"][0]; u2.Result != "weak" || u2.Steps[0].Result != "weak" {
		t.Fatalf("u2 row %#v", u2)
	}
	// Result counts count every row, one per unit attempt, as for retried
	// units without steps (TestTimesCountEveryAttemptWithoutSteps).
	stats := SummarizeTimes(rows)
	if stats.Count != 4 || stats.Done != 2 || stats.Weak != 1 || stats.Failed != 1 {
		t.Fatalf("stats %#v", stats)
	}
}

// Pins what bp workflow times does today with a unit retried in a workflow
// without steps: each row is one attempt and counts once.
func TestTimesCountEveryAttemptWithoutSteps(t *testing.T) {
	clock := newFakeClock()
	store, run := makeRun(t, clock, baseYAML(""), unitsJSON("u1", "u2"), "{{.Key}}", "", "agent-a")
	engine := supervisedEngine(store, newScriptedDriver(clock), clock, nil)
	calls := 0
	engine.Validator = func(context.Context, string, []string, time.Duration) (int, string, string, error) {
		calls++
		if calls == 1 {
			if _, err := store.RequestStop(run.ID); err != nil {
				t.Error(err)
			}
			return 2, "", "", nil
		}
		return 0, "", "", nil
	}
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resume(run.ID, ResumeOptions{RetryFailed: true}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(context.Background(), run.ID, true); err != nil {
		t.Fatal(err)
	}
	rows, _, err := store.Times(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	stats := SummarizeTimes(rows)
	if len(rows) != 3 || stats.Count != 3 || stats.Done != 2 || stats.Failed != 1 || stats.ByAgent["agent-a"].Count != 3 {
		t.Fatalf("rows %d stats %#v", len(rows), stats)
	}
	status, err := store.Status(run.ID)
	if err != nil || status.Counts.Done != 2 || status.Counts.Failed != 0 {
		t.Fatalf("status counts the latest state per unit: %#v %v", status.Counts, err)
	}
}
