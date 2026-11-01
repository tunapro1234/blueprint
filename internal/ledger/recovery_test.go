package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptionInTheMiddleFailsClosed(t *testing.T) {
	for name, damage := range map[string]func([]string) []string{
		"malformed line": func(l []string) []string { l[1] = `{"seq": not json`; return l },
		"sequence gap":   func(l []string) []string { return append(l[:1], l[2:]...) },
		"foreign epoch": func(l []string) []string {
			var ev Event
			json.Unmarshal([]byte(l[1]), &ev)
			ev.Epoch = "someoneelse"
			b, _ := json.Marshal(ev)
			l[1] = string(b)
			return l
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.task("a")
			f.task("b")
			f.task("c")
			log := f.store.logPath("demo")
			writeLines(t, log, damage(readLines(t, log)))
			_, err := f.reopen().GetTask("demo", "t1")
			wantErr(t, err, ErrCorrupt)
			_, err = f.reopen().CreateTask(lead, "demo", TaskSpec{Title: "d"})
			wantErr(t, err, ErrCorrupt) // and nothing is appended over it
		})
	}
}

func TestIncompleteFinalTransactionIsIgnoredAndCut(t *testing.T) {
	f := newFixture(t)
	f.task("a")
	log := f.store.logPath("demo")
	before := readLines(t, log)
	// A two-event transaction of which only the first event reached disk.
	last := Event{}
	json.Unmarshal([]byte(before[len(before)-1]), &last)
	partial := Event{Seq: last.Seq + 1, Tx: last.Seq + 1, TxSize: 2, Epoch: last.Epoch, Kind: "task", ID: "t9", Op: "create",
		Task: &Task{ID: "t9", Title: "ghost", State: TaskPending}}
	b, _ := json.Marshal(partial)
	writeLines(t, log, append(before, string(b)))

	s := f.reopen()
	if _, err := s.GetTask("demo", "t9"); err == nil {
		t.Fatal("an incomplete transaction was applied")
	}
	must(s.CreateTask(lead, "demo", TaskSpec{Title: "real"}))
	lines := readLines(t, log)
	if len(lines) != len(before)+1 || strings.Contains(strings.Join(lines, ""), "ghost") {
		t.Fatalf("partial transaction not cut before the append:\n%s", strings.Join(lines, "\n"))
	}
	if task := must(f.reopen().GetTask("demo", "t2")); task.Title != "real" {
		t.Fatalf("t2 = %+v", task)
	}
}

func TestSnapshotFailureAfterDurableAppendIsCommitted(t *testing.T) {
	f := newFixture(t)
	f.task("a")
	f.store.failSnapshot = true
	claimed := must(f.store.Claim(ada, "demo", "t1", time.Hour))
	f.store.failSnapshot = false
	// The snapshot still says pending; the log says claimed. The log wins.
	s := f.reopen()
	task := must(s.GetTask("demo", "t1"))
	if task.Owner != "ada" || task.Fence != claimed.Fence {
		t.Fatalf("durable claim lost: %+v", task)
	}
	// A retry by someone else sees the committed claim, not a free task.
	_, err := s.Claim(bob, "demo", "t1", 0)
	wantErr(t, err, ErrConflict)
}

func TestMissingSnapshotAndLogAheadChecks(t *testing.T) {
	f := newFixture(t)
	f.task("a")
	dir := f.store.projectDir("demo")
	// Without a snapshot the full log rebuilds the state.
	os.Remove(filepath.Join(dir, "state.json"))
	if task := must(f.reopen().GetTask("demo", "t1")); task.Title != "a" {
		t.Fatalf("rebuild from log: %+v", task)
	}
	// A snapshot ahead of the log (log truncated by hand) is corruption.
	f.task("b")
	lines := readLines(t, f.store.logPath("demo"))
	writeLines(t, f.store.logPath("demo"), lines[:len(lines)-1])
	_, err := f.reopen().GetTask("demo", "t1")
	wantErr(t, err, ErrCorrupt)
}

func TestCompactionCrashKeepsFloorHonestAndHistoryComplete(t *testing.T) {
	f := newFixture(t)
	f.store.MaxEvents = 8
	src := f.file("in.txt", "v1")
	fd := must(f.store.AddFinding(ada, "demo", FindingSpec{Assertion: "original claim", Sources: []ArtifactRef{{Path: src}}}))
	must(f.store.JudgeFinding(bob, "demo", fd.ID, FindingVerified, "checked by hand", 0))
	start := must(f.store.Feed("demo", FeedQuery{})).Next
	for i := 0; i < 20; i++ {
		f.task(fmt.Sprintf("t%d", i))
	}
	// The early events are archived, not lost: provenance survives.
	hist := must(f.store.FindingHistory("demo", fd.ID))
	if len(hist) != 2 || hist[0].Op != "add" || hist[0].Finding.Assertion != "original claim" || hist[1].Op != "verified" {
		t.Fatalf("history after compaction: %+v", hist)
	}
	_, err := f.store.Feed("demo", FeedQuery{After: start})
	wantErr(t, err, ErrCursorExpired)

	// Simulate a crash after the archive segment was written but before
	// the live log was rewritten: events are in both places.
	dir := f.store.projectDir("demo")
	segs, _ := filepath.Glob(filepath.Join(dir, "archive", "*.jsonl"))
	if len(segs) == 0 {
		t.Fatal("no archive segment")
	}
	var archived []string
	for _, seg := range segs {
		archived = append(archived, readLines(t, seg)...)
	}
	live := readLines(t, f.store.logPath("demo"))
	writeLines(t, f.store.logPath("demo"), append(archived, live...))
	s := f.reopen()
	hist = must(s.FindingHistory("demo", fd.ID))
	if len(hist) != 2 {
		t.Fatalf("duplicate events not deduplicated: %d", len(hist))
	}
	// The floor comes from the live log, never from a stale snapshot field:
	// with the old events back in the log, the old cursor is valid again
	// and replays exactly the retained events.
	page := must(s.Feed("demo", FeedQuery{After: start, Limit: 500}))
	if len(page.Events) == 0 || page.Events[0].Seq != mustSeq(t, start)+1 {
		t.Fatalf("feed after crash-restore: %+v", page.Events[:1])
	}
}

func mustSeq(t *testing.T, cursor string) int64 {
	_, seq, err := parseCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func TestSymlinkRetargetIsDrift(t *testing.T) {
	f := newFixture(t)
	a := f.file("a.txt", "same bytes")
	b := f.file("b.txt", "other bytes")
	link := filepath.Join(f.root, "current.txt")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	fd := must(f.store.AddFinding(ada, "demo", FindingSpec{Assertion: "current is good", Sources: []ArtifactRef{{Path: link}}}))
	if fd.Sources[0].Path != link || fd.Sources[0].Target != a {
		t.Fatalf("artifact should keep logical path and target: %+v", fd.Sources[0])
	}
	must(f.store.JudgeFinding(bob, "demo", fd.ID, FindingVerified, "read it", 0))
	// Retarget the link; a.txt itself is untouched.
	os.Remove(link)
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	view := must(f.store.GetFinding("demo", fd.ID))
	if view.Effective != FindingStale || view.Status != FindingVerified || !strings.Contains(view.Drift[0].Problem, "now resolves to") {
		t.Fatalf("retargeted link should read stale: %+v", view)
	}
}

func TestStalePrerequisiteBlocksDependentClaim(t *testing.T) {
	f := newFixture(t)
	f.task("build")
	f.task("deploy", "t1")
	art := f.file("build.out", "v1")
	c := must(f.store.Claim(ada, "demo", "t1", 0))
	must(f.store.Submit(ada, "demo", "t1", OwnerProof{Fence: c.Fence}, []ArtifactRef{{Path: art}}, ""))
	must(f.store.Verify(bob, "demo", "t1", VerifyInput{Result: "pass", Checks: "built and ran"}))
	f.file("build.out", "v2 — rebuilt after verification")
	_, err := f.store.Claim(carol, "demo", "t2", 0)
	wantErr(t, err, ErrStaleEvidence)
	view := must(f.store.GetTask("demo", "t1"))
	if len(view.ArtifactDrift) != 1 {
		t.Fatalf("verified task should show drift: %+v", view)
	}
	dep := must(f.store.GetTask("demo", "t2"))
	if len(dep.Blockers) != 1 || !strings.Contains(dep.Blockers[0], "verified artifacts changed") {
		t.Fatalf("blockers: %+v", dep.Blockers)
	}
	b := must(f.store.Brief("demo", 0))
	if !hasIssue(b, "dependency-stale", "t2") {
		t.Fatalf("brief issues: %+v", b.Issues)
	}
	f.file("build.out", "v1")
	must(f.store.Claim(carol, "demo", "t2", 0))
}

func TestUnprovenReviewersNeedProjectConsent(t *testing.T) {
	f := newFixture(t)
	art := f.file("x.txt", "x")
	f.task("t")
	c := must(f.store.Claim(ada, "demo", "t1", 0))
	must(f.store.Submit(ada, "demo", "t1", OwnerProof{Fence: c.Fence}, []ArtifactRef{{Path: art}}, ""))
	claimed := Actor{Label: "mcp:reviewer"} // a self-declared label
	_, err := f.store.Verify(claimed, "demo", "t1", VerifyInput{Result: "pass", Checks: "fine"})
	wantErr(t, err, ErrForbidden)
	fd := must(f.store.AddFinding(ada, "demo", FindingSpec{Assertion: "x"}))
	_, err = f.store.JudgeFinding(claimed, "demo", fd.ID, FindingVerified, "agree", 0)
	wantErr(t, err, ErrForbidden)
	// The unproven author may still retract its own finding.
	own := must(f.store.AddFinding(claimed, "demo", FindingSpec{Assertion: "mine"}))
	must(f.store.JudgeFinding(claimed, "demo", own.ID, FindingRefuted, "I was wrong", 0))

	// A project created with consent records the verification as unproven.
	if _, err := f.store.CreateProject(lead, ProjectSpec{Name: "open", Goal: "g", Roots: []string{f.root}, AllowUnprovenReviewers: true}); err != nil {
		t.Fatal(err)
	}
	must(f.store.CreateTask(lead, "open", TaskSpec{Title: "t"}))
	c = must(f.store.Claim(ada, "open", "t1", 0))
	must(f.store.Submit(ada, "open", "t1", OwnerProof{Fence: c.Fence}, []ArtifactRef{{Path: art}}, ""))
	v := must(f.store.Verify(claimed, "open", "t1", VerifyInput{Result: "pass", Checks: "fine"}))
	if v.Verifications[0].ByVerified {
		t.Fatal("unproven verifier recorded as proven")
	}
	b := must(f.store.Brief("open", 0))
	if !b.Verified[0].Unproven || !strings.Contains(b.Text(), "identity unproven") {
		t.Fatalf("brief should mark the unproven verifier: %+v", b.Verified)
	}
}

func TestJudgmentSurvivesManyChecks(t *testing.T) {
	f := newFixture(t)
	src := f.file("s.txt", "s")
	fd := must(f.store.AddFinding(ada, "demo", FindingSpec{Assertion: "stable", Sources: []ArtifactRef{{Path: src}}}))
	must(f.store.JudgeFinding(bob, "demo", fd.ID, FindingVerified, "the decisive evidence", 0))
	for i := 0; i < maxListed+5; i++ {
		must(f.store.CheckFinding(carol, "demo", fd.ID))
	}
	view := must(f.store.GetFinding("demo", fd.ID))
	if len(view.Checks) != maxListed || view.Judgment == nil || view.Judgment.Evidence != "the decisive evidence" {
		t.Fatalf("judgment lost: checks=%d judgment=%+v", len(view.Checks), view.Judgment)
	}
	b := must(f.store.Brief("demo", 0))
	if b.Findings[0].Evidence != "the decisive evidence" {
		t.Fatalf("brief evidence %q", b.Findings[0].Evidence)
	}
	// After a revision the old judgment no longer speaks for the claim.
	must(f.store.ReviseFinding(ada, "demo", fd.ID, FindingSpec{Assertion: "stable, revised"}, 0))
	b = must(f.store.Brief("demo", 0))
	if len(b.Open) != 1 || b.Open[0].Evidence != "" {
		t.Fatalf("revised hypothesis shows old evidence: %+v", b.Open)
	}
}

func TestBriefByteBudget(t *testing.T) {
	f := newFixture(t)
	var refs []ArtifactRef
	for i := 0; i < 30; i++ {
		refs = append(refs, ArtifactRef{Path: f.file(fmt.Sprintf("deep/%s/file-%02d.txt", strings.Repeat("d", 120), i), "x")})
	}
	for i := 0; i < 100; i++ {
		must(f.store.AddFinding(ada, "demo", FindingSpec{Assertion: fmt.Sprintf("claim %d %s", i, strings.Repeat("words ", 40)), Sources: refs}))
	}
	b := must(f.store.Brief("demo", 100))
	data, _ := json.Marshal(b)
	if len(data) > MaxBriefBytes {
		t.Fatalf("brief is %d bytes, over %d", len(data), MaxBriefBytes)
	}
	if !b.Truncated || b.Items >= 100 || b.More == "" || b.Omit["hypotheses"] == 0 {
		t.Fatalf("truncation not explicit: truncated=%v items=%d omit=%v", b.Truncated, b.Items, b.Omit)
	}
	if b.Open[0].MoreSources != 30-briefArtifacts {
		t.Fatalf("sources not capped: %d", b.Open[0].MoreSources)
	}
	if !strings.Contains(b.Text(), "Continue with: bp work feed demo --after "+b.Cursor) {
		t.Fatal("truncated brief lost its cursor")
	}
}
