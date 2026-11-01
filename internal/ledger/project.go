package ledger

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Project states. Paused refuses new claims and new tasks; stopped also
// refuses submissions and verifications. Reads, recovery and admin
// changes keep working, so a halted project can still be inspected and
// handed over.
const (
	ProjectActive  = "active"
	ProjectPaused  = "paused"
	ProjectStopped = "stopped"
)

// Project is the unit of shared work: a goal, a lead and the directories
// whose files may be cited as artifacts or evidence.
type Project struct {
	Name   string   `json:"name"`
	Goal   string   `json:"goal"`
	Lead   string   `json:"lead"`
	State  string   `json:"state"`
	Reason string   `json:"reason,omitempty"`
	Roots  []string `json:"roots"`
	// AllowUnprovenReviewers lets an identity bp did not prove (a label
	// self-declared over MCP or HTTP) verify tasks and judge findings.
	// Off by default: two different declared names are not evidence of
	// two independent reviewers.
	AllowUnprovenReviewers bool   `json:"allowUnprovenReviewers,omitempty"`
	Epoch                  string `json:"epoch"`
	Version                int    `json:"version"`
	CreatedAt              string `json:"createdAt"`
	UpdatedAt              string `json:"updatedAt"`
}

// ProjectSpec creates a project.
type ProjectSpec struct {
	Name                   string   `json:"name"`
	Goal                   string   `json:"goal"`
	Roots                  []string `json:"roots,omitempty"`
	AllowUnprovenReviewers bool     `json:"allowUnprovenReviewers,omitempty"`
}

// CreateProject makes a project led by actor. Roots are directories whose
// files may be cited; each must exist and is stored resolved (symlinks
// followed), so containment checks compare real paths.
func (s *Store) CreateProject(actor Actor, spec ProjectSpec) (Project, error) {
	name, goal, roots := spec.Name, spec.Goal, spec.Roots
	if err := validActor(actor); err != nil {
		return Project{}, err
	}
	if err := textField("goal", goal, 4096, true); err != nil {
		return Project{}, err
	}
	resolved, err := resolveRoots(roots)
	if err != nil {
		return Project{}, err
	}
	var out Project
	err = s.lock(name, func() error {
		if _, err := s.load(name); err == nil {
			return fmt.Errorf("%w: project %s already exists", ErrConflict, name)
		}
		// A leftover log from a project whose snapshot was removed would
		// replay into the new one; refuse instead of mixing histories.
		if info, err := os.Stat(filepath.Join(s.projectDir(name), "events.jsonl")); err == nil && info.Size() > 0 {
			return fmt.Errorf("%w: project %s has an unreadable history; move %s aside first", ErrConflict, name, s.projectDir(name))
		}
		now := s.now().Format(time.RFC3339Nano)
		st := newState()
		st.Project = Project{Name: name, Goal: goal, Lead: actor.Label, State: ProjectActive, Roots: resolved,
			AllowUnprovenReviewers: spec.AllowUnprovenReviewers,
			Epoch:                  randomID(), Version: 1, CreatedAt: now, UpdatedAt: now}
		ch := &change{}
		ch.add(Event{Kind: "project", ID: name, Op: "create", Version: 1, Project: &st.Project})
		out = st.Project
		return s.commit(name, st, ch, actor)
	})
	return out, err
}

func resolveRoots(roots []string) ([]string, error) {
	out := []string{}
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			return nil, invalid("root %q must be an absolute path", root)
		}
		real, err := filepath.EvalSymlinks(filepath.Clean(root))
		if err != nil {
			return nil, invalid("root %q: %v", root, err)
		}
		info, err := os.Stat(real)
		if err != nil || !info.IsDir() {
			return nil, invalid("root %q is not a directory", root)
		}
		if real == "/" {
			return nil, invalid("the filesystem root cannot be a project root")
		}
		out = append(out, real)
	}
	sort.Strings(out)
	return dedupe(out), nil
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, v := range sorted {
		if i == 0 || v != sorted[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// Projects lists project names that have a readable state.
func (s *Store) Projects() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		if e.IsDir() && validProject(e.Name()) == nil {
			if _, err := os.Stat(filepath.Join(s.Dir, e.Name(), "events.jsonl")); err == nil {
				names = append(names, e.Name())
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

// GetProject returns the project record.
func (s *Store) GetProject(name string) (Project, error) {
	var out Project
	err := s.read(name, func(st *State) error {
		out = st.Project
		return nil
	})
	return out, err
}

// SetProjectState pauses, stops or resumes a project. Only the lead may.
func (s *Store) SetProjectState(actor Actor, name, state, reason string) (Project, error) {
	switch state {
	case ProjectActive, ProjectPaused, ProjectStopped:
	default:
		return Project{}, invalid("project state must be active, paused or stopped")
	}
	if err := textField("reason", reason, 1024, false); err != nil {
		return Project{}, err
	}
	var out Project
	err := s.mutate(name, actor, func(st *State, ch *change) error {
		p := &st.Project
		if actor.Label != p.Lead {
			return fmt.Errorf("%w: only the project lead (%s) changes the project state", ErrForbidden, p.Lead)
		}
		if p.State == state && p.Reason == reason {
			out = *p
			return nil
		}
		p.State, p.Reason = state, reason
		s.touchProject(p)
		ch.add(Event{Kind: "project", ID: name, Op: "state", Version: p.Version, Note: state, Project: p})
		out = *p
		return nil
	})
	return out, err
}

// AddRoot lets the lead cite files from another directory.
func (s *Store) AddRoot(actor Actor, name, root string) (Project, error) {
	resolved, err := resolveRoots([]string{root})
	if err != nil {
		return Project{}, err
	}
	var out Project
	err = s.mutate(name, actor, func(st *State, ch *change) error {
		p := &st.Project
		if actor.Label != p.Lead {
			return fmt.Errorf("%w: only the project lead (%s) adds roots", ErrForbidden, p.Lead)
		}
		for _, r := range p.Roots {
			if r == resolved[0] {
				out = *p
				return nil
			}
		}
		p.Roots = append(p.Roots, resolved[0])
		sort.Strings(p.Roots)
		s.touchProject(p)
		ch.add(Event{Kind: "project", ID: name, Op: "root", Version: p.Version, Note: resolved[0], Project: p})
		out = *p
		return nil
	})
	return out, err
}

func (s *Store) touchProject(p *Project) {
	p.Version++
	p.UpdatedAt = s.now().Format(time.RFC3339Nano)
}

// requireReviewer refuses a reviewer identity bp did not prove, unless the
// project allows it.
func requireReviewer(p Project, actor Actor, what string) error {
	if actor.Verified || p.AllowUnprovenReviewers {
		return nil
	}
	return fmt.Errorf("%w: %s needs an identity bp proved (run it from a bp agent's terminal); %s is self-declared, and project %s does not allow unproven reviewers", ErrForbidden, what, actor.Label, p.Name)
}

func requireState(p Project, allowed ...string) error {
	for _, a := range allowed {
		if p.State == a {
			return nil
		}
	}
	reason := ""
	if p.Reason != "" {
		reason = " (" + p.Reason + ")"
	}
	return fmt.Errorf("%w: project %s is %s%s; the lead resumes it with bp work project resume", ErrHalted, p.Name, p.State, reason)
}
