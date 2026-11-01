package api

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"blueprint/internal/audit"
	"blueprint/internal/ledger"
)

// The work ledger (internal/ledger) is reached through one request shape,
// WorkRequest, shared by the CLI, MCP and REST, so every transport has
// exactly the same operations and checks. Core adds what the ledger does
// not know: who the caller is, the remote boundary and the audit log.
//
// Identity vs authorization: the caller's label (Caller.Label, proven by
// a pane or marked by its transport) is the identity the ledger records.
// What that identity may do is decided by ledger records: the project
// lead administers, the task owner with the current fence writes its
// task, and verification must come from a different identity. Remote
// (gateway) callers cannot reach the ledger at all: it names local files.

// WorkRequest is one ledger operation. Op selects it; the other fields are
// the operation's arguments (see WorkOps).
type WorkRequest struct {
	Op      string `json:"op"`
	Project string `json:"project,omitempty"`
	ID      string `json:"id,omitempty"`

	// project
	Goal string `json:"goal,omitempty"`
	// AllowUnprovenReviewers (project.create) lets self-declared labels
	// verify; off by default.
	AllowUnprovenReviewers bool     `json:"allowUnprovenReviewers,omitempty"`
	Roots                  []string `json:"roots,omitempty"`
	Root                   string   `json:"root,omitempty"`
	Reason                 string   `json:"reason,omitempty"`

	// task
	Title        string   `json:"title,omitempty"`
	Spec         string   `json:"spec,omitempty"`
	Deps         []string `json:"deps,omitempty"`
	Dep          string   `json:"dep,omitempty"`
	Verifier     string   `json:"verifier,omitempty"`
	MaxAttempts  int      `json:"maxAttempts,omitempty"`
	LeaseSeconds int      `json:"leaseSeconds,omitempty"`
	Fence        int64    `json:"fence,omitempty"`
	Version      int      `json:"version,omitempty"`
	Note         string   `json:"note,omitempty"`
	Progress     int      `json:"progress,omitempty"`
	State        string   `json:"state,omitempty"`
	Owner        string   `json:"owner,omitempty"`

	Artifacts  []ledger.ArtifactRef `json:"artifacts,omitempty"`
	Result     string               `json:"result,omitempty"`
	Checks     string               `json:"checks,omitempty"`
	Evidence   []ledger.ArtifactRef `json:"evidence,omitempty"`
	Validator  *ledger.ValidatorRun `json:"validator,omitempty"`
	Submission int                  `json:"submission,omitempty"`

	// finding
	Kind         string               `json:"kind,omitempty"`
	Assertion    string               `json:"assertion,omitempty"`
	Conditions   string               `json:"conditions,omitempty"`
	Task         string               `json:"task,omitempty"`
	Sources      []ledger.ArtifactRef `json:"sources,omitempty"`
	EvidenceText string               `json:"evidenceText,omitempty"`
	Status       string               `json:"status,omitempty"`

	// feed and brief
	After   string            `json:"after,omitempty"`
	Limit   int               `json:"limit,omitempty"`
	Filter  ledger.FeedFilter `json:"filter,omitempty"`
	Summary bool              `json:"summary,omitempty"`
	From    string            `json:"from,omitempty"`
	Items   int               `json:"items,omitempty"`
}

// WorkOps lists every operation and whether it writes.
var WorkOps = map[string]bool{
	"project.create": true, "project.list": false, "project.get": false,
	"project.pause": true, "project.resume": true, "project.stop": true, "project.root": true,
	"task.create": true, "task.get": false, "task.list": false, "task.history": false, "task.dep": true,
	"task.claim": true, "task.renew": true, "task.checkpoint": true, "task.block": true, "task.unblock": true,
	"task.release": true, "task.recover": true, "task.submit": true, "task.verify": true,
	"task.cancel": true, "task.reopen": true,
	"finding.add": true, "finding.get": false, "finding.list": false, "finding.history": false,
	"finding.revise": true, "finding.verify": true, "finding.refute": true, "finding.check": true,
	"feed": false, "feed.subscribe": true, "feed.next": false, "feed.ack": true,
	"brief": false, "doctor": false,
}

// Ledger returns the work ledger under this core's state directory.
func (c *Core) Ledger() *ledger.Store {
	return &ledger.Store{Dir: filepath.Join(c.dir(), "ledger"), Now: c.now}
}

// workError keeps the ledger's message and makes it match both the
// ledger error and the API class transports map to a status.
type workError struct {
	class error
	err   error
}

func (e workError) Error() string   { return e.err.Error() }
func (e workError) Unwrap() []error { return []error{e.class, e.err} }

func workErr(err error) error {
	if err == nil {
		return nil
	}
	class := error(nil)
	switch {
	case errors.Is(err, ledger.ErrNotFound):
		class = ErrNotFound
	case errors.Is(err, ledger.ErrInvalid), errors.Is(err, ledger.ErrCursorInvalid):
		class = ErrInvalid
	case errors.Is(err, ledger.ErrForbidden):
		class = ErrForbidden
	case errors.Is(err, ledger.ErrConflict), errors.Is(err, ledger.ErrStaleOwner), errors.Is(err, ledger.ErrLeaseExpired),
		errors.Is(err, ledger.ErrCycle), errors.Is(err, ledger.ErrDependency), errors.Is(err, ledger.ErrStaleEvidence),
		errors.Is(err, ledger.ErrCursorExpired), errors.Is(err, ledger.ErrHalted):
		class = errWorkConflict
	default:
		return err
	}
	return workError{class: class, err: err}
}

// errWorkConflict is a ledger state conflict. It matches ErrConflict for
// status mapping but is told apart in MCP error text (the board's "read
// the key again" hint does not apply).
var errWorkConflict = fmt.Errorf("%w (work)", ErrConflict)

// Work runs one ledger operation for caller.
func (c *Core) Work(caller Caller, req WorkRequest) (any, error) {
	writes, known := WorkOps[req.Op]
	if !known {
		return nil, invalid("unknown work operation %q", req.Op)
	}
	if caller.Remote {
		c.auditResult(caller, "work."+req.Op, req.Project, req.ID, fmt.Errorf("%w: remote", ErrForbidden))
		return nil, fmt.Errorf("%w: the work ledger is local only; remote clients cannot reach it", ErrForbidden)
	}
	if writes || req.Op == "feed.next" {
		if err := ValidateCaller(caller); err != nil {
			return nil, err
		}
	}
	actor := ledger.Actor{Label: caller.Label(), Verified: caller.Verified && !caller.Remote}
	if req.Validator != nil && caller.Transport != "cli" {
		// Only bp work task verify --run executes a validator; over MCP or
		// HTTP the record is the verifier's own report.
		v := *req.Validator
		v.SelfReported = true
		req.Validator = &v
	}
	value, err := c.work(actor, req)
	err = workErr(err)
	if writes {
		target := req.Project
		if target == "" {
			target = "-"
		}
		ev := audit.Event{Kind: "api.work." + req.Op, Actor: caller.Label(), Target: target, Reason: req.ID,
			Fields: map[string]string{"transport": caller.Transport}}
		if err != nil {
			ev.Kind += ".refused"
			ev.Fields["error"] = err.Error()
		}
		c.audit(ev)
	}
	return value, err
}

func (c *Core) work(actor ledger.Actor, req WorkRequest) (any, error) {
	l := c.Ledger()
	p, id := req.Project, req.ID
	proof := ledger.OwnerProof{Fence: req.Fence, Version: req.Version}
	lease := time.Duration(req.LeaseSeconds) * time.Second
	if req.Op != "project.list" && p == "" {
		return nil, invalid("project is required")
	}
	needID := func() error {
		if id == "" {
			return invalid("%s needs an id", req.Op)
		}
		return nil
	}
	switch req.Op {
	case "project.create":
		return l.CreateProject(actor, ledger.ProjectSpec{Name: p, Goal: req.Goal, Roots: req.Roots, AllowUnprovenReviewers: req.AllowUnprovenReviewers})
	case "project.list":
		names, err := l.Projects()
		return map[string]any{"projects": names}, err
	case "project.get":
		return l.GetProject(p)
	case "project.pause":
		return l.SetProjectState(actor, p, ledger.ProjectPaused, req.Reason)
	case "project.resume":
		return l.SetProjectState(actor, p, ledger.ProjectActive, req.Reason)
	case "project.stop":
		return l.SetProjectState(actor, p, ledger.ProjectStopped, req.Reason)
	case "project.root":
		return l.AddRoot(actor, p, req.Root)
	case "task.create":
		return l.CreateTask(actor, p, ledger.TaskSpec{Title: req.Title, Spec: req.Spec, Deps: req.Deps, Verifier: req.Verifier, MaxAttempts: req.MaxAttempts})
	case "task.list":
		tasks, err := l.Tasks(p, req.State, req.Owner)
		return map[string]any{"tasks": tasks}, err
	case "finding.add":
		return l.AddFinding(actor, p, ledger.FindingSpec{Kind: req.Kind, Assertion: req.Assertion, Conditions: req.Conditions, Task: req.Task, Sources: req.Sources})
	case "finding.list":
		findings, err := l.Findings(p, req.Kind, req.Status)
		return map[string]any{"findings": findings}, err
	case "feed":
		return l.Feed(p, ledger.FeedQuery{After: req.After, Limit: req.Limit, Filter: req.Filter, Summary: req.Summary})
	case "feed.subscribe":
		return l.Subscribe(actor, p, req.Filter, req.From)
	case "feed.next":
		return l.FeedSubscribed(actor, p, req.Limit, req.Summary)
	case "feed.ack":
		return l.Ack(actor, p, req.After)
	case "brief":
		return l.Brief(p, req.Items)
	case "doctor":
		issues, err := l.Doctor(p)
		return map[string]any{"issues": issues}, err
	}
	if err := needID(); err != nil {
		return nil, err
	}
	switch req.Op {
	case "task.get":
		return l.GetTask(p, id)
	case "task.history":
		events, err := l.TaskHistory(p, id)
		return map[string]any{"events": events}, err
	case "task.dep":
		return l.AddDep(actor, p, id, req.Dep)
	case "task.claim":
		return l.Claim(actor, p, id, lease)
	case "task.renew":
		return l.Renew(actor, p, id, proof, lease)
	case "task.checkpoint":
		return l.Checkpoint(actor, p, id, proof, req.Note, req.Progress)
	case "task.block":
		return l.Block(actor, p, id, proof, req.Reason)
	case "task.unblock":
		return l.Unblock(actor, p, id, proof)
	case "task.release":
		return l.Release(actor, p, id, proof, req.Note)
	case "task.recover":
		return l.Recover(actor, p, id, req.Reason)
	case "task.submit":
		return l.Submit(actor, p, id, proof, req.Artifacts, req.Note)
	case "task.verify":
		return l.Verify(actor, p, id, ledger.VerifyInput{Submission: req.Submission, Result: req.Result, Checks: req.Checks, Evidence: req.Evidence, Validator: req.Validator})
	case "task.cancel":
		return l.Cancel(actor, p, id, req.Reason)
	case "task.reopen":
		return l.Reopen(actor, p, id, req.Reason)
	case "finding.get":
		return l.GetFinding(p, id)
	case "finding.history":
		events, err := l.FindingHistory(p, id)
		return map[string]any{"events": events}, err
	case "finding.revise":
		return l.ReviseFinding(actor, p, id, ledger.FindingSpec{Kind: req.Kind, Assertion: req.Assertion, Conditions: req.Conditions, Sources: req.Sources}, req.Version)
	case "finding.verify":
		return l.JudgeFinding(actor, p, id, ledger.FindingVerified, req.EvidenceText, req.Version)
	case "finding.refute":
		return l.JudgeFinding(actor, p, id, ledger.FindingRefuted, req.EvidenceText, req.Version)
	case "finding.check":
		return l.CheckFinding(actor, p, id)
	}
	return nil, invalid("unknown work operation %q", req.Op)
}
