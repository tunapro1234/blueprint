package ledger

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var (
	lead  = Actor{Label: "lead", Verified: true}
	ada   = Actor{Label: "ada", Verified: true}
	bob   = Actor{Label: "bob", Verified: true}
	carol = Actor{Label: "carol", Verified: true}
)

type fixture struct {
	t     *testing.T
	store *Store
	clock *clock
	root  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "work")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
	f := &fixture{t: t, store: &Store{Dir: filepath.Join(base, "ledger"), Now: c.now}, clock: c, root: root}
	if _, err := f.store.CreateProject(lead, ProjectSpec{Name: "demo", Goal: "ship the demo", Roots: []string{root}}); err != nil {
		t.Fatal(err)
	}
	return f
}

// reopen returns a fresh Store over the same directory: a restart.
func (f *fixture) reopen() *Store {
	return &Store{Dir: f.store.Dir, Now: f.clock.now, MaxEvents: f.store.MaxEvents}
}

func (f *fixture) file(name, content string) string {
	f.t.Helper()
	path := filepath.Join(f.root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		f.t.Fatal(err)
	}
	return real
}

func (f *fixture) task(title string, deps ...string) Task {
	f.t.Helper()
	task, err := f.store.CreateTask(lead, "demo", TaskSpec{Title: title, Deps: deps})
	if err != nil {
		f.t.Fatal(err)
	}
	return task
}

// must fails the test (by panic, which the test runner reports with the
// caller's line) when a call that should succeed returns an error.
func must[T any](v T, err error) T {
	if err != nil {
		panic(fmt.Sprintf("unexpected error: %v", err))
	}
	return v
}

func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("got error %v, want %v", err, target)
	}
}

func TestConcurrentClaimsHaveOneWinner(t *testing.T) {
	f := newFixture(t)
	f.task("one task")
	const n = 16
	var wg sync.WaitGroup
	wins := make(chan string, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Separate Store values share only the directory, like separate
			// processes sharing the flock.
			s := f.reopen()
			who := Actor{Label: fmt.Sprintf("agent%d", i)}
			if _, err := s.Claim(who, "demo", "t1", 0); err == nil {
				wins <- who.Label
			} else {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(wins)
	close(errs)
	if len(wins) != 1 {
		t.Fatalf("%d claimants won, want exactly 1", len(wins))
	}
	for err := range errs {
		wantErr(t, err, ErrConflict)
	}
	task := must(f.store.GetTask("demo", "t1"))
	if task.Fence != 1 || task.State != TaskClaimed {
		t.Fatalf("task after race: %+v", task)
	}
}

func TestConcurrentClaimsAcrossProcesses(t *testing.T) {
	if os.Getenv("LEDGER_CHILD_DIR") != "" {
		s := &Store{Dir: os.Getenv("LEDGER_CHILD_DIR")}
		if _, err := s.Claim(Actor{Label: os.Getenv("LEDGER_CHILD_NAME")}, "demo", "t1", 0); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	f := newFixture(t)
	f.task("one task")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const n = 6
	cmds := make([]*exec.Cmd, n)
	for i := range cmds {
		cmds[i] = exec.Command(exe, "-test.run=^TestConcurrentClaimsAcrossProcesses$")
		cmds[i].Env = append(os.Environ(), "LEDGER_CHILD_DIR="+f.store.Dir, fmt.Sprintf("LEDGER_CHILD_NAME=proc%d", i))
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	wins := 0
	for _, c := range cmds {
		if c.Wait() == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d processes won the claim, want 1", wins)
	}
}

func TestStaleOwnerCannotWriteAfterRecovery(t *testing.T) {
	f := newFixture(t)
	f.task("t")
	art := f.file("out.txt", "v1")
	claimed := must(f.store.Claim(ada, "demo", "t1", time.Minute))
	adaFence := OwnerProof{Fence: claimed.Fence}

	// Live lease: bob cannot recover, the lead could.
	_, err := f.store.Recover(bob, "demo", "t1", "looks stuck")
	wantErr(t, err, ErrForbidden)

	// Lease expires: ada's writes need a renew first.
	f.clock.advance(2 * time.Minute)
	_, err = f.store.Checkpoint(ada, "demo", "t1", adaFence, "still here", 10)
	wantErr(t, err, ErrLeaseExpired)

	// bob recovers explicitly and claims; the fence moves on twice.
	must(f.store.Recover(bob, "demo", "t1", "ada's lease expired"))
	bobTask := must(f.store.Claim(bob, "demo", "t1", time.Minute))
	if bobTask.Fence != claimed.Fence+2 {
		t.Fatalf("fence %d, want %d", bobTask.Fence, claimed.Fence+2)
	}

	// Every write by the old owner is refused, even a renew.
	_, err = f.store.Renew(ada, "demo", "t1", adaFence, time.Minute)
	wantErr(t, err, ErrStaleOwner)
	_, err = f.store.Submit(ada, "demo", "t1", adaFence, []ArtifactRef{{Path: art}}, "")
	wantErr(t, err, ErrStaleOwner)

	// ada re-claiming under her own name still cannot reuse the old fence.
	must(f.store.Release(bob, "demo", "t1", OwnerProof{Fence: bobTask.Fence}, "handing back"))
	again := must(f.store.Claim(ada, "demo", "t1", time.Minute))
	_, err = f.store.Checkpoint(ada, "demo", "t1", adaFence, "old fence", 0)
	wantErr(t, err, ErrStaleOwner)
	must(f.store.Checkpoint(ada, "demo", "t1", OwnerProof{Fence: again.Fence}, "new fence", 5))
}

func TestExpiredLeaseRenewsWithoutRecovery(t *testing.T) {
	f := newFixture(t)
	f.task("t")
	claimed := must(f.store.Claim(ada, "demo", "t1", time.Minute))
	f.clock.advance(5 * time.Minute)
	renewed := must(f.store.Renew(ada, "demo", "t1", OwnerProof{Fence: claimed.Fence}, time.Minute))
	if renewed.Fence != claimed.Fence {
		t.Fatalf("renew changed the fence")
	}
	must(f.store.Checkpoint(ada, "demo", "t1", OwnerProof{Fence: claimed.Fence}, "back", 20))
	// The lead may take over a live lease, recorded.
	rec := must(f.store.Recover(lead, "demo", "t1", "reassigning"))
	if rec.State != TaskPending || rec.Owner != "" {
		t.Fatalf("after lead recover: %+v", rec)
	}
	hist := must(f.store.TaskHistory("demo", "t1"))
	last := hist[len(hist)-1]
	if last.Op != "recover" || !strings.Contains(last.Note, "from ada: reassigning") || last.Actor != "lead" {
		t.Fatalf("recover event: %+v", last)
	}
}

func TestPersistenceAcrossRestartAndTornTail(t *testing.T) {
	f := newFixture(t)
	f.task("a")
	claimed := must(f.store.Claim(ada, "demo", "t1", time.Hour))
	must(f.store.Checkpoint(ada, "demo", "t1", OwnerProof{Fence: claimed.Fence}, "half", 50))

	// Simulate a crash after the log append but before the snapshot: put
	// the snapshot back to an older state.
	dir := f.store.projectDir("demo")
	old, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	must(f.store.Checkpoint(ada, "demo", "t1", OwnerProof{Fence: claimed.Fence}, "three quarters", 75))
	if err := os.WriteFile(filepath.Join(dir, "state.json"), old, 0o600); err != nil {
		t.Fatal(err)
	}
	// And a torn half-line at the end of the log.
	lf, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lf.WriteString(`{"seq":99,"kind":"task","id":"t1","task":{"id":"t1","sta`); err != nil {
		t.Fatal(err)
	}
	lf.Close()

	s := f.reopen()
	task := must(s.GetTask("demo", "t1"))
	if task.Owner != "ada" || task.Fence != claimed.Fence || task.Checkpoints[len(task.Checkpoints)-1].Note != "three quarters" {
		t.Fatalf("after restart: %+v", task)
	}
	// The next write cuts the torn tail instead of gluing onto it.
	must(s.Checkpoint(ada, "demo", "t1", OwnerProof{Fence: claimed.Fence}, "done soon", 90))
	s2 := f.reopen()
	hist := must(s2.TaskHistory("demo", "t1"))
	if got := hist[len(hist)-1].Note; got != "done soon" {
		t.Fatalf("last event note %q", got)
	}
	page := must(s2.Feed("demo", FeedQuery{}))
	for i := 1; i < len(page.Events); i++ {
		if page.Events[i].Seq != page.Events[i-1].Seq+1 {
			t.Fatalf("sequence gap: %d then %d", page.Events[i-1].Seq, page.Events[i].Seq)
		}
	}
}

func TestDependenciesFailuresAndCycles(t *testing.T) {
	f := newFixture(t)
	f.task("base")
	f.task("next", "t1")
	f.task("last", "t2")

	_, err := f.store.Claim(ada, "demo", "t2", 0)
	wantErr(t, err, ErrDependency)
	if !strings.Contains(err.Error(), "t1 (pending)") {
		t.Fatalf("dependency error should name the waiting dep: %v", err)
	}

	_, err = f.store.AddDep(lead, "demo", "t1", "t3")
	wantErr(t, err, ErrCycle)
	if !strings.Contains(err.Error(), "t1 -> t3 -> t2 -> t1") {
		t.Fatalf("cycle error should name the cycle: %v", err)
	}
	_, err = f.store.AddDep(lead, "demo", "t1", "t1")
	wantErr(t, err, ErrCycle)
	_, err = f.store.CreateTask(lead, "demo", TaskSpec{Title: "x", Deps: []string{"t99"}})
	wantErr(t, err, ErrNotFound)
	_, err = f.store.AddDep(ada, "demo", "t3", "t1")
	wantErr(t, err, ErrForbidden)

	// t1 fails its only attempt: t2 cannot start and the brief says why.
	one := must(f.store.CreateTask(lead, "demo", TaskSpec{Title: "fragile", MaxAttempts: 1}))
	f.task("after fragile", one.ID)
	art := f.file("frag.txt", "x")
	c := must(f.store.Claim(ada, "demo", one.ID, 0))
	must(f.store.Submit(ada, "demo", one.ID, OwnerProof{Fence: c.Fence}, []ArtifactRef{{Path: art}}, ""))
	failed := must(f.store.Verify(bob, "demo", one.ID, VerifyInput{Result: "fail", Checks: "wrong output"}))
	if failed.State != TaskFailed {
		t.Fatalf("state %s, want failed", failed.State)
	}
	_, err = f.store.Claim(ada, "demo", "t5", 0)
	wantErr(t, err, ErrDependency)
	if !strings.Contains(err.Error(), "t4 (failed)") {
		t.Fatalf("want failed dependency named: %v", err)
	}
	brief := must(f.store.Brief("demo", 0))
	if !hasIssue(brief, "dependency-failed", "t5") {
		t.Fatalf("brief issues %+v", brief.Issues)
	}
	// Reopen restores it.
	_, err = f.store.Reopen(ada, "demo", one.ID, "retry")
	wantErr(t, err, ErrForbidden)
	re := must(f.store.Reopen(lead, "demo", one.ID, "retry with a fix"))
	if re.State != TaskPending || re.Attempts != 0 {
		t.Fatalf("reopened: %+v", re)
	}
}

func hasIssue(b Brief, kind, id string) bool {
	for _, i := range b.Issues {
		if i.Kind == kind && i.ID == id {
			return true
		}
	}
	return false
}

func TestSubmissionVerificationRules(t *testing.T) {
	f := newFixture(t)
	f.store.CreateTask(lead, "demo", TaskSpec{Title: "report", Verifier: "carol"})
	art := f.file("report.md", "draft 1")
	c := must(f.store.Claim(ada, "demo", "t1", 0))
	proof := OwnerProof{Fence: c.Fence}

	first := must(f.store.Submit(ada, "demo", "t1", proof, []ArtifactRef{{Path: art, Revision: "r1"}}, "first"))
	again := must(f.store.Submit(ada, "demo", "t1", proof, []ArtifactRef{{Path: art, Revision: "r1"}}, "first"))
	if !again.Repeated || again.Task.Version != first.Task.Version || len(again.Task.Submissions) != 1 {
		t.Fatalf("identical resubmission should be a no-op: %+v", again)
	}
	_, err := f.store.Submit(ada, "demo", "t1", proof, []ArtifactRef{{Path: art, Digest: "sha256:00"}}, "")
	wantErr(t, err, ErrStaleEvidence)

	// Self-verification and a non-named verifier are refused; a transport
	// receipt has no path to verification at all.
	_, err = f.store.Verify(ada, "demo", "t1", VerifyInput{Result: "pass", Checks: "I checked"})
	wantErr(t, err, ErrForbidden)
	_, err = f.store.Verify(bob, "demo", "t1", VerifyInput{Result: "pass", Checks: "looks fine"})
	wantErr(t, err, ErrForbidden)

	// The artifact changes after submission: a pass is refused.
	f.file("report.md", "draft 1, edited after submit")
	_, err = f.store.Verify(carol, "demo", "t1", VerifyInput{Result: "pass", Checks: "read it"})
	wantErr(t, err, ErrStaleEvidence)
	brief := must(f.store.Brief("demo", 0))
	if !hasIssue(brief, "submission-changed", "t1") {
		t.Fatalf("brief should flag changed submission: %+v", brief.Issues)
	}

	// Resubmitting the new bytes supersedes submission 1.
	second := must(f.store.Submit(ada, "demo", "t1", proof, []ArtifactRef{{Path: art, Revision: "r2"}}, "second"))
	if n := second.Task.Submissions[len(second.Task.Submissions)-1].N; n != 2 {
		t.Fatalf("submission number %d", n)
	}
	_, err = f.store.Verify(carol, "demo", "t1", VerifyInput{Submission: 1, Result: "pass", Checks: "old"})
	wantErr(t, err, ErrConflict)
	_, err = f.store.Verify(carol, "demo", "t1", VerifyInput{Submission: 2, Result: "pass", Checks: "x", Validator: &ValidatorRun{Command: []string{"check"}, Exit: 1}})
	wantErr(t, err, ErrInvalid)
	_, err = f.store.Verify(carol, "demo", "t1", VerifyInput{Result: "fail", Checks: "x", Validator: &ValidatorRun{Command: []string{"check"}, Exit: 1}})
	wantErr(t, err, ErrInvalid) // a validator record must pin the submission

	failed := must(f.store.Verify(carol, "demo", "t1", VerifyInput{Result: "fail", Checks: "missing section 3"}))
	if failed.State != TaskWorking || failed.Attempts != 1 || failed.Owner != "ada" {
		t.Fatalf("after fail: %+v", failed)
	}
	f.file("report.md", "draft 3 with section 3")
	must(f.store.Submit(ada, "demo", "t1", proof, []ArtifactRef{{Path: art, Revision: "r3"}}, "third"))
	ok := must(f.store.Verify(carol, "demo", "t1", VerifyInput{Submission: 3, Result: "pass", Checks: "section 3 present; ran check.py",
		Validator: &ValidatorRun{Command: []string{"python3", "check.py"}, Exit: 0, Output: "ok"}}))
	if ok.State != TaskVerified {
		t.Fatalf("state %s", ok.State)
	}
	_, err = f.store.Submit(ada, "demo", "t1", proof, []ArtifactRef{{Path: art}}, "")
	wantErr(t, err, ErrConflict)

	// A verified task whose artifact later changes is flagged, not silently trusted.
	f.file("report.md", "tampered")
	brief = must(f.store.Brief("demo", 0))
	if !hasIssue(brief, "verified-artifact-changed", "t1") {
		t.Fatalf("brief should flag a changed verified artifact: %+v", brief.Issues)
	}
}

func TestArtifactPathBoundaries(t *testing.T) {
	f := newFixture(t)
	f.task("t")
	c := must(f.store.Claim(ada, "demo", "t1", 0))
	proof := OwnerProof{Fence: c.Fence}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := f.store.Submit(ada, "demo", "t1", proof, []ArtifactRef{{Path: outside}}, "")
	wantErr(t, err, ErrForbidden)

	// A symlink inside the root pointing outside is refused.
	link := filepath.Join(f.root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.Submit(ada, "demo", "t1", proof, []ArtifactRef{{Path: link}}, "")
	wantErr(t, err, ErrForbidden)

	// Traversal is resolved before the check.
	_, err = f.store.Submit(ada, "demo", "t1", proof, []ArtifactRef{{Path: f.root + "/../" + filepath.Base(filepath.Dir(outside))}}, "")
	if err == nil {
		t.Fatal("traversal outside the root was accepted")
	}
	_, err = f.store.Submit(ada, "demo", "t1", proof, []ArtifactRef{{Path: "relative.txt"}}, "")
	wantErr(t, err, ErrInvalid)
	_, err = f.store.Submit(ada, "demo", "t1", proof, []ArtifactRef{{Path: f.root}}, "")
	wantErr(t, err, ErrInvalid) // a directory is not an artifact

	// A sibling directory sharing the root's prefix is outside.
	sibling := f.root + "-evil"
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.Submit(ada, "demo", "t1", proof, []ArtifactRef{{Path: filepath.Join(sibling, "x")}}, "")
	wantErr(t, err, ErrForbidden)

	// Only the lead adds roots.
	_, err = f.store.AddRoot(ada, "demo", sibling)
	wantErr(t, err, ErrForbidden)
	_, err = f.store.CreateProject(lead, ProjectSpec{Name: "rootless", Goal: "g", Roots: []string{"/"}})
	wantErr(t, err, ErrInvalid)
	_, err = f.store.CreateProject(lead, ProjectSpec{Name: "../escape", Goal: "g"})
	wantErr(t, err, ErrInvalid)
}

func TestFindingEvidenceAndInvalidation(t *testing.T) {
	f := newFixture(t)
	src := f.file("data/bench.csv", "run,ms\n1,120\n")
	fd := must(f.store.AddFinding(ada, "demo", FindingSpec{Assertion: "p95 is under 150 ms", Sources: []ArtifactRef{{Path: src, Revision: "abc123"}}}))
	if fd.Status != FindingHypothesis || fd.Sources[0].Digest == "" {
		t.Fatalf("new finding: %+v", fd)
	}
	_, err := f.store.JudgeFinding(ada, "demo", fd.ID, FindingVerified, "I say so", 0)
	wantErr(t, err, ErrForbidden)
	v := must(f.store.JudgeFinding(bob, "demo", fd.ID, FindingVerified, "re-ran bench.csv analysis: p95 120 ms", 0))
	if v.Status != FindingVerified || v.Checks[0].By != "bob" || v.Checks[0].Saw[0].Digest != fd.Sources[0].Digest {
		t.Fatalf("verified finding: %+v", v)
	}
	fresh := must(f.store.CheckFinding(carol, "demo", fd.ID))
	if !fresh.Fresh || fresh.Finding.Status != FindingVerified {
		t.Fatalf("fresh check: %+v", fresh)
	}

	// The input changes: reads report stale before anyone writes.
	f.file("data/bench.csv", "run,ms\n1,120\n2,400\n")
	brief := must(f.store.Brief("demo", 0))
	if len(brief.Stale) != 1 || brief.Stale[0].Status != FindingStale || brief.Stale[0].Stored != FindingVerified {
		t.Fatalf("brief should show effective stale: %+v", brief.Stale)
	}
	// A verification on changed input is refused.
	_, err = f.store.JudgeFinding(carol, "demo", fd.ID, FindingVerified, "still true?", 0)
	wantErr(t, err, ErrStaleEvidence)
	// Check persists stale; feed readers see it.
	checked := must(f.store.CheckFinding(carol, "demo", fd.ID))
	if checked.Fresh || checked.Finding.Status != FindingStale {
		t.Fatalf("check: %+v", checked)
	}
	page := must(f.store.Feed("demo", FeedQuery{Filter: FeedFilter{Kinds: []string{"finding"}}}))
	if last := page.Events[len(page.Events)-1]; last.Op != "stale" || last.State != FindingStale {
		t.Fatalf("feed tail: %+v", last)
	}
	// Only the author revises; revision re-hashes and returns to hypothesis
	// with history kept.
	_, err = f.store.ReviseFinding(bob, "demo", fd.ID, FindingSpec{Assertion: "p95 is 400 ms"}, 0)
	wantErr(t, err, ErrForbidden)
	rev := must(f.store.ReviseFinding(ada, "demo", fd.ID, FindingSpec{Assertion: "p95 is under 450 ms with run 2"}, 0))
	if rev.Status != FindingHypothesis || rev.Revision != 2 || len(rev.Checks) != 3 {
		t.Fatalf("revised: %+v", rev)
	}
	hist := must(f.store.FindingHistory("demo", fd.ID))
	ops := []string{}
	for _, ev := range hist {
		ops = append(ops, ev.Op)
	}
	if strings.Join(ops, ",") != "add,verified,fresh,stale,revise" {
		t.Fatalf("history ops %v", ops)
	}
	if hist[1].Finding.Assertion != "p95 is under 150 ms" {
		t.Fatalf("history should keep the original assertion")
	}
}

func TestNegativeResults(t *testing.T) {
	f := newFixture(t)
	log := f.file("logs/try-lr.txt", "loss diverged at step 40")
	_, err := f.store.AddFinding(ada, "demo", FindingSpec{Kind: KindNegative, Assertion: "lr 1e-2 diverges"})
	wantErr(t, err, ErrInvalid)
	neg := must(f.store.AddFinding(ada, "demo", FindingSpec{Kind: KindNegative, Assertion: "lr 1e-2 diverges",
		Conditions: "batch 8, fp16, no warmup", Sources: []ArtifactRef{{Path: log}}}))
	must(f.store.JudgeFinding(bob, "demo", neg.ID, FindingVerified, "reproduced divergence at step 38", 0))
	brief := must(f.store.Brief("demo", 0))
	if len(brief.Negatives) != 1 || brief.Negatives[0].Conditions != "batch 8, fp16, no warmup" || brief.Negatives[0].Evidence == "" {
		t.Fatalf("negative in brief: %+v", brief.Negatives)
	}
	if !strings.Contains(brief.Text(), "Failed experiments (avoid repeating)") {
		t.Fatalf("text brief should list failed experiments")
	}
}

func TestFeedCursorResumeReplayFilter(t *testing.T) {
	f := newFixture(t)
	f.task("a")
	f.task("b")
	head := must(f.store.Feed("demo", FeedQuery{}))
	if len(head.Events) != 3 { // project create + 2 tasks
		t.Fatalf("events %d", len(head.Events))
	}
	c := must(f.store.Claim(ada, "demo", "t2", 0))
	must(f.store.Checkpoint(ada, "demo", "t2", OwnerProof{Fence: c.Fence}, "step", 1))
	must(f.store.AddFinding(bob, "demo", FindingSpec{Assertion: "x"}))

	// Resume from the earlier cursor in a "restarted" store.
	s := f.reopen()
	p1 := must(s.Feed("demo", FeedQuery{After: head.Next, Limit: 2}))
	if len(p1.Events) != 2 || !p1.More || p1.Events[0].Op != "claim" {
		t.Fatalf("page 1: %+v", p1)
	}
	p1again := must(s.Feed("demo", FeedQuery{After: head.Next, Limit: 2}))
	if p1again.Next != p1.Next || p1again.Events[1].Seq != p1.Events[1].Seq {
		t.Fatalf("replay from the same cursor differs")
	}
	p2 := must(s.Feed("demo", FeedQuery{After: p1.Next}))
	if len(p2.Events) != 1 || p2.More || p2.Next != p2.Head {
		t.Fatalf("page 2: %+v", p2)
	}

	// Filtering by kind and by involvement; the cursor passes skipped events.
	tasks := must(s.Feed("demo", FeedQuery{After: head.Next, Filter: FeedFilter{Kinds: []string{"finding"}}}))
	if len(tasks.Events) != 1 || tasks.Events[0].Kind != "finding" || tasks.Next != tasks.Head {
		t.Fatalf("kind filter: %+v", tasks)
	}
	mine := must(s.Feed("demo", FeedQuery{Filter: FeedFilter{Involving: "ada"}, Summary: true}))
	for _, ev := range mine.Events {
		if ev.Task != nil {
			t.Fatalf("summary should drop bodies")
		}
		if ev.Owner != "ada" && ev.Actor != "ada" {
			t.Fatalf("involving filter leaked %+v", ev)
		}
	}
	if len(mine.Events) != 2 {
		t.Fatalf("ada events %d", len(mine.Events))
	}

	// Bad cursors.
	_, err := s.Feed("demo", FeedQuery{After: "nope"})
	wantErr(t, err, ErrCursorInvalid)
	_, err = s.Feed("demo", FeedQuery{After: "otherepoch:1"})
	wantErr(t, err, ErrCursorInvalid)
	epoch, _, _ := parseCursor(head.Head)
	_, err = s.Feed("demo", FeedQuery{After: formatCursor(epoch, 999)})
	wantErr(t, err, ErrCursorInvalid)
	_, err = s.Feed("demo", FeedQuery{Filter: FeedFilter{Kinds: []string{"msg"}}})
	wantErr(t, err, ErrInvalid)
}

func TestFeedRetentionExpiresOldCursors(t *testing.T) {
	f := newFixture(t)
	f.store.MaxEvents = 10
	start := must(f.store.Feed("demo", FeedQuery{}))
	for i := 0; i < 12; i++ {
		f.task(fmt.Sprintf("t%d", i))
	}
	_, err := f.store.Feed("demo", FeedQuery{After: start.Head})
	wantErr(t, err, ErrCursorExpired)
	page := must(f.store.Feed("demo", FeedQuery{}))
	if len(page.Events) == 0 || len(page.Events) > 10 {
		t.Fatalf("retained %d events", len(page.Events))
	}
	// State survives compaction in the snapshot.
	s := f.reopen()
	if tasks := must(s.Tasks("demo", "", "")); len(tasks) != 12 {
		t.Fatalf("tasks after compaction: %d", len(tasks))
	}
	// The brief's cursor is always valid to continue from.
	b := must(s.Brief("demo", 0))
	if _, err := s.Feed("demo", FeedQuery{After: b.Cursor}); err != nil {
		t.Fatalf("brief cursor: %v", err)
	}
}

func TestSubscriptionResumeAndAck(t *testing.T) {
	f := newFixture(t)
	sub := must(f.store.Subscribe(bob, "demo", FeedFilter{Kinds: []string{"task"}}, ""))
	f.task("a")
	must(f.store.AddFinding(ada, "demo", FindingSpec{Assertion: "noise"}))
	f.task("b")

	s := f.reopen() // bob restarts
	page := must(s.FeedSubscribed(bob, "demo", 1, false))
	if len(page.Events) != 1 || page.Events[0].ID != "t1" {
		t.Fatalf("first page %+v", page)
	}
	// Not acked: the same events come back (at-least-once).
	again := must(s.FeedSubscribed(bob, "demo", 1, false))
	if again.Events[0].Seq != page.Events[0].Seq {
		t.Fatalf("unacked read should replay")
	}
	must(s.Ack(bob, "demo", page.Next))
	rest := must(s.FeedSubscribed(bob, "demo", 10, false))
	if len(rest.Events) != 1 || rest.Events[0].ID != "t2" {
		t.Fatalf("after ack %+v", rest)
	}
	_, err := s.Ack(bob, "demo", sub.Cursor)
	wantErr(t, err, ErrConflict) // backwards
	_, err = s.FeedSubscribed(carol, "demo", 10, false)
	wantErr(t, err, ErrNotFound)
}

func TestBriefIsBoundedAndDeterministic(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 30; i++ {
		f.task(fmt.Sprintf("task %d %s", i, strings.Repeat("long ", 55)))
	}
	c := must(f.store.Claim(ada, "demo", "t3", time.Minute))
	must(f.store.Checkpoint(ada, "demo", "t3", OwnerProof{Fence: c.Fence}, "working on it", 30))
	f.clock.advance(time.Hour)

	b1 := must(f.store.Brief("demo", 5))
	b2 := must(f.reopen().Brief("demo", 5))
	if b1.Text() != b2.Text() {
		t.Fatalf("brief is not deterministic")
	}
	if len(b1.Claimable) != 5 || b1.Omit["claimable"] != 24 {
		t.Fatalf("claimable %d omitted %d", len(b1.Claimable), b1.Omit["claimable"])
	}
	if len(b1.Claimable[0].Title) > briefTextLimit+3 {
		t.Fatalf("titles are not clipped")
	}
	if !hasIssue(b1, "lease-expired", "t3") || !strings.HasPrefix(b1.Next[0], "recover t3") {
		t.Fatalf("expired lease should lead the next actions: %v", b1.Next)
	}
	if b1.Cursor == "" || !strings.Contains(b1.Text(), "bp work feed demo --after "+b1.Cursor) {
		t.Fatalf("brief should carry its cursor")
	}
	if len(b1.Text()) > 16<<10 {
		t.Fatalf("text brief is %d bytes", len(b1.Text()))
	}
}

func TestProjectPauseStopAndLeadOnly(t *testing.T) {
	f := newFixture(t)
	f.task("a")
	art := f.file("a.txt", "a")
	_, err := f.store.SetProjectState(ada, "demo", ProjectPaused, "")
	wantErr(t, err, ErrForbidden)
	c := must(f.store.Claim(ada, "demo", "t1", 0))
	must(f.store.SetProjectState(lead, "demo", ProjectPaused, "budget review"))
	f.task2Err(t, ErrHalted)
	// A held task can still be submitted while paused; nothing new starts.
	must(f.store.Submit(ada, "demo", "t1", OwnerProof{Fence: c.Fence}, []ArtifactRef{{Path: art}}, ""))
	must(f.store.SetProjectState(lead, "demo", ProjectStopped, "halt"))
	_, err = f.store.Verify(bob, "demo", "t1", VerifyInput{Result: "pass", Checks: "x"})
	wantErr(t, err, ErrHalted)
	b := must(f.store.Brief("demo", 0))
	if !hasIssue(b, "project-stopped", "demo") {
		t.Fatalf("brief should show the stop: %+v", b.Issues)
	}
	must(f.store.SetProjectState(lead, "demo", ProjectActive, ""))
	must(f.store.Verify(bob, "demo", "t1", VerifyInput{Result: "pass", Checks: "x"}))
}

func (f *fixture) task2Err(t *testing.T, target error) {
	t.Helper()
	_, err := f.store.CreateTask(lead, "demo", TaskSpec{Title: "new"})
	wantErr(t, err, target)
	_, err = f.store.Claim(bob, "demo", "t1", 0)
	if err == nil {
		t.Fatal("claim during pause should fail")
	}
}

func TestInputValidation(t *testing.T) {
	f := newFixture(t)
	_, err := f.store.CreateTask(lead, "demo", TaskSpec{Title: "two\nlines"})
	wantErr(t, err, ErrInvalid)
	_, err = f.store.CreateTask(lead, "demo", TaskSpec{Title: "bell\a"})
	wantErr(t, err, ErrInvalid)
	_, err = f.store.CreateTask(Actor{Label: ""}, "demo", TaskSpec{Title: "x"})
	wantErr(t, err, ErrInvalid)
	_, err = f.store.Claim(ada, "demo", "t1", 48*time.Hour)
	wantErr(t, err, ErrInvalid)
	_, err = f.store.GetProject("missing")
	wantErr(t, err, ErrNotFound)
	if _, statErr := os.Stat(f.store.projectDir("missing")); !os.IsNotExist(statErr) {
		t.Fatal("a read of an unknown project created its directory")
	}
	_, err = f.store.CreateProject(lead, ProjectSpec{Name: "demo", Goal: "again"})
	wantErr(t, err, ErrConflict)
}
