package ledger

import (
	"fmt"
	"strings"
	"time"
)

// Task states.
//
//	pending -> claimed -> working -> submitted -> verified
//	              |          |           |
//	              |       blocked        +-> working (rework, attempt used)
//	              |                      +-> failed  (attempt budget spent)
//	              +-> pending (release, or recover after the lease expired)
//
// cancelled and failed are terminal until the lead reopens the task.
const (
	TaskPending   = "pending"
	TaskClaimed   = "claimed"
	TaskWorking   = "working"
	TaskBlocked   = "blocked"
	TaskSubmitted = "submitted"
	TaskVerified  = "verified"
	TaskFailed    = "failed"
	TaskCancelled = "cancelled"
)

const (
	DefaultLease       = 15 * time.Minute
	MaxLease           = 24 * time.Hour
	DefaultMaxAttempts = 3
	maxListed          = 20 // checkpoints/submissions/verifications kept inline
	maxDeps            = 32
	maxTasks           = 5000
)

// Task is one unit of work with a single owner at a time.
type Task struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Spec      string   `json:"spec,omitempty"`
	CreatedBy string   `json:"createdBy"`
	State     string   `json:"state"`
	Deps      []string `json:"deps,omitempty"`
	// Owner holds the task while it is claimed, working, blocked or
	// submitted. Fence increases on every claim and recovery; an owner's
	// write must carry the current fence, so a replaced owner's late write
	// is refused even if it reuses the same name.
	Owner      string `json:"owner,omitempty"`
	Fence      int64  `json:"fence"`
	LeaseUntil string `json:"leaseUntil,omitempty"`
	// Verifier, when set, is the only actor that may verify.
	Verifier      string         `json:"verifier,omitempty"`
	MaxAttempts   int            `json:"maxAttempts"`
	Attempts      int            `json:"attempts"`
	BlockedReason string         `json:"blockedReason,omitempty"`
	Checkpoints   []Checkpoint   `json:"checkpoints,omitempty"`
	Submissions   []Submission   `json:"submissions,omitempty"`
	Verifications []Verification `json:"verifications,omitempty"`
	Version       int            `json:"version"`
	CreatedAt     string         `json:"createdAt"`
	UpdatedAt     string         `json:"updatedAt"`
}

// Checkpoint is an owner's progress note.
type Checkpoint struct {
	At       string `json:"at"`
	By       string `json:"by"`
	Note     string `json:"note"`
	Progress int    `json:"progress,omitempty"`
}

// Submission is an owner's claim that the work is done, with the exact
// artifacts it consists of.
type Submission struct {
	N         int        `json:"n"`
	By        string     `json:"by"`
	Fence     int64      `json:"fence"`
	Artifacts []Artifact `json:"artifacts"`
	Note      string     `json:"note,omitempty"`
	At        string     `json:"at"`
}

// Verification records who checked which submission, what they checked
// and the result. It is the only way a task becomes verified.
type Verification struct {
	Submission int    `json:"submission"`
	By         string `json:"by"`
	// ByVerified records whether bp proved the verifier's identity (a
	// pane it started) or the label was self-declared over a transport.
	ByVerified bool          `json:"byVerified"`
	Result     string        `json:"result"` // pass or fail
	Checks     string        `json:"checks"`
	Evidence   []Artifact    `json:"evidence,omitempty"`
	Validator  *ValidatorRun `json:"validator,omitempty"`
	At         string        `json:"at"`
}

// ValidatorRun is a command a verifier ran against the artifacts. The
// caller runs it (the ledger never executes anything); the record keeps
// the argv, exit code and a bounded excerpt of its output.
type ValidatorRun struct {
	Command      []string `json:"command"`
	Exit         int      `json:"exit"`
	Output       string   `json:"output,omitempty"`
	OutputDigest string   `json:"outputDigest,omitempty"`
	// SelfReported marks a record the verifier supplied over MCP or HTTP;
	// bp did not run that command. bp work task verify --run runs it.
	SelfReported bool `json:"selfReported,omitempty"`
}

// TaskSpec creates a task.
type TaskSpec struct {
	Title       string   `json:"title"`
	Spec        string   `json:"spec,omitempty"`
	Deps        []string `json:"deps,omitempty"`
	Verifier    string   `json:"verifier,omitempty"`
	MaxAttempts int      `json:"maxAttempts,omitempty"`
}

// OwnerProof is what an owner presents on every write: the fence it got
// from its claim and, optionally, the task version it last read.
type OwnerProof struct {
	Fence   int64 `json:"fence"`
	Version int   `json:"version,omitempty"`
}

func (s *Store) stamp() string { return s.now().Format(time.RFC3339Nano) }

func (s *Store) touchTask(t *Task) {
	t.Version++
	t.UpdatedAt = s.stamp()
}

func taskEvent(t *Task, op, note string) Event {
	return Event{Kind: "task", ID: t.ID, Op: op, Version: t.Version, Note: note, Task: t}
}

func (st *State) task(id string) (*Task, error) {
	t, ok := st.Tasks[id]
	if !ok {
		return nil, fmt.Errorf("%w: task %s in project %s", ErrNotFound, id, st.Project.Name)
	}
	return t, nil
}

func nextID(prefix string, n int, exists func(string) bool) string {
	for {
		n++
		id := fmt.Sprintf("%s%d", prefix, n)
		if !exists(id) {
			return id
		}
	}
}

// CreateTask adds a pending task. Dependencies must already exist.
func (s *Store) CreateTask(actor Actor, project string, spec TaskSpec) (Task, error) {
	if err := textField("title", spec.Title, 300, true); err != nil {
		return Task{}, err
	}
	if strings.Contains(spec.Title, "\n") {
		return Task{}, invalid("title must be one line")
	}
	if err := textField("spec", spec.Spec, 16<<10, false); err != nil {
		return Task{}, err
	}
	if len(spec.Deps) > maxDeps {
		return Task{}, invalid("at most %d dependencies", maxDeps)
	}
	if spec.MaxAttempts < 0 || spec.MaxAttempts > 100 {
		return Task{}, invalid("max attempts must be 1-100")
	}
	if spec.MaxAttempts == 0 {
		spec.MaxAttempts = DefaultMaxAttempts
	}
	if err := textField("verifier", spec.Verifier, 200, false); err != nil {
		return Task{}, err
	}
	var out Task
	err := s.mutate(project, actor, func(st *State, ch *change) error {
		if err := requireState(st.Project, ProjectActive); err != nil {
			return err
		}
		if len(st.Tasks) >= maxTasks {
			return invalid("project %s has %d tasks, the limit", project, maxTasks)
		}
		deps := []string{}
		seen := map[string]bool{}
		for _, d := range spec.Deps {
			if seen[d] {
				continue
			}
			seen[d] = true
			if _, err := st.task(d); err != nil {
				return err
			}
			deps = append(deps, d)
		}
		sortIDs(deps)
		id := nextID("t", len(st.Tasks), func(id string) bool { _, ok := st.Tasks[id]; return ok })
		now := s.stamp()
		t := &Task{ID: id, Title: spec.Title, Spec: spec.Spec, CreatedBy: actor.Label, State: TaskPending,
			Deps: deps, Verifier: spec.Verifier, MaxAttempts: spec.MaxAttempts, Version: 1, CreatedAt: now, UpdatedAt: now}
		st.Tasks[id] = t
		ch.add(taskEvent(t, "create", ""))
		out = *t
		return nil
	})
	return out, err
}

// AddDep makes task id depend on dep. It refuses a dependency that would
// close a cycle and names the cycle.
func (s *Store) AddDep(actor Actor, project, id, dep string) (Task, error) {
	var out Task
	err := s.mutate(project, actor, func(st *State, ch *change) error {
		t, err := st.task(id)
		if err != nil {
			return err
		}
		if _, err := st.task(dep); err != nil {
			return err
		}
		if actor.Label != t.CreatedBy && actor.Label != st.Project.Lead {
			return fmt.Errorf("%w: only %s (creator) or the lead changes dependencies of %s", ErrForbidden, t.CreatedBy, id)
		}
		if t.State != TaskPending {
			return fmt.Errorf("%w: %s is %s; dependencies change only while pending", ErrConflict, id, t.State)
		}
		for _, d := range t.Deps {
			if d == dep {
				out = *t
				return nil
			}
		}
		if len(t.Deps) >= maxDeps {
			return invalid("at most %d dependencies", maxDeps)
		}
		if path := st.depPath(dep, id); path != nil {
			return fmt.Errorf("%w: %s -> %s would close the cycle %s", ErrCycle, id, dep, strings.Join(append([]string{id}, path...), " -> "))
		}
		t.Deps = append(t.Deps, dep)
		sortIDs(t.Deps)
		s.touchTask(t)
		ch.add(taskEvent(t, "dep", dep))
		out = *t
		return nil
	})
	return out, err
}

// depPath returns a dependency path from -> ... -> to, or nil.
func (st *State) depPath(from, to string) []string {
	seen := map[string]bool{}
	var walk func(id string) []string
	walk = func(id string) []string {
		if id == to {
			return []string{id}
		}
		if seen[id] {
			return nil
		}
		seen[id] = true
		t := st.Tasks[id]
		if t == nil {
			return nil
		}
		for _, d := range t.Deps {
			if p := walk(d); p != nil {
				return append([]string{id}, p...)
			}
		}
		return nil
	}
	return walk(from)
}

// depStatus reports why a task's dependencies are not satisfied: failed
// lists dependencies that failed or were cancelled (the task cannot run
// until the lead reopens or replaces them), waiting lists unfinished ones,
// and stale lists verified ones whose verified artifacts changed since —
// their verification no longer describes what is on disk.
func (st *State) depStatus(t *Task) (failed, waiting, stale []string) {
	for _, d := range t.Deps {
		dep := st.Tasks[d]
		switch {
		case dep == nil:
			failed = append(failed, d+" (missing)")
		case dep.State == TaskFailed || dep.State == TaskCancelled:
			failed = append(failed, d+" ("+dep.State+")")
		case dep.State != TaskVerified:
			waiting = append(waiting, d+" ("+dep.State+")")
		default:
			if drift := verifiedDrift(st, dep); len(drift) > 0 {
				stale = append(stale, d+" (verified artifacts changed: "+driftText(drift)+")")
			}
		}
	}
	return failed, waiting, stale
}

// verifiedDrift re-hashes the artifacts of a verified task's verified
// submission.
func verifiedDrift(st *State, t *Task) []Drift {
	if t.State != TaskVerified || len(t.Submissions) == 0 {
		return nil
	}
	return checkArtifacts(st.Project.Roots, t.Submissions[len(t.Submissions)-1].Artifacts)
}

func leaseDuration(ttl time.Duration) (time.Duration, error) {
	if ttl == 0 {
		return DefaultLease, nil
	}
	if ttl < time.Second || ttl > MaxLease {
		return 0, invalid("lease must be between 1s and %s", MaxLease)
	}
	return ttl, nil
}

func (s *Store) leaseExpired(t *Task) bool {
	if t.LeaseUntil == "" {
		return false
	}
	until, err := time.Parse(time.RFC3339Nano, t.LeaseUntil)
	return err != nil || !s.now().Before(until)
}

// Claim takes a pending task for actor. Exactly one concurrent claimant
// wins: the check and the write happen under the project lock. The result
// carries the new fence the owner must present on later writes.
func (s *Store) Claim(actor Actor, project, id string, ttl time.Duration) (Task, error) {
	ttl, err := leaseDuration(ttl)
	if err != nil {
		return Task{}, err
	}
	var out Task
	err = s.mutate(project, actor, func(st *State, ch *change) error {
		if err := requireState(st.Project, ProjectActive); err != nil {
			return err
		}
		t, err := st.task(id)
		if err != nil {
			return err
		}
		if t.State != TaskPending {
			holder := ""
			if t.Owner != "" {
				holder = " by " + t.Owner
				if s.leaseExpired(t) {
					holder += " (lease expired; bp work task recover hands it back)"
				}
			}
			return fmt.Errorf("%w: %s is %s%s", ErrConflict, id, t.State, holder)
		}
		if failed, waiting, stale := st.depStatus(t); len(failed) > 0 {
			return fmt.Errorf("%w: %s depends on %s", ErrDependency, id, strings.Join(failed, ", "))
		} else if len(waiting) > 0 {
			return fmt.Errorf("%w: %s waits on %s", ErrDependency, id, strings.Join(waiting, ", "))
		} else if len(stale) > 0 {
			return fmt.Errorf("%w: %s depends on %s; the lead reopens the prerequisite or restores its artifacts", ErrStaleEvidence, id, strings.Join(stale, ", "))
		}
		t.State, t.Owner, t.BlockedReason = TaskClaimed, actor.Label, ""
		t.Fence++
		t.LeaseUntil = s.now().Add(ttl).Format(time.RFC3339Nano)
		s.touchTask(t)
		ch.add(taskEvent(t, "claim", ""))
		out = *t
		return nil
	})
	return out, err
}

// requireOwner checks an owner write: same owner, current fence, optional
// version, and a live lease unless allowExpired.
func (s *Store) requireOwner(t *Task, actor Actor, proof OwnerProof, allowExpired bool) error {
	if t.Owner == "" {
		return fmt.Errorf("%w: %s has no owner (it is %s)", ErrStaleOwner, t.ID, t.State)
	}
	if t.Owner != actor.Label {
		return fmt.Errorf("%w: %s is owned by %s, not %s", ErrStaleOwner, t.ID, t.Owner, actor.Label)
	}
	if proof.Fence != t.Fence {
		return fmt.Errorf("%w: fence %d is stale for %s (current %d); the task was recovered or reclaimed", ErrStaleOwner, proof.Fence, t.ID, t.Fence)
	}
	if proof.Version > 0 && proof.Version != t.Version {
		return fmt.Errorf("%w: %s is at version %d, not %d", ErrConflict, t.ID, t.Version, proof.Version)
	}
	if !allowExpired && t.State != TaskSubmitted && s.leaseExpired(t) {
		return fmt.Errorf("%w: lease on %s ended at %s; renew it (bp work task renew) before writing", ErrLeaseExpired, t.ID, t.LeaseUntil)
	}
	return nil
}

// Renew extends the owner's lease. An expired lease may be renewed while
// nobody has recovered the task: the fence proves continuity.
func (s *Store) Renew(actor Actor, project, id string, proof OwnerProof, ttl time.Duration) (Task, error) {
	ttl, err := leaseDuration(ttl)
	if err != nil {
		return Task{}, err
	}
	return s.ownerOp(actor, project, id, proof, true, "renew", func(st *State, t *Task) (string, error) {
		switch t.State {
		case TaskClaimed, TaskWorking, TaskBlocked:
		default:
			return "", fmt.Errorf("%w: %s is %s; only a held task has a lease", ErrConflict, id, t.State)
		}
		t.LeaseUntil = s.now().Add(ttl).Format(time.RFC3339Nano)
		return "", nil
	})
}

func (s *Store) ownerOp(actor Actor, project, id string, proof OwnerProof, allowExpired bool, op string, fn func(st *State, t *Task) (string, error)) (Task, error) {
	var out Task
	err := s.mutate(project, actor, func(st *State, ch *change) error {
		t, err := st.task(id)
		if err != nil {
			return err
		}
		if err := s.requireOwner(t, actor, proof, allowExpired); err != nil {
			return err
		}
		note, err := fn(st, t)
		if err != nil {
			return err
		}
		s.touchTask(t)
		ch.add(taskEvent(t, op, note))
		out = *t
		return nil
	})
	return out, err
}

// Checkpoint records progress; the first one moves claimed to working.
func (s *Store) Checkpoint(actor Actor, project, id string, proof OwnerProof, note string, progress int) (Task, error) {
	if err := textField("note", note, 4096, true); err != nil {
		return Task{}, err
	}
	if progress < 0 || progress > 100 {
		return Task{}, invalid("progress must be 0-100")
	}
	return s.ownerOp(actor, project, id, proof, false, "checkpoint", func(st *State, t *Task) (string, error) {
		switch t.State {
		case TaskClaimed:
			t.State = TaskWorking
		case TaskWorking, TaskBlocked:
		default:
			return "", fmt.Errorf("%w: %s is %s", ErrConflict, id, t.State)
		}
		t.Checkpoints = appendBounded(t.Checkpoints, Checkpoint{At: s.stamp(), By: actor.Label, Note: note, Progress: progress})
		return note, nil
	})
}

// Block marks a held task blocked with a reason; Unblock resumes it.
func (s *Store) Block(actor Actor, project, id string, proof OwnerProof, reason string) (Task, error) {
	if err := textField("reason", reason, 2048, true); err != nil {
		return Task{}, err
	}
	return s.ownerOp(actor, project, id, proof, false, "block", func(st *State, t *Task) (string, error) {
		if t.State != TaskClaimed && t.State != TaskWorking && t.State != TaskBlocked {
			return "", fmt.Errorf("%w: %s is %s", ErrConflict, id, t.State)
		}
		t.State, t.BlockedReason = TaskBlocked, reason
		return reason, nil
	})
}

func (s *Store) Unblock(actor Actor, project, id string, proof OwnerProof) (Task, error) {
	return s.ownerOp(actor, project, id, proof, false, "unblock", func(st *State, t *Task) (string, error) {
		if t.State != TaskBlocked {
			return "", fmt.Errorf("%w: %s is %s, not blocked", ErrConflict, id, t.State)
		}
		t.State, t.BlockedReason = TaskWorking, ""
		return "", nil
	})
}

// Release hands a held task back to pending.
func (s *Store) Release(actor Actor, project, id string, proof OwnerProof, note string) (Task, error) {
	if err := textField("note", note, 2048, false); err != nil {
		return Task{}, err
	}
	return s.ownerOp(actor, project, id, proof, true, "release", func(st *State, t *Task) (string, error) {
		if t.State != TaskClaimed && t.State != TaskWorking && t.State != TaskBlocked {
			return "", fmt.Errorf("%w: %s is %s", ErrConflict, id, t.State)
		}
		t.State, t.Owner, t.LeaseUntil, t.BlockedReason = TaskPending, "", "", ""
		return note, nil
	})
}

// Recover takes a task away from an owner whose lease expired and returns
// it to pending. The fence advances, so the previous owner's later writes
// are refused. The lead may also recover a live lease (an explicit,
// recorded takeover); nobody else can.
func (s *Store) Recover(actor Actor, project, id, reason string) (Task, error) {
	if err := textField("reason", reason, 2048, true); err != nil {
		return Task{}, err
	}
	var out Task
	err := s.mutate(project, actor, func(st *State, ch *change) error {
		t, err := st.task(id)
		if err != nil {
			return err
		}
		switch t.State {
		case TaskClaimed, TaskWorking, TaskBlocked:
		default:
			return fmt.Errorf("%w: %s is %s; only a held task can be recovered", ErrConflict, id, t.State)
		}
		if !s.leaseExpired(t) && actor.Label != st.Project.Lead {
			return fmt.Errorf("%w: %s's lease on %s runs until %s; only the lead may take over a live lease", ErrForbidden, t.Owner, id, t.LeaseUntil)
		}
		previous := t.Owner
		t.State, t.Owner, t.LeaseUntil, t.BlockedReason = TaskPending, "", "", ""
		t.Fence++
		s.touchTask(t)
		ch.add(taskEvent(t, "recover", fmt.Sprintf("from %s: %s", previous, reason)))
		out = *t
		return nil
	})
	return out, err
}

// SubmitResult is a submission's outcome. Repeated is true when an
// identical submission was already pending verification; nothing changed.
type SubmitResult struct {
	Task     Task `json:"task"`
	Repeated bool `json:"repeated,omitempty"`
}

// Submit records the owner's result: artifacts with their current digests.
// Submitting the same bytes again while awaiting verification is a no-op;
// different bytes supersede the earlier submission.
func (s *Store) Submit(actor Actor, project, id string, proof OwnerProof, refs []ArtifactRef, note string) (SubmitResult, error) {
	if len(refs) == 0 {
		return SubmitResult{}, invalid("a submission needs at least one artifact")
	}
	if err := textField("note", note, 4096, false); err != nil {
		return SubmitResult{}, err
	}
	var out SubmitResult
	err := s.mutate(project, actor, func(st *State, ch *change) error {
		if err := requireState(st.Project, ProjectActive, ProjectPaused); err != nil {
			return err
		}
		t, err := st.task(id)
		if err != nil {
			return err
		}
		if err := s.requireOwner(t, actor, proof, false); err != nil {
			return err
		}
		switch t.State {
		case TaskClaimed, TaskWorking, TaskSubmitted:
		default:
			return fmt.Errorf("%w: %s is %s; submit from claimed or working", ErrConflict, id, t.State)
		}
		artifacts, err := resolveArtifacts(st.Project.Roots, refs)
		if err != nil {
			return err
		}
		if t.State == TaskSubmitted && len(t.Submissions) > 0 && sameArtifacts(t.Submissions[len(t.Submissions)-1].Artifacts, artifacts) {
			out = SubmitResult{Task: *t, Repeated: true}
			return nil
		}
		n := 1
		if len(t.Submissions) > 0 {
			n = t.Submissions[len(t.Submissions)-1].N + 1
		}
		t.Submissions = appendBounded(t.Submissions, Submission{N: n, By: actor.Label, Fence: t.Fence, Artifacts: artifacts, Note: note, At: s.stamp()})
		t.State, t.LeaseUntil = TaskSubmitted, ""
		s.touchTask(t)
		ch.add(taskEvent(t, "submit", fmt.Sprintf("submission %d", n)))
		out = SubmitResult{Task: *t}
		return nil
	})
	return out, err
}

func sameArtifacts(a, b []Artifact) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Path != b[i].Path || a[i].Target != b[i].Target || a[i].Digest != b[i].Digest || a[i].Revision != b[i].Revision {
			return false
		}
	}
	return true
}

// VerifyInput is a verifier's report on the latest submission.
type VerifyInput struct {
	// Submission is the submission number checked; 0 means the latest.
	// Verifying a superseded submission is refused.
	Submission int           `json:"submission,omitempty"`
	Result     string        `json:"result"` // pass or fail
	Checks     string        `json:"checks"`
	Evidence   []ArtifactRef `json:"evidence,omitempty"`
	Validator  *ValidatorRun `json:"validator,omitempty"`
}

// Verify records an independent check of the latest submission. The
// verifier must not be the submitter or the owner, and must be the
// task's named verifier when one is set. A pass re-hashes the submitted
// artifacts first: bytes that changed since submission cannot pass. A
// fail uses one attempt; the task returns to its owner for rework, or
// fails when the budget is spent.
func (s *Store) Verify(actor Actor, project, id string, in VerifyInput) (Task, error) {
	if in.Result != "pass" && in.Result != "fail" {
		return Task{}, invalid("result must be pass or fail")
	}
	if err := textField("checks", in.Checks, 8192, true); err != nil {
		return Task{}, err
	}
	if in.Validator != nil {
		if len(in.Validator.Command) == 0 || len(in.Validator.Command) > 64 {
			return Task{}, invalid("validator command must have 1-64 arguments")
		}
		if err := textField("validator output", in.Validator.Output, 8192, false); err != nil {
			return Task{}, err
		}
		if in.Result == "pass" && in.Validator.Exit != 0 {
			return Task{}, invalid("a validator that exited %d cannot back a pass", in.Validator.Exit)
		}
		if in.Submission == 0 {
			return Task{}, invalid("a validator result must name the submission it checked")
		}
	}
	var out Task
	err := s.mutate(project, actor, func(st *State, ch *change) error {
		if err := requireState(st.Project, ProjectActive, ProjectPaused); err != nil {
			return err
		}
		t, err := st.task(id)
		if err != nil {
			return err
		}
		if t.State != TaskSubmitted || len(t.Submissions) == 0 {
			return fmt.Errorf("%w: %s is %s; only a submitted task can be verified", ErrConflict, id, t.State)
		}
		latest := t.Submissions[len(t.Submissions)-1]
		if in.Submission != 0 && in.Submission != latest.N {
			return fmt.Errorf("%w: submission %d of %s was superseded by %d", ErrConflict, in.Submission, id, latest.N)
		}
		if actor.Label == latest.By || actor.Label == t.Owner {
			return fmt.Errorf("%w: %s submitted or owns %s; verification must come from someone else", ErrForbidden, actor.Label, id)
		}
		if t.Verifier != "" && actor.Label != t.Verifier {
			return fmt.Errorf("%w: %s names %s as its verifier", ErrForbidden, id, t.Verifier)
		}
		if err := requireReviewer(st.Project, actor, "verifying "+id); err != nil {
			return err
		}
		if in.Result == "pass" {
			if drift := checkArtifacts(st.Project.Roots, latest.Artifacts); len(drift) > 0 {
				return fmt.Errorf("%w: submission %d of %s changed since it was submitted: %s; the owner must resubmit", ErrStaleEvidence, latest.N, id, driftText(drift))
			}
		}
		evidence, err := resolveArtifacts(st.Project.Roots, in.Evidence)
		if err != nil {
			return err
		}
		t.Verifications = appendBounded(t.Verifications, Verification{Submission: latest.N, By: actor.Label, ByVerified: actor.Verified, Result: in.Result,
			Checks: in.Checks, Evidence: evidence, Validator: in.Validator, At: s.stamp()})
		note := fmt.Sprintf("submission %d %s", latest.N, in.Result)
		if in.Result == "pass" {
			t.State, t.LeaseUntil = TaskVerified, ""
		} else {
			t.Attempts++
			if t.Attempts >= t.MaxAttempts {
				t.State, t.LeaseUntil = TaskFailed, ""
				note += fmt.Sprintf("; attempt budget %d spent", t.MaxAttempts)
			} else {
				t.State = TaskWorking
				t.LeaseUntil = s.now().Add(DefaultLease).Format(time.RFC3339Nano)
			}
		}
		s.touchTask(t)
		ch.add(taskEvent(t, "verify", note))
		out = *t
		return nil
	})
	return out, err
}

// Cancel ends a task that is not verified. The creator or the lead may.
func (s *Store) Cancel(actor Actor, project, id, reason string) (Task, error) {
	if err := textField("reason", reason, 2048, true); err != nil {
		return Task{}, err
	}
	var out Task
	err := s.mutate(project, actor, func(st *State, ch *change) error {
		t, err := st.task(id)
		if err != nil {
			return err
		}
		if actor.Label != t.CreatedBy && actor.Label != st.Project.Lead {
			return fmt.Errorf("%w: only %s (creator) or the lead cancels %s", ErrForbidden, t.CreatedBy, id)
		}
		if t.State == TaskVerified || t.State == TaskCancelled {
			return fmt.Errorf("%w: %s is %s", ErrConflict, id, t.State)
		}
		t.State, t.Owner, t.LeaseUntil = TaskCancelled, "", ""
		t.Fence++
		s.touchTask(t)
		ch.add(taskEvent(t, "cancel", reason))
		out = *t
		return nil
	})
	return out, err
}

// Reopen returns a failed or cancelled task to pending with a fresh
// attempt budget. Only the lead may.
func (s *Store) Reopen(actor Actor, project, id, reason string) (Task, error) {
	if err := textField("reason", reason, 2048, true); err != nil {
		return Task{}, err
	}
	var out Task
	err := s.mutate(project, actor, func(st *State, ch *change) error {
		t, err := st.task(id)
		if err != nil {
			return err
		}
		if actor.Label != st.Project.Lead {
			return fmt.Errorf("%w: only the lead (%s) reopens tasks", ErrForbidden, st.Project.Lead)
		}
		if t.State != TaskFailed && t.State != TaskCancelled {
			return fmt.Errorf("%w: %s is %s; only failed or cancelled tasks reopen", ErrConflict, id, t.State)
		}
		t.State, t.Owner, t.LeaseUntil, t.Attempts = TaskPending, "", "", 0
		t.Fence++
		s.touchTask(t)
		ch.add(taskEvent(t, "reopen", reason))
		out = *t
		return nil
	})
	return out, err
}

// TaskView is a task as read now: the stored record plus what a fresh
// look at the disk and the clock says about it.
type TaskView struct {
	Task
	// LeaseExpired is true when the owner's lease has ended; the task is
	// then recoverable.
	LeaseExpired bool `json:"leaseExpired,omitempty"`
	// ArtifactDrift lists artifacts of the latest submission (submitted or
	// verified) whose bytes or target changed since.
	ArtifactDrift []Drift `json:"artifactDrift,omitempty"`
	// Blockers explains why a pending task cannot be claimed.
	Blockers []string `json:"blockers,omitempty"`
}

func (s *Store) taskView(st *State, t *Task) TaskView {
	v := TaskView{Task: *t}
	switch t.State {
	case TaskClaimed, TaskWorking, TaskBlocked:
		v.LeaseExpired = s.leaseExpired(t)
	case TaskSubmitted, TaskVerified:
		if n := len(t.Submissions); n > 0 {
			v.ArtifactDrift = checkArtifacts(st.Project.Roots, t.Submissions[n-1].Artifacts)
		}
	case TaskPending:
		failed, waiting, stale := st.depStatus(t)
		v.Blockers = append(append(append(v.Blockers, failed...), waiting...), stale...)
	}
	return v
}

// GetTask returns one task as read now.
func (s *Store) GetTask(project, id string) (TaskView, error) {
	var out TaskView
	err := s.read(project, func(st *State) error {
		t, err := st.task(id)
		if err != nil {
			return err
		}
		out = s.taskView(st, t)
		return nil
	})
	return out, err
}

// Tasks lists tasks in id order, optionally only those in one state or
// held by one owner.
func (s *Store) Tasks(project, state, owner string) ([]TaskView, error) {
	out := []TaskView{}
	err := s.read(project, func(st *State) error {
		for _, id := range st.taskIDs() {
			t := st.Tasks[id]
			if (state == "" || t.State == state) && (owner == "" || t.Owner == owner) {
				out = append(out, s.taskView(st, t))
			}
		}
		return nil
	})
	return out, err
}

func (st *State) taskIDs() []string {
	ids := make([]string, 0, len(st.Tasks))
	for id := range st.Tasks {
		ids = append(ids, id)
	}
	sortIDs(ids)
	return ids
}

func appendBounded[T any](list []T, v T) []T {
	list = append(list, v)
	if len(list) > maxListed {
		list = append([]T(nil), list[len(list)-maxListed:]...)
	}
	return list
}
