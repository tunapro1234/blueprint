package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"blueprint/internal/api"
	"blueprint/internal/ledger"
	"blueprint/internal/workflow"
)

// bp work is the CLI over the work ledger (internal/ledger through
// api.Core.Work), the same operations bp mcp offers as bp_work_* tools and
// the API as POST /v1/work. Writes need a pane-proven identity, exactly as
// bp board put does; reads do not.

const workUsage = `usage:
  bp work projects
  bp work project create <project> --goal <text> [--root <dir>]... [--allow-unproven-reviewers]
  bp work project show|pause|resume|stop <project> [--reason <text>]
  bp work project root <project> <dir>

  bp work task add <project> <title...> [--spec <text>] [--dep <id,...>] [--verifier <agent>] [--attempts <n>]
  bp work task list <project> [--state <state>] [--owner <agent>]
  bp work task show <project> <id> [--history]
  bp work task dep <project> <id> <depends-on-id>
  bp work task claim <project> <id> [--lease <duration>]
  bp work task renew <project> <id> --fence <n> [--lease <duration>]
  bp work task checkpoint <project> <id> --fence <n> [--progress <0-100>] <note...>
  bp work task block <project> <id> --fence <n> <reason...>
  bp work task unblock <project> <id> --fence <n>
  bp work task release <project> <id> --fence <n> [<note...>]
  bp work task recover <project> <id> <reason...>
  bp work task submit <project> <id> --fence <n> --artifact <path>... [--revision <rev>] [--note <text>]
  bp work task verify <project> <id> (--pass|--fail) --checks <text> [--evidence <path>]... [--submission <n>]
                      [--run [--timeout <duration>] -- <validator argv...>]
  bp work task cancel|reopen <project> <id> <reason...>

  bp work finding add <project> <assertion...> [--source <path>]... [--revision <rev>] [--task <id>]
                      [--negative --conditions <text>]
  bp work finding list <project> [--kind finding|negative] [--status <status>]
  bp work finding show <project> <id> [--history]
  bp work finding verify|refute <project> <id> <evidence...>
  bp work finding check <project> <id>
  bp work finding revise <project> <id> [--assertion <text>] [--conditions <text>] [--source <path>]...

  bp work feed <project> [--after <cursor>] [--limit <n>] [--kind <k>]... [--id <id>]... [--involving <agent>] [--summary]
  bp work feed <project> --subscribe [--from <cursor>|start] [filters]
  bp work feed <project> --next [--limit <n>]
  bp work feed <project> --ack <cursor>
  bp work brief <project> [--items <n>]
  bp work doctor <project>

Every command takes --json. A task is verified only by "bp work task verify"
from someone other than its submitter; a message receipt never verifies work.
Paths are resolved against the current directory and must lie inside one of
the project's roots. Full guide: docs/work.md.`

// workArgs is a small flag scanner: flags anywhere, repeated flags
// collected, positionals in order, everything after "--" kept apart.
type workArgs struct {
	pos   []string
	flags map[string][]string
	bools map[string]bool
	tail  []string
}

var workBoolFlags = map[string]bool{"--json": true, "--history": true, "--pass": true, "--fail": true,
	"--run": true, "--negative": true, "--summary": true, "--subscribe": true, "--next": true,
	"--allow-unproven-reviewers": true}

func parseWorkArgs(args []string) (workArgs, error) {
	w := workArgs{flags: map[string][]string{}, bools: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			w.tail = append([]string(nil), args[i+1:]...)
			return w, nil
		case workBoolFlags[arg]:
			w.bools[arg] = true
		case strings.HasPrefix(arg, "--") && len(arg) > 2:
			if i+1 >= len(args) {
				return w, fmt.Errorf("%s needs a value", arg)
			}
			i++
			w.flags[arg] = append(w.flags[arg], args[i])
		default:
			w.pos = append(w.pos, arg)
		}
	}
	return w, nil
}

func (w workArgs) one(name string) string {
	if v := w.flags[name]; len(v) > 0 {
		return v[len(v)-1]
	}
	return ""
}

func (w workArgs) int(name string) (int, error) {
	v := w.one(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s needs a non-negative number", name)
	}
	return n, nil
}

func (w workArgs) duration(name string) (time.Duration, error) {
	v := w.one(name)
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s needs a positive duration like 15m", name)
	}
	return d, nil
}

func (w workArgs) list(name string) []string {
	var out []string
	for _, v := range w.flags[name] {
		out = append(out, splitList(v)...)
	}
	return out
}

// check refuses flags the command does not take, so a typo is an error.
func (w workArgs) check(allowed ...string) error {
	ok := map[string]bool{"--json": true}
	for _, a := range allowed {
		ok[a] = true
	}
	for f := range w.flags {
		if !ok[f] {
			return fmt.Errorf("unknown option %s (see bp work help)", f)
		}
	}
	for f := range w.bools {
		if !ok[f] {
			return fmt.Errorf("unknown option %s (see bp work help)", f)
		}
	}
	return nil
}

func (w workArgs) need(n int, usage string) error {
	if len(w.pos) < n {
		return fmt.Errorf("usage: %s", usage)
	}
	return nil
}

func absPaths(paths []string, revision string) ([]ledger.ArtifactRef, error) {
	refs := []ledger.ArtifactRef{}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ledger.ArtifactRef{Path: abs, Revision: revision})
	}
	return refs, nil
}

// workCommand is bp work ...
func (a *app) workCommand(args []string) error {
	if len(args) == 0 || args[0] == "help" {
		fmt.Fprintln(a.out, workUsage)
		return nil
	}
	group := args[0]
	w, err := parseWorkArgs(args[1:])
	if err != nil {
		return err
	}
	req, err := a.workRequest(group, &w)
	if err != nil {
		return err
	}
	core := a.apiCore()
	caller := api.Caller{Transport: "cli", Name: "reader"}
	if api.WorkOps[req.Op] || req.Op == "feed.next" {
		if caller, err = a.callerForWrite("work " + strings.ReplaceAll(req.Op, ".", " ")); err != nil {
			return err
		}
	}
	value, err := core.Work(caller, req)
	if err != nil {
		return err
	}
	if w.bools["--json"] {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(a.out, string(data))
		return nil
	}
	a.printWork(req, value)
	return nil
}

// workRequest turns command-line words into a WorkRequest.
func (a *app) workRequest(group string, w *workArgs) (api.WorkRequest, error) {
	var req api.WorkRequest
	switch group {
	case "projects":
		return api.WorkRequest{Op: "project.list"}, w.check()
	case "brief":
		if err := w.need(1, "bp work brief <project> [--items <n>]"); err != nil {
			return req, err
		}
		items, err := w.int("--items")
		return api.WorkRequest{Op: "brief", Project: w.pos[0], Items: items}, firstErr(err, w.check("--items"))
	case "doctor":
		if err := w.need(1, "bp work doctor <project>"); err != nil {
			return req, err
		}
		return api.WorkRequest{Op: "doctor", Project: w.pos[0]}, w.check()
	case "feed":
		return a.feedRequest(w)
	case "project", "task", "finding":
	default:
		return req, fmt.Errorf("unknown work command %q\n%s", group, workUsage)
	}
	if len(w.pos) < 2 {
		return req, fmt.Errorf("usage: bp work %s <action> <project> ... (see bp work help)", group)
	}
	action, project := w.pos[0], w.pos[1]
	rest := w.pos[2:]
	req.Project = project
	idAndText := func(usage string) (string, string, error) {
		if len(rest) < 1 {
			return "", "", fmt.Errorf("usage: %s", usage)
		}
		return rest[0], strings.Join(rest[1:], " "), nil
	}
	fence := func() (int64, error) {
		v := w.one("--fence")
		if v == "" {
			return 0, fmt.Errorf("--fence is required: pass the fence your claim returned")
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("--fence needs a positive number")
		}
		return n, nil
	}
	switch group + " " + action {
	case "project create":
		req.Op, req.Goal, req.AllowUnprovenReviewers = "project.create", w.one("--goal"), w.bools["--allow-unproven-reviewers"]
		for _, r := range w.flags["--root"] {
			abs, err := filepath.Abs(r)
			if err != nil {
				return req, err
			}
			req.Roots = append(req.Roots, abs)
		}
		return req, w.check("--goal", "--root", "--allow-unproven-reviewers")
	case "project show":
		req.Op = "project.get"
		return req, w.check()
	case "project pause", "project resume", "project stop":
		req.Op, req.Reason = "project."+action, w.one("--reason")
		return req, w.check("--reason")
	case "project root":
		if len(rest) != 1 {
			return req, fmt.Errorf("usage: bp work project root <project> <dir>")
		}
		abs, err := filepath.Abs(rest[0])
		req.Op, req.Root = "project.root", abs
		return req, firstErr(err, w.check())

	case "task add":
		attempts, err := w.int("--attempts")
		req.Op, req.Title, req.Spec, req.Deps = "task.create", strings.Join(rest, " "), w.one("--spec"), w.list("--dep")
		req.Verifier, req.MaxAttempts = w.one("--verifier"), attempts
		return req, firstErr(err, w.check("--spec", "--dep", "--verifier", "--attempts"))
	case "task list":
		req.Op, req.State, req.Owner = "task.list", w.one("--state"), w.one("--owner")
		return req, w.check("--state", "--owner")
	case "task show":
		id, _, err := idAndText("bp work task show <project> <id> [--history]")
		req.Op, req.ID = "task.get", id
		if w.bools["--history"] {
			req.Op = "task.history"
		}
		return req, firstErr(err, w.check("--history"))
	case "task dep":
		if len(rest) != 2 {
			return req, fmt.Errorf("usage: bp work task dep <project> <id> <depends-on-id>")
		}
		req.Op, req.ID, req.Dep = "task.dep", rest[0], rest[1]
		return req, w.check()
	case "task claim":
		id, _, err := idAndText("bp work task claim <project> <id> [--lease <duration>]")
		lease, lerr := w.duration("--lease")
		req.Op, req.ID, req.LeaseSeconds = "task.claim", id, int(lease/time.Second)
		return req, firstErr(err, lerr, w.check("--lease"))
	case "task renew":
		id, _, err := idAndText("bp work task renew <project> <id> --fence <n> [--lease <duration>]")
		f, ferr := fence()
		lease, lerr := w.duration("--lease")
		req.Op, req.ID, req.Fence, req.LeaseSeconds = "task.renew", id, f, int(lease/time.Second)
		return req, firstErr(err, ferr, lerr, w.check("--fence", "--lease"))
	case "task checkpoint":
		id, note, err := idAndText("bp work task checkpoint <project> <id> --fence <n> <note...>")
		f, ferr := fence()
		progress, perr := w.int("--progress")
		req.Op, req.ID, req.Fence, req.Note, req.Progress = "task.checkpoint", id, f, note, progress
		return req, firstErr(err, ferr, perr, w.check("--fence", "--progress"))
	case "task block", "task unblock", "task release":
		id, text, err := idAndText("bp work task " + action + " <project> <id> --fence <n> ...")
		f, ferr := fence()
		req.Op, req.ID, req.Fence = "task."+action, id, f
		if action == "block" {
			req.Reason = text
		} else {
			req.Note = text
		}
		return req, firstErr(err, ferr, w.check("--fence"))
	case "task recover", "task cancel", "task reopen":
		id, reason, err := idAndText("bp work task " + action + " <project> <id> <reason...>")
		req.Op, req.ID, req.Reason = "task."+action, id, reason
		return req, firstErr(err, w.check())
	case "task submit":
		id, _, err := idAndText("bp work task submit <project> <id> --fence <n> --artifact <path>...")
		f, ferr := fence()
		refs, aerr := absPaths(w.flags["--artifact"], w.one("--revision"))
		req.Op, req.ID, req.Fence, req.Artifacts, req.Note = "task.submit", id, f, refs, w.one("--note")
		return req, firstErr(err, ferr, aerr, w.check("--fence", "--artifact", "--revision", "--note"))
	case "task verify":
		return a.verifyRequest(req, w, rest)

	case "finding add":
		refs, err := absPaths(w.flags["--source"], w.one("--revision"))
		req.Op, req.Assertion, req.Sources, req.Task = "finding.add", strings.Join(rest, " "), refs, w.one("--task")
		req.Conditions = w.one("--conditions")
		if w.bools["--negative"] {
			req.Kind = ledger.KindNegative
		}
		return req, firstErr(err, w.check("--source", "--revision", "--task", "--conditions", "--negative"))
	case "finding list":
		req.Op, req.Kind, req.Status = "finding.list", w.one("--kind"), w.one("--status")
		return req, w.check("--kind", "--status")
	case "finding show":
		id, _, err := idAndText("bp work finding show <project> <id> [--history]")
		req.Op, req.ID = "finding.get", id
		if w.bools["--history"] {
			req.Op = "finding.history"
		}
		return req, firstErr(err, w.check("--history"))
	case "finding verify", "finding refute":
		id, evidence, err := idAndText("bp work finding " + action + " <project> <id> <evidence...>")
		req.Op, req.ID, req.EvidenceText = "finding."+action, id, evidence
		return req, firstErr(err, w.check())
	case "finding check":
		id, _, err := idAndText("bp work finding check <project> <id>")
		req.Op, req.ID = "finding.check", id
		return req, firstErr(err, w.check())
	case "finding revise":
		id, _, err := idAndText("bp work finding revise <project> <id> ...")
		req.Op, req.ID, req.Assertion, req.Conditions = "finding.revise", id, w.one("--assertion"), w.one("--conditions")
		if len(w.flags["--source"]) > 0 {
			refs, aerr := absPaths(w.flags["--source"], w.one("--revision"))
			req.Sources, err = refs, firstErr(err, aerr)
		}
		return req, firstErr(err, w.check("--assertion", "--conditions", "--source", "--revision"))
	}
	return req, fmt.Errorf("unknown work command %q %q\n%s", group, action, workUsage)
}

func (a *app) verifyRequest(req api.WorkRequest, w *workArgs, rest []string) (api.WorkRequest, error) {
	if len(rest) != 1 {
		return req, fmt.Errorf("usage: bp work task verify <project> <id> (--pass|--fail) --checks <text> [--run -- <argv...>]")
	}
	req.Op, req.ID, req.Checks = "task.verify", rest[0], w.one("--checks")
	submission, err := w.int("--submission")
	if err != nil {
		return req, err
	}
	req.Submission = submission
	if req.Evidence, err = absPaths(w.flags["--evidence"], w.one("--revision")); err != nil {
		return req, err
	}
	if err := w.check("--pass", "--fail", "--checks", "--evidence", "--revision", "--submission", "--run", "--timeout"); err != nil {
		return req, err
	}
	switch {
	case w.bools["--pass"] && w.bools["--fail"]:
		return req, fmt.Errorf("choose one of --pass or --fail")
	case w.bools["--pass"]:
		req.Result = "pass"
	case w.bools["--fail"]:
		req.Result = "fail"
	}
	if !w.bools["--run"] {
		if len(w.tail) > 0 {
			return req, fmt.Errorf("a command after -- needs --run")
		}
		if req.Result == "" {
			return req, fmt.Errorf("pass --pass or --fail (or --run a validator)")
		}
		return req, nil
	}
	if len(w.tail) == 0 {
		return req, fmt.Errorf("--run needs a validator command after --")
	}
	timeout, err := w.duration("--timeout")
	if err != nil {
		return req, err
	}
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	dir, err := os.Getwd()
	if err != nil {
		return req, err
	}
	// Pin the submission the validator judges. Verify then refuses if a
	// newer submission superseded it while the validator ran, and a pass
	// re-hashes the artifacts under the ledger lock.
	view, err := a.apiCore().Ledger().GetTask(req.Project, req.ID)
	if err != nil {
		return req, err
	}
	if view.State != ledger.TaskSubmitted || len(view.Submissions) == 0 {
		return req, fmt.Errorf("%s is %s; only a submitted task can be verified", req.ID, view.State)
	}
	latest := view.Submissions[len(view.Submissions)-1].N
	if req.Submission == 0 {
		req.Submission = latest
	}
	if req.Submission != latest {
		return req, fmt.Errorf("submission %d of %s was superseded by %d", req.Submission, req.ID, latest)
	}
	if len(view.ArtifactDrift) > 0 {
		return req, fmt.Errorf("artifacts of %s changed since submission %d; the owner must resubmit before validation", req.ID, latest)
	}
	// The validator runs here, as the caller, with the workflow runner: argv
	// without a shell, stdin closed, killed with its process group on
	// timeout. Exit 0 backs a pass, exit 1 a fail; anything else records
	// nothing, because an unknown outcome is not a result.
	exit, stdout, stderr, err := workflow.RunCommand(a.ctx, dir, w.tail, timeout)
	if err != nil {
		return req, fmt.Errorf("validator did not produce a result: %v", err)
	}
	output := stdout
	if stderr != "" {
		output += "\n[stderr]\n" + stderr
	}
	sum := sha256.Sum256([]byte(output))
	req.Validator = &ledger.ValidatorRun{Command: w.tail, Exit: exit, Output: clipOutput(output), OutputDigest: "sha256:" + hex.EncodeToString(sum[:])}
	switch {
	case exit != 0 && exit != 1:
		return req, fmt.Errorf("validator exited %d (only 0 = pass and 1 = fail are results); nothing recorded:\n%s", exit, clipOutput(output))
	case req.Result == "":
		req.Result = map[int]string{0: "pass", 1: "fail"}[exit]
	case req.Result == "pass" && exit != 0:
		return req, fmt.Errorf("validator exited %d; it cannot back --pass:\n%s", exit, clipOutput(output))
	}
	if req.Checks == "" {
		req.Checks = "validator: " + strings.Join(w.tail, " ")
	}
	return req, nil
}

func clipOutput(s string) string {
	if len(s) > 2048 {
		return s[:2048] + "…"
	}
	return s
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *app) feedRequest(w *workArgs) (api.WorkRequest, error) {
	if err := w.need(1, "bp work feed <project> [--after <cursor>] ..."); err != nil {
		return api.WorkRequest{}, err
	}
	limit, err := w.int("--limit")
	if err != nil {
		return api.WorkRequest{}, err
	}
	req := api.WorkRequest{Op: "feed", Project: w.pos[0], After: w.one("--after"), Limit: limit, Summary: w.bools["--summary"],
		Filter: ledger.FeedFilter{Kinds: w.list("--kind"), IDs: w.list("--id"), Involving: w.one("--involving")}}
	modes := 0
	for _, m := range []bool{w.bools["--subscribe"], w.bools["--next"], w.one("--ack") != ""} {
		if m {
			modes++
		}
	}
	if modes > 1 {
		return req, fmt.Errorf("choose one of --subscribe, --next or --ack")
	}
	switch {
	case w.bools["--subscribe"]:
		req.Op, req.From = "feed.subscribe", w.one("--from")
		return req, w.check("--subscribe", "--from", "--kind", "--id", "--involving")
	case w.bools["--next"]:
		req.Op = "feed.next"
		return req, w.check("--next", "--limit", "--summary")
	case w.one("--ack") != "":
		req.Op, req.After = "feed.ack", w.one("--ack")
		return req, w.check("--ack")
	}
	return req, w.check("--after", "--limit", "--kind", "--id", "--involving", "--summary")
}

// printWork renders a result for a person or an agent reading a terminal.
func (a *app) printWork(req api.WorkRequest, value any) {
	switch v := value.(type) {
	case ledger.Brief:
		fmt.Fprint(a.out, v.Text())
	case ledger.Task:
		a.printTask(v)
	case ledger.TaskView:
		a.printTask(v.Task)
		if v.LeaseExpired {
			fmt.Fprintln(a.out, "  lease expired: recoverable with bp work task recover")
		}
		for _, d := range v.ArtifactDrift {
			fmt.Fprintf(a.out, "  CHANGED since submission: %s\n", strings.TrimSpace(d.Path+" "+d.Got+" "+d.Problem))
		}
		for _, b := range v.Blockers {
			fmt.Fprintf(a.out, "  blocked by %s\n", b)
		}
	case ledger.SubmitResult:
		a.printTask(v.Task)
		if v.Repeated {
			fmt.Fprintln(a.err, "identical submission already awaits verification; nothing changed")
		}
	case ledger.Finding:
		a.printFinding(v)
	case ledger.FindingView:
		a.printFindingView(v)
	case ledger.CheckResult:
		a.printFinding(v.Finding)
		if v.Fresh {
			fmt.Fprintln(a.out, "sources unchanged")
		} else {
			for _, d := range v.Drift {
				fmt.Fprintf(a.out, "changed: %s\n", strings.TrimSpace(d.Path+" "+d.Got+" "+d.Problem))
			}
		}
	case ledger.Project:
		fmt.Fprintf(a.out, "%s [%s] lead %s — %s\n", v.Name, v.State, v.Lead, v.Goal)
		for _, r := range v.Roots {
			fmt.Fprintf(a.out, "  root %s\n", r)
		}
	case ledger.FeedPage:
		for _, ev := range v.Events {
			line := fmt.Sprintf("%d\t%s %s %s\tby %s", ev.Seq, ev.Kind, ev.ID, ev.Op, ev.Actor)
			if ev.State != "" {
				line += "\t[" + ev.State + "]"
			}
			if ev.Owner != "" {
				line += " owner " + ev.Owner
			}
			if ev.Note != "" {
				line += "\t" + ev.Note
			}
			fmt.Fprintln(a.out, line)
		}
		more := ""
		if v.More {
			more = " (more: call again with --after " + v.Next + ")"
		}
		fmt.Fprintf(a.err, "next %s · head %s%s\n", v.Next, v.Head, more)
	case ledger.Subscription:
		fmt.Fprintf(a.out, "subscribed at %s\n", v.Cursor)
	case map[string]any:
		switch {
		case v["tasks"] != nil:
			for _, t := range v["tasks"].([]ledger.TaskView) {
				a.printTask(t.Task)
			}
		case v["findings"] != nil:
			for _, f := range v["findings"].([]ledger.FindingView) {
				a.printFindingView(f)
			}
		case v["issues"] != nil:
			issues := v["issues"].([]ledger.Issue)
			if len(issues) == 0 {
				fmt.Fprintln(a.out, "no issues")
			}
			for _, i := range issues {
				fmt.Fprintf(a.out, "%s\t%s\t%s\n", i.Kind, i.ID, i.Detail)
			}
		case v["projects"] != nil:
			for _, p := range v["projects"].([]string) {
				fmt.Fprintln(a.out, p)
			}
		case v["events"] != nil:
			for _, ev := range v["events"].([]ledger.Event) {
				fmt.Fprintf(a.out, "%d\t%s\t%s v%d\tby %s\t[%s]\t%s\n", ev.Seq, ev.At, ev.Op, ev.Version, ev.Actor, ev.State, ev.Note)
			}
		}
	default:
		data, _ := json.MarshalIndent(value, "", "  ")
		fmt.Fprintln(a.out, string(data))
	}
}

func (a *app) printTask(t ledger.Task) {
	line := fmt.Sprintf("%s [%s] %s", t.ID, t.State, t.Title)
	if t.Owner != "" {
		line += fmt.Sprintf(" — owner %s fence %d", t.Owner, t.Fence)
		if t.LeaseUntil != "" {
			line += " lease until " + t.LeaseUntil
		}
	}
	if len(t.Deps) > 0 {
		line += " — deps " + strings.Join(t.Deps, ",")
	}
	fmt.Fprintf(a.out, "%s (v%d)\n", line, t.Version)
	if n := len(t.Submissions); n > 0 && (t.State == ledger.TaskSubmitted || t.State == ledger.TaskVerified) {
		s := t.Submissions[n-1]
		for _, art := range s.Artifacts {
			fmt.Fprintf(a.out, "  submission %d: %s %s\n", s.N, art.Digest, art.Path)
		}
	}
	if n := len(t.Verifications); n > 0 {
		v := t.Verifications[n-1]
		fmt.Fprintf(a.out, "  verification of submission %d by %s: %s — %s\n", v.Submission, v.By, v.Result, v.Checks)
	}
}

func (a *app) printFindingView(v ledger.FindingView) {
	a.printFinding(v.Finding)
	if v.Effective != v.Status {
		fmt.Fprintf(a.out, "  EFFECTIVE STATUS %s (stored %s): sources changed\n", v.Effective, v.Status)
		for _, d := range v.Drift {
			fmt.Fprintf(a.out, "    %s\n", strings.TrimSpace(d.Path+" "+d.Got+" "+d.Problem))
		}
	}
}

func (a *app) printFinding(f ledger.Finding) {
	line := fmt.Sprintf("%s [%s] %s — by %s (rev %d, v%d)", f.ID, f.Status, f.Assertion, f.Author, f.Revision, f.Version)
	if f.Kind == ledger.KindNegative {
		line = "negative " + line + " — applies when: " + f.Conditions
	}
	fmt.Fprintln(a.out, line)
	for _, s := range f.Sources {
		fmt.Fprintf(a.out, "  source %s %s\n", s.Digest, s.Path)
	}
	if f.StaleReason != "" {
		fmt.Fprintf(a.out, "  stale: %s\n", f.StaleReason)
	}
}
