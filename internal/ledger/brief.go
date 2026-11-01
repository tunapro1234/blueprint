package ledger

import (
	"encoding/json"
	"fmt"
	"strings"
)

// A Brief is a deterministic, bounded digest of a project's ledger state
// for an agent that joins or resumes: the goal, who holds what, what is
// verified (with its evidence), what is stale or refuted, which failed
// experiments to avoid, what is blocked, and what to do next. It is
// computed from ledger records only (no model writes it), so equal state
// and clock give an equal brief. It names the sequence it was built at
// and the cursor to continue the feed from.

const (
	defaultBriefItems = 20
	maxBriefItems     = 100
	briefTextLimit    = 280
	briefArtifacts    = 5
	// MaxBriefBytes bounds a brief's JSON. When the item cap alone would
	// exceed it, the per-list cap is halved until it fits, and the brief
	// says it was truncated.
	MaxBriefBytes = 48 << 10
)

// Brief is the handoff bundle.
type Brief struct {
	Project   Project        `json:"project"`
	AsOf      string         `json:"asOf"`
	Seq       int64          `json:"seq"`
	Cursor    string         `json:"cursor"`
	Authority string         `json:"authority"`
	Counts    map[string]int `json:"counts"`

	Held      []BriefTask `json:"held"`
	Claimable []BriefTask `json:"claimable"`
	Review    []BriefTask `json:"awaitingVerification"`
	Blocked   []BriefTask `json:"blocked"`
	Verified  []BriefTask `json:"verified"`
	Failed    []BriefTask `json:"failed"`

	Findings  []BriefFinding `json:"verifiedFindings"`
	Open      []BriefFinding `json:"hypotheses"`
	Stale     []BriefFinding `json:"staleOrRefuted"`
	Negatives []BriefFinding `json:"negativeResults"`

	Issues []Issue   `json:"issues"`
	Next   []string  `json:"nextActions"`
	Omit   BriefOmit `json:"omitted"`
	// Items is the per-list cap actually used; Truncated is true when the
	// byte budget forced it below the requested cap.
	Items     int    `json:"items"`
	Truncated bool   `json:"truncated,omitempty"`
	More      string `json:"more,omitempty"`
}

// BriefOmit counts items cut by the bound, per list.
type BriefOmit map[string]int

// BriefTask is a task line.
type BriefTask struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	State         string     `json:"state"`
	Owner         string     `json:"owner,omitempty"`
	Fence         int64      `json:"fence,omitempty"`
	LeaseUntil    string     `json:"leaseUntil,omitempty"`
	Expired       bool       `json:"leaseExpired,omitempty"`
	Why           string     `json:"why,omitempty"`
	Latest        string     `json:"latest,omitempty"`
	Artifacts     []Artifact `json:"artifacts,omitempty"`
	MoreArtifacts int        `json:"moreArtifacts,omitempty"`
	VerifiedBy    string     `json:"verifiedBy,omitempty"`
	// Unproven marks a verification whose verifier label was
	// self-declared rather than proven by bp.
	Unproven bool `json:"verifierUnproven,omitempty"`
}

// BriefFinding is a finding line with its effective status.
type BriefFinding struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Assertion   string     `json:"assertion"`
	Conditions  string     `json:"conditions,omitempty"`
	Author      string     `json:"author"`
	Status      string     `json:"status"`
	Stored      string     `json:"storedStatus"`
	Why         string     `json:"why,omitempty"`
	Evidence    string     `json:"evidence,omitempty"`
	CheckedBy   string     `json:"checkedBy,omitempty"`
	Unproven    bool       `json:"checkerUnproven,omitempty"`
	Sources     []Artifact `json:"sources,omitempty"`
	MoreSources int        `json:"moreSources,omitempty"`
}

// Issue is one doctor finding about the project's health.
type Issue struct {
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Detail string `json:"detail"`
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= briefTextLimit {
		return s
	}
	cut := briefTextLimit
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// Brief builds the handoff bundle with at most items entries per list.
func (s *Store) Brief(project string, items int) (Brief, error) {
	if items <= 0 {
		items = defaultBriefItems
	}
	if items > maxBriefItems {
		items = maxBriefItems
	}
	var out Brief
	err := s.read(project, func(st *State) error {
		requested := items
		for {
			out = s.brief(st, items)
			data, err := json.Marshal(out)
			if err != nil {
				return err
			}
			if len(data) <= MaxBriefBytes || items == 1 {
				break
			}
			items /= 2
		}
		if items < requested {
			out.Truncated = true
		}
		if out.Truncated || len(out.Omit) > 0 {
			out.More = "lists are capped; full records: bp work task list " + project + ", bp work finding list " + project + ", bp work feed " + project
		}
		return nil
	})
	return out, err
}

func capArtifacts(list []Artifact) ([]Artifact, int) {
	if len(list) <= briefArtifacts {
		return list, 0
	}
	return list[:briefArtifacts], len(list) - briefArtifacts
}

// Doctor returns only the health issues.
func (s *Store) Doctor(project string) ([]Issue, error) {
	b, err := s.Brief(project, maxBriefItems)
	return b.Issues, err
}

func (s *Store) brief(st *State, items int) Brief {
	b := Brief{Items: items, Project: st.Project, AsOf: s.stamp(), Seq: st.Seq, Cursor: formatCursor(st.Project.Epoch, st.Seq),
		Authority: "computed from ledger records at seq " + fmt.Sprint(st.Seq) + "; not a model summary",
		Counts:    map[string]int{}, Omit: BriefOmit{}, Issues: []Issue{}, Next: []string{}}
	b.Project.Roots = append([]string(nil), st.Project.Roots...)
	add := func(name string, list *[]BriefTask, t BriefTask) {
		if len(*list) < items {
			*list = append(*list, t)
		} else {
			b.Omit[name]++
		}
	}
	addF := func(name string, list *[]BriefFinding, f BriefFinding) {
		if len(*list) < items {
			*list = append(*list, f)
		} else {
			b.Omit[name]++
		}
	}
	issue := func(kind, id, detail string) {
		if len(b.Issues) < maxBriefItems {
			b.Issues = append(b.Issues, Issue{Kind: kind, ID: id, Detail: clip(detail)})
		} else {
			b.Omit["issues"]++
		}
	}
	if st.Project.State != ProjectActive {
		issue("project-"+st.Project.State, st.Project.Name, "project is "+st.Project.State+" "+st.Project.Reason)
	}

	for _, id := range st.taskIDs() {
		t := st.Tasks[id]
		b.Counts["task."+t.State]++
		line := BriefTask{ID: t.ID, Title: clip(t.Title), State: t.State, Owner: t.Owner, LeaseUntil: t.LeaseUntil}
		if t.Owner != "" {
			line.Fence = t.Fence
		}
		if n := len(t.Checkpoints); n > 0 {
			line.Latest = clip(t.Checkpoints[n-1].Note)
		}
		switch t.State {
		case TaskPending:
			failed, waiting, stale := st.depStatus(t)
			switch {
			case len(failed) == 0 && len(waiting) == 0 && len(stale) > 0:
				line.Why = "prerequisite changed after verification: " + strings.Join(stale, ", ")
				add("blocked", &b.Blocked, line)
				issue("dependency-stale", t.ID, t.ID+" cannot start: "+strings.Join(stale, ", "))
			case len(failed) > 0:
				line.Why = "dependency failed: " + strings.Join(failed, ", ")
				add("blocked", &b.Blocked, line)
				issue("dependency-failed", t.ID, t.ID+" cannot start: "+strings.Join(failed, ", ")+"; the lead reopens or cancels")
			case len(waiting) > 0:
				line.Why = "waiting on " + strings.Join(waiting, ", ")
				add("blocked", &b.Blocked, line)
			default:
				add("claimable", &b.Claimable, line)
			}
		case TaskClaimed, TaskWorking, TaskBlocked:
			if s.leaseExpired(t) {
				line.Expired = true
				issue("lease-expired", t.ID, fmt.Sprintf("%s held by %s, lease ended %s; bp work task recover %s %s", t.ID, t.Owner, t.LeaseUntil, st.Project.Name, t.ID))
			}
			if t.State == TaskBlocked {
				line.Why = clip(t.BlockedReason)
				add("blocked", &b.Blocked, line)
				issue("task-blocked", t.ID, t.ID+" blocked by its owner: "+t.BlockedReason)
			} else {
				add("held", &b.Held, line)
			}
		case TaskSubmitted:
			sub := t.Submissions[len(t.Submissions)-1]
			line.Artifacts, line.MoreArtifacts = capArtifacts(sub.Artifacts)
			line.Why = fmt.Sprintf("submission %d by %s", sub.N, sub.By)
			if t.Verifier != "" {
				line.Why += "; verifier " + t.Verifier
			}
			if drift := checkArtifacts(st.Project.Roots, sub.Artifacts); len(drift) > 0 {
				issue("submission-changed", t.ID, t.ID+" artifacts changed after submission: "+driftText(drift))
			}
			add("awaitingVerification", &b.Review, line)
		case TaskVerified:
			if n := len(t.Verifications); n > 0 {
				line.VerifiedBy = t.Verifications[n-1].By
				line.Unproven = !t.Verifications[n-1].ByVerified
				line.Why = clip(t.Verifications[n-1].Checks)
			}
			if n := len(t.Submissions); n > 0 {
				line.Artifacts, line.MoreArtifacts = capArtifacts(t.Submissions[n-1].Artifacts)
				if drift := checkArtifacts(st.Project.Roots, t.Submissions[n-1].Artifacts); len(drift) > 0 {
					line.Why = "ARTIFACT CHANGED SINCE VERIFICATION: " + driftText(drift)
					issue("verified-artifact-changed", t.ID, t.ID+" was verified, but its artifacts changed: "+driftText(drift))
				}
			}
			add("verified", &b.Verified, line)
		case TaskFailed:
			if n := len(t.Verifications); n > 0 {
				line.Why = clip(t.Verifications[n-1].Checks)
			}
			add("failed", &b.Failed, line)
		}
	}
	if cycle := st.findCycle(); cycle != nil {
		issue("dependency-cycle", cycle[0], "dependency cycle "+strings.Join(cycle, " -> "))
	}

	for _, id := range st.findingIDs() {
		f := st.Findings[id]
		status, why := f.Status, f.StaleReason
		if status != FindingStale && status != FindingRefuted {
			if drift := checkArtifacts(st.Project.Roots, f.Sources); len(drift) > 0 {
				status, why = FindingStale, driftText(drift)
				issue("finding-stale", f.ID, f.ID+" sources changed: "+why+"; bp work finding check "+st.Project.Name+" "+f.ID)
			}
		}
		b.Counts["finding."+status]++
		line := BriefFinding{ID: f.ID, Kind: f.Kind, Assertion: clip(f.Assertion), Conditions: clip(f.Conditions), Author: f.Author,
			Status: status, Stored: f.Status, Why: clip(why)}
		line.Sources, line.MoreSources = capArtifacts(f.Sources)
		if c := f.Judgment; c != nil && c.Revision == f.Revision {
			line.Evidence, line.CheckedBy, line.Unproven = clip(c.Evidence), c.By, !c.ByVerified
		}
		switch {
		case status == FindingStale || status == FindingRefuted:
			addF("staleOrRefuted", &b.Stale, line)
		case f.Kind == KindNegative:
			addF("negativeResults", &b.Negatives, line)
		case status == FindingVerified:
			addF("verifiedFindings", &b.Findings, line)
		default:
			addF("hypotheses", &b.Open, line)
		}
	}

	// Next actions, in a fixed order.
	for _, t := range b.Held {
		if t.Expired {
			b.Next = append(b.Next, fmt.Sprintf("recover %s (owner %s's lease expired)", t.ID, t.Owner))
		}
	}
	for _, t := range b.Review {
		b.Next = append(b.Next, fmt.Sprintf("verify %s (%s) — independent of its submitter", t.ID, t.Why))
	}
	for _, t := range b.Claimable {
		b.Next = append(b.Next, fmt.Sprintf("claim %s: %s", t.ID, t.Title))
	}
	for _, f := range b.Open {
		b.Next = append(b.Next, fmt.Sprintf("verify or refute finding %s", f.ID))
	}
	for _, i := range b.Issues {
		if i.Kind == "finding-stale" || i.Kind == "dependency-failed" {
			b.Next = append(b.Next, i.Detail)
		}
	}
	if len(b.Next) > items {
		b.Omit["nextActions"] = len(b.Next) - items
		b.Next = b.Next[:items]
	}
	for k, v := range b.Omit {
		if v == 0 {
			delete(b.Omit, k)
		}
	}
	return b
}

// findCycle returns one dependency cycle, or nil. AddDep refuses cycles;
// this guards state written by hand or by a future bug.
func (st *State) findCycle() []string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var found []string
	var visit func(id string) bool
	visit = func(id string) bool {
		color[id] = grey
		stack = append(stack, id)
		t := st.Tasks[id]
		if t != nil {
			for _, d := range t.Deps {
				switch color[d] {
				case grey:
					for i, s := range stack {
						if s == d {
							found = append(append([]string(nil), stack[i:]...), d)
							return true
						}
					}
				case white:
					if st.Tasks[d] != nil && visit(d) {
						return true
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return false
	}
	for _, id := range st.taskIDs() {
		if color[id] == white && visit(id) {
			return found
		}
	}
	return nil
}

// Text renders a brief for a terminal or a prompt.
func (b Brief) Text() string {
	var w strings.Builder
	p := b.Project
	fmt.Fprintf(&w, "# %s — %s\n", p.Name, clip(p.Goal))
	fmt.Fprintf(&w, "state %s · lead %s · seq %d · cursor %s · as of %s\n", p.State, p.Lead, b.Seq, b.Cursor, b.AsOf)
	fmt.Fprintf(&w, "(%s)\n", b.Authority)
	section := func(title string, n int) bool {
		if n == 0 {
			return false
		}
		fmt.Fprintf(&w, "\n## %s\n", title)
		return true
	}
	tasks := func(title, key string, list []BriefTask) {
		if !section(title, len(list)) {
			return
		}
		for _, t := range list {
			line := fmt.Sprintf("- %s [%s] %s", t.ID, t.State, t.Title)
			if t.Owner != "" {
				line += fmt.Sprintf(" — owner %s (fence %d", t.Owner, t.Fence)
				if t.LeaseUntil != "" {
					line += ", lease until " + t.LeaseUntil
				}
				if t.Expired {
					line += ", EXPIRED"
				}
				line += ")"
			}
			if t.VerifiedBy != "" {
				line += " — verified by " + t.VerifiedBy
				if t.Unproven {
					line += " (identity unproven)"
				}
			}
			if t.Why != "" {
				line += " — " + t.Why
			}
			if t.Latest != "" {
				line += " — last checkpoint: " + t.Latest
			}
			fmt.Fprintln(&w, line)
			for _, a := range t.Artifacts {
				fmt.Fprintf(&w, "    %s %s%s\n", a.Digest, a.Path, revisionText(a.Revision))
			}
			if t.MoreArtifacts > 0 {
				fmt.Fprintf(&w, "    … %d more artifacts (bp work task show %s %s)\n", t.MoreArtifacts, p.Name, t.ID)
			}
		}
		if n := b.Omit[key]; n > 0 {
			fmt.Fprintf(&w, "- … %d more\n", n)
		}
	}
	findings := func(title, key string, list []BriefFinding) {
		if !section(title, len(list)) {
			return
		}
		for _, f := range list {
			line := fmt.Sprintf("- %s [%s] %s — by %s", f.ID, f.Status, f.Assertion, f.Author)
			if f.Conditions != "" {
				line += " — applies when: " + f.Conditions
			}
			if f.Evidence != "" {
				who := f.CheckedBy
				if f.Unproven {
					who += ", identity unproven"
				}
				line += fmt.Sprintf(" — evidence (%s): %s", who, f.Evidence)
			}
			if f.Why != "" {
				line += " — " + f.Why
			}
			fmt.Fprintln(&w, line)
			for _, a := range f.Sources {
				fmt.Fprintf(&w, "    %s %s%s\n", a.Digest, a.Path, revisionText(a.Revision))
			}
			if f.MoreSources > 0 {
				fmt.Fprintf(&w, "    … %d more sources (bp work finding show %s %s)\n", f.MoreSources, p.Name, f.ID)
			}
		}
		if n := b.Omit[key]; n > 0 {
			fmt.Fprintf(&w, "- … %d more\n", n)
		}
	}
	tasks("Held", "held", b.Held)
	tasks("Awaiting verification", "awaitingVerification", b.Review)
	tasks("Blocked", "blocked", b.Blocked)
	tasks("Claimable", "claimable", b.Claimable)
	tasks("Verified", "verified", b.Verified)
	tasks("Failed", "failed", b.Failed)
	findings("Verified findings", "verifiedFindings", b.Findings)
	findings("Hypotheses", "hypotheses", b.Open)
	findings("Stale or refuted", "staleOrRefuted", b.Stale)
	findings("Failed experiments (avoid repeating)", "negativeResults", b.Negatives)
	if section("Issues", len(b.Issues)) {
		for _, i := range b.Issues {
			fmt.Fprintf(&w, "- %s %s: %s\n", i.Kind, i.ID, i.Detail)
		}
	}
	if section("Next actions", len(b.Next)) {
		for _, n := range b.Next {
			fmt.Fprintf(&w, "- %s\n", n)
		}
	}
	if b.More != "" {
		fmt.Fprintf(&w, "\n(%s)\n", b.More)
	}
	fmt.Fprintf(&w, "\nContinue with: bp work feed %s --after %s\n", p.Name, b.Cursor)
	return w.String()
}

func revisionText(r string) string {
	if r == "" {
		return ""
	}
	return " @" + r
}
