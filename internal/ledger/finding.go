package ledger

import (
	"fmt"
	"strings"
)

// Finding kinds and statuses.
//
// A finding starts as a hypothesis. An actor other than its author may
// mark it verified (or refuted) with evidence; the author may refute
// (retract) it. If a cited source's bytes change, the finding is stale:
// reads report the effective status from a fresh re-hash, and a check
// persists the stale status so feed readers see it. A stale finding
// returns to hypothesis only through a revision that re-cites the
// current sources.
//
// A negative finding records a failed experiment: what was tried, the
// conditions under which the failure applies, and the same evidence
// rules. It tells the next agent what not to repeat, and under which
// conditions the result would no longer hold.
const (
	KindFinding  = "finding"
	KindNegative = "negative"

	FindingHypothesis = "hypothesis"
	FindingVerified   = "verified"
	FindingRefuted    = "refuted"
	FindingStale      = "stale"

	maxFindings = 5000
)

// Finding is a shared claim with provenance.
type Finding struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Assertion   string         `json:"assertion"`
	Conditions  string         `json:"conditions,omitempty"`
	Author      string         `json:"author"`
	Status      string         `json:"status"`
	StaleReason string         `json:"staleReason,omitempty"`
	Task        string         `json:"task,omitempty"`
	Sources     []Artifact     `json:"sources,omitempty"`
	Checks      []FindingCheck `json:"checks,omitempty"`
	// Judgment is the latest verification or refutation of the current
	// revision, kept outside the bounded Checks list so freshness checks
	// never push the evidence behind a status out of the record.
	Judgment  *FindingCheck `json:"judgment,omitempty"`
	Revision  int           `json:"revision"`
	Version   int           `json:"version"`
	CreatedAt string        `json:"createdAt"`
	UpdatedAt string        `json:"updatedAt"`
}

// FindingCheck is one verification, refutation or freshness check, with
// the source digests it saw.
type FindingCheck struct {
	By         string     `json:"by"`
	ByVerified bool       `json:"byVerified"`
	Result     string     `json:"result"` // verified, refuted, stale, fresh
	Evidence   string     `json:"evidence,omitempty"`
	Saw        []Artifact `json:"saw,omitempty"`
	Revision   int        `json:"revision"`
	At         string     `json:"at"`
}

// FindingSpec records a new finding or revises one.
type FindingSpec struct {
	Kind       string        `json:"kind,omitempty"`
	Assertion  string        `json:"assertion"`
	Conditions string        `json:"conditions,omitempty"`
	Task       string        `json:"task,omitempty"`
	Sources    []ArtifactRef `json:"sources,omitempty"`
}

func findingEvent(f *Finding, op, note string) Event {
	return Event{Kind: "finding", ID: f.ID, Op: op, Version: f.Version, Note: note, Finding: f}
}

func (st *State) finding(id string) (*Finding, error) {
	f, ok := st.Findings[id]
	if !ok {
		return nil, fmt.Errorf("%w: finding %s in project %s", ErrNotFound, id, st.Project.Name)
	}
	return f, nil
}

func validFindingSpec(spec *FindingSpec) error {
	if spec.Kind == "" {
		spec.Kind = KindFinding
	}
	if spec.Kind != KindFinding && spec.Kind != KindNegative {
		return invalid("kind must be finding or negative")
	}
	if err := textField("assertion", spec.Assertion, 4096, true); err != nil {
		return err
	}
	if err := textField("conditions", spec.Conditions, 4096, spec.Kind == KindNegative); err != nil {
		if spec.Kind == KindNegative && strings.TrimSpace(spec.Conditions) == "" {
			return invalid("a negative result needs --conditions: when does this failure apply?")
		}
		return err
	}
	return nil
}

// AddFinding records a hypothesis with its sources hashed now.
func (s *Store) AddFinding(actor Actor, project string, spec FindingSpec) (Finding, error) {
	if err := validFindingSpec(&spec); err != nil {
		return Finding{}, err
	}
	var out Finding
	err := s.mutate(project, actor, func(st *State, ch *change) error {
		if err := requireState(st.Project, ProjectActive, ProjectPaused); err != nil {
			return err
		}
		if len(st.Findings) >= maxFindings {
			return invalid("project %s has %d findings, the limit", project, maxFindings)
		}
		if spec.Task != "" {
			if _, err := st.task(spec.Task); err != nil {
				return err
			}
		}
		sources, err := resolveArtifacts(st.Project.Roots, spec.Sources)
		if err != nil {
			return err
		}
		id := nextID("f", len(st.Findings), func(id string) bool { _, ok := st.Findings[id]; return ok })
		now := s.stamp()
		f := &Finding{ID: id, Kind: spec.Kind, Assertion: spec.Assertion, Conditions: spec.Conditions, Author: actor.Label,
			Status: FindingHypothesis, Task: spec.Task, Sources: sources, Revision: 1, Version: 1, CreatedAt: now, UpdatedAt: now}
		st.Findings[id] = f
		ch.add(findingEvent(f, "add", ""))
		out = *f
		return nil
	})
	return out, err
}

// ReviseFinding replaces the assertion, conditions and sources (re-hashed
// now) and returns the finding to hypothesis. Earlier checks stay in the
// record, tagged with the revision they judged. Only the author revises.
func (s *Store) ReviseFinding(actor Actor, project, id string, spec FindingSpec, expectVersion int) (Finding, error) {
	var out Finding
	err := s.mutate(project, actor, func(st *State, ch *change) error {
		f, err := st.finding(id)
		if err != nil {
			return err
		}
		if actor.Label != f.Author {
			return fmt.Errorf("%w: only %s (author) revises %s; record your own finding instead", ErrForbidden, f.Author, id)
		}
		if expectVersion > 0 && expectVersion != f.Version {
			return fmt.Errorf("%w: %s is at version %d, not %d", ErrConflict, id, f.Version, expectVersion)
		}
		if spec.Kind == "" {
			spec.Kind = f.Kind
		}
		if spec.Assertion == "" {
			spec.Assertion = f.Assertion
		}
		if spec.Conditions == "" {
			spec.Conditions = f.Conditions
		}
		if err := validFindingSpec(&spec); err != nil {
			return err
		}
		refs := spec.Sources
		if refs == nil {
			for _, a := range f.Sources {
				refs = append(refs, ArtifactRef{Path: a.Path, Revision: a.Revision})
			}
		}
		sources, err := resolveArtifacts(st.Project.Roots, refs)
		if err != nil {
			return err
		}
		f.Kind, f.Assertion, f.Conditions, f.Sources = spec.Kind, spec.Assertion, spec.Conditions, sources
		f.Status, f.StaleReason, f.Judgment = FindingHypothesis, "", nil
		f.Revision++
		f.Version++
		f.UpdatedAt = s.stamp()
		ch.add(findingEvent(f, "revise", fmt.Sprintf("revision %d", f.Revision)))
		out = *f
		return nil
	})
	return out, err
}

// JudgeFinding marks a finding verified or refuted with evidence. Verified
// needs an actor other than the author and sources whose bytes still
// match; the author may refute (retract) its own finding.
func (s *Store) JudgeFinding(actor Actor, project, id, result, evidence string, expectVersion int) (Finding, error) {
	if result != FindingVerified && result != FindingRefuted {
		return Finding{}, invalid("result must be verified or refuted")
	}
	if err := textField("evidence", evidence, 8192, true); err != nil {
		return Finding{}, err
	}
	var out Finding
	err := s.mutate(project, actor, func(st *State, ch *change) error {
		if err := requireState(st.Project, ProjectActive, ProjectPaused); err != nil {
			return err
		}
		f, err := st.finding(id)
		if err != nil {
			return err
		}
		if expectVersion > 0 && expectVersion != f.Version {
			return fmt.Errorf("%w: %s is at version %d, not %d", ErrConflict, id, f.Version, expectVersion)
		}
		if result == FindingVerified && actor.Label == f.Author {
			return fmt.Errorf("%w: %s wrote %s; verification must come from someone else", ErrForbidden, actor.Label, id)
		}
		if actor.Label != f.Author {
			// The author may always retract; judging someone else's
			// finding needs a proven identity.
			if err := requireReviewer(st.Project, actor, "judging "+id); err != nil {
				return err
			}
		}
		if f.Status == FindingStale {
			return fmt.Errorf("%w: %s is stale (%s); its author must revise it against the current sources first", ErrStaleEvidence, id, f.StaleReason)
		}
		if drift := checkArtifacts(st.Project.Roots, f.Sources); len(drift) > 0 && result == FindingVerified {
			return fmt.Errorf("%w: sources of %s changed since it was recorded: %s; run bp work finding check, then the author revises it", ErrStaleEvidence, id, driftText(drift))
		}
		check := FindingCheck{By: actor.Label, ByVerified: actor.Verified, Result: result, Evidence: evidence, Saw: f.Sources, Revision: f.Revision, At: s.stamp()}
		f.Checks = appendBounded(f.Checks, check)
		f.Judgment = &check
		f.Status = result
		f.Version++
		f.UpdatedAt = s.stamp()
		ch.add(findingEvent(f, result, ""))
		out = *f
		return nil
	})
	return out, err
}

// CheckResult is a freshness check's outcome.
type CheckResult struct {
	Finding Finding `json:"finding"`
	Fresh   bool    `json:"fresh"`
	Drift   []Drift `json:"drift,omitempty"`
}

// CheckFinding re-hashes a finding's sources. Drift marks it stale (an
// event, so feed readers see it). A fresh verified finding gets a recorded
// fresh check, so readers can see when it was last confirmed against its
// inputs; a fresh hypothesis or refuted finding changes nothing.
func (s *Store) CheckFinding(actor Actor, project, id string) (CheckResult, error) {
	var out CheckResult
	err := s.mutate(project, actor, func(st *State, ch *change) error {
		f, err := st.finding(id)
		if err != nil {
			return err
		}
		drift := checkArtifacts(st.Project.Roots, f.Sources)
		out.Drift = drift
		out.Fresh = len(drift) == 0
		switch {
		case len(drift) > 0 && f.Status != FindingStale:
			f.Checks = appendBounded(f.Checks, FindingCheck{By: actor.Label, ByVerified: actor.Verified, Result: FindingStale, Evidence: driftText(drift), Revision: f.Revision, At: s.stamp()})
			f.Status, f.StaleReason = FindingStale, driftText(drift)
			f.Version++
			f.UpdatedAt = s.stamp()
			ch.add(findingEvent(f, "stale", f.StaleReason))
		case len(drift) == 0 && f.Status == FindingVerified:
			f.Checks = appendBounded(f.Checks, FindingCheck{By: actor.Label, ByVerified: actor.Verified, Result: "fresh", Saw: f.Sources, Revision: f.Revision, At: s.stamp()})
			f.Version++
			f.UpdatedAt = s.stamp()
			ch.add(findingEvent(f, "fresh", ""))
		}
		out.Finding = *f
		return nil
	})
	return out, err
}

// FindingView is a finding as read now: the stored record plus its
// effective status from a fresh re-hash of its sources. A stored
// "verified" whose sources changed reads as effective "stale", so a reader
// never mistakes old evidence for a current validation.
type FindingView struct {
	Finding
	Effective string  `json:"effectiveStatus"`
	Drift     []Drift `json:"drift,omitempty"`
}

func (st *State) findingView(f *Finding) FindingView {
	v := FindingView{Finding: *f, Effective: f.Status}
	if f.Status != FindingStale && f.Status != FindingRefuted {
		if drift := checkArtifacts(st.Project.Roots, f.Sources); len(drift) > 0 {
			v.Effective, v.Drift = FindingStale, drift
		}
	}
	return v
}

// GetFinding returns one finding with its effective status.
func (s *Store) GetFinding(project, id string) (FindingView, error) {
	var out FindingView
	err := s.read(project, func(st *State) error {
		f, err := st.finding(id)
		if err != nil {
			return err
		}
		out = st.findingView(f)
		return nil
	})
	return out, err
}

// Findings lists findings in id order with effective status, optionally
// filtered by kind and effective status.
func (s *Store) Findings(project, kind, status string) ([]FindingView, error) {
	out := []FindingView{}
	err := s.read(project, func(st *State) error {
		for _, id := range st.findingIDs() {
			f := st.Findings[id]
			if kind != "" && f.Kind != kind {
				continue
			}
			v := st.findingView(f)
			if status == "" || v.Effective == status {
				out = append(out, v)
			}
		}
		return nil
	})
	return out, err
}

func (st *State) findingIDs() []string {
	ids := make([]string, 0, len(st.Findings))
	for id := range st.Findings {
		ids = append(ids, id)
	}
	sortIDs(ids)
	return ids
}

// FindingHistory returns every retained event of one finding, oldest
// first: its full provenance.
func (s *Store) FindingHistory(project, id string) ([]Event, error) {
	return s.entityHistory(project, "finding", id)
}

// TaskHistory returns every retained event of one task, oldest first.
func (s *Store) TaskHistory(project, id string) ([]Event, error) {
	return s.entityHistory(project, "task", id)
}

func (s *Store) entityHistory(project, kind, id string) ([]Event, error) {
	var out []Event
	err := s.read(project, func(st *State) error {
		var err error
		out, err = s.history(project, st, func(ev Event) bool { return ev.Kind == kind && ev.ID == id })
		return err
	})
	return out, err
}
