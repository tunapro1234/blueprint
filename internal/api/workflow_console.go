package api

// workflow_console.go serves the fleet console's workflow surface
// (docs/fleet-console.md, "Workflows — WS4"): list the saved workflow
// definitions, view one (its definition, its units and its recent runs), and
// clone one under a new name so a saved workflow is reusable. It is reads and a
// copy only; it never starts or stops a run (run control is out of scope for
// v1), never delivers a message and never types into a pane, so it cannot become
// a way past the turn.
//
// The handlers are registered through RegisterWorkflowConsole so the integrator
// wires them onto the /v1 mux without this file touching http.go. Mount them
// behind the same loopback + token auth as the rest of /v1.
//
// Honesty: status and timestamps are copied verbatim from the workflow store.
// Unknown stays unknown: a workflow with no bundled sample units reports an
// empty unit list, and a run whose timestamp is zero omits it rather than
// guessing one. Clone fails closed: it refuses an invalid name and never
// clobbers an existing workflow (409 ABORTED on collision).

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"blueprint/internal/workflow"
)

// WorkflowConsoleDeps are the dependencies the workflow endpoints need. The
// integrator builds it (NewWorkflowConsoleDeps for production); tests inject
// stubs so no real workflow store, tmux or network is touched.
type WorkflowConsoleDeps struct {
	// List returns the saved workflow definitions (workflow.Store.List).
	List func() ([]workflow.Definition, error)
	// Get resolves one saved workflow to its definition and, best-effort, the
	// units bundled with it (a saved workflow may carry a sample units file).
	// When no units can be loaded it returns an empty slice, never a guess. A
	// missing workflow returns an error wrapping os.ErrNotExist.
	Get func(name string) (workflow.Definition, []workflow.Unit, error)
	// RecentRuns returns up to limit of the workflow's runs, newest first. The
	// runs are copied verbatim from the store (status and timestamps honest).
	RecentRuns func(name string, limit int) ([]workflow.Run, error)
	// Clone copies the saved workflow from under the new name to. It fails
	// closed: it returns an error wrapping errWfConsoleExists if to already
	// exists, os.ErrNotExist if from does not, or errWfConsoleInvalid if the
	// copied definition is not valid under the new name.
	Clone func(from, to string) error
}

// wfRecentRunLimit bounds how many recent runs a workflow detail returns.
const wfRecentRunLimit = 10

// Clone failure sentinels, so the transport maps each to the right status.
var (
	errWfConsoleExists  = errors.New("workflow already exists")
	errWfConsoleInvalid = errors.New("invalid workflow definition")
)

// wfConsoleNamePattern mirrors the workflow package's own name rule (lowercase
// letters, digits and interior hyphens). It is duplicated here rather than
// exported from internal/workflow so the console can reject a bad name, and a
// path traversal attempt, before it ever reaches the store (fail closed).
var wfConsoleNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// wfValidName reports whether name is a valid, bounded workflow name. It rejects
// empty names, over-long names and anything with a path separator or dot, so a
// traversal like "../other" can never reach the store.
func wfValidName(name string) bool {
	return name != "" && len(name) <= 64 && wfConsoleNamePattern.MatchString(name)
}

// NewWorkflowConsoleDeps wires the production workflow dependencies over a
// workflow.Store at stateDir. List, Get and RecentRuns read the store; Clone
// copies a saved workflow under a new name without clobbering an existing one.
func NewWorkflowConsoleDeps(stateDir string) WorkflowConsoleDeps {
	store := workflow.NewStore(stateDir)
	return WorkflowConsoleDeps{
		List: store.List,
		Get: func(name string) (workflow.Definition, []workflow.Unit, error) {
			path, def, err := store.ResolveWorkflow(name)
			if err != nil {
				return workflow.Definition{}, nil, err
			}
			// A saved workflow may bundle a sample units file in its own
			// directory; if it does not, units stay unknown (empty), not guessed.
			units, uerr := workflow.LoadUnits(def, path)
			if uerr != nil {
				units = nil
			}
			return def, units, nil
		},
		RecentRuns: func(name string, limit int) ([]workflow.Run, error) {
			all, err := store.Runs()
			if err != nil {
				return nil, err
			}
			matched := make([]workflow.Run, 0, len(all))
			for _, run := range all {
				if run.Workflow == name {
					matched = append(matched, run)
				}
			}
			// Store.Runs is oldest-first; "recent" is newest-first.
			sort.Slice(matched, func(i, j int) bool { return matched[i].CreatedAt.After(matched[j].CreatedAt) })
			if limit > 0 && len(matched) > limit {
				matched = matched[:limit]
			}
			return matched, nil
		},
		Clone: func(from, to string) error {
			return wfCloneWorkflow(store, from, to)
		},
	}
}

// RegisterWorkflowConsole mounts the workflow endpoints on mux. The caller
// supplies an already-authenticated mux; these handlers add no auth of their own.
func RegisterWorkflowConsole(mux *http.ServeMux, deps WorkflowConsoleDeps) {
	mux.HandleFunc("GET /v1/workflows", deps.handleList)
	mux.HandleFunc("GET /v1/workflows/{name}", deps.handleGet)
	mux.HandleFunc("POST /v1/workflows/{name}/clone", deps.handleClone)
}

// --- GET /v1/workflows ---

// wfSummaryJSON is one saved workflow as the list returns it.
type wfSummaryJSON struct {
	Name        string `json:"name"`
	Version     int    `json:"version"`
	Description string `json:"description,omitempty"`
}

func (d WorkflowConsoleDeps) handleList(w http.ResponseWriter, r *http.Request) {
	if d.List == nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "workflow store not configured", "")
		return
	}
	definitions, err := d.List()
	if err != nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "list workflows: "+err.Error(), "")
		return
	}
	workflows := make([]wfSummaryJSON, 0, len(definitions))
	for _, def := range definitions {
		workflows = append(workflows, wfSummaryJSON{Name: def.Name, Version: def.Version, Description: def.Description})
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"workflows": workflows})
}

// --- GET /v1/workflows/{name} ---

// wfRunJSON is one workflow run as the detail view returns it: the status and
// timestamps copied verbatim from the store, with a zero timestamp omitted
// rather than rendered as an epoch.
type wfRunJSON struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

func wfRunJSONOf(run workflow.Run) wfRunJSON {
	out := wfRunJSON{ID: run.ID, Status: run.Status}
	if !run.CreatedAt.IsZero() {
		out.CreatedAt = run.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !run.UpdatedAt.IsZero() {
		out.UpdatedAt = run.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func (d WorkflowConsoleDeps) handleGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !wfValidName(name) {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "workflow name must be lowercase letters, digits and interior hyphens", "")
		return
	}
	if d.Get == nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "workflow store not configured", "")
		return
	}
	def, units, err := d.Get(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeStatusError(w, http.StatusNotFound, "NOT_FOUND", "no such workflow", "")
			return
		}
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "read workflow: "+err.Error(), "")
		return
	}
	runs := []wfRunJSON{}
	if d.RecentRuns != nil {
		recent, rerr := d.RecentRuns(name, wfRecentRunLimit)
		if rerr != nil {
			writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "read workflow runs: "+rerr.Error(), "")
			return
		}
		// The dependency bounds the slice, but cap defensively so the view is
		// bounded regardless of the source.
		if len(recent) > wfRecentRunLimit {
			recent = recent[:wfRecentRunLimit]
		}
		for _, run := range recent {
			runs = append(runs, wfRunJSONOf(run))
		}
	}
	if units == nil {
		units = []workflow.Unit{}
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"definition": def, "units": units, "recentRuns": runs})
}

// --- POST /v1/workflows/{name}/clone ---

func (d WorkflowConsoleDeps) handleClone(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !wfValidName(name) {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "workflow name must be lowercase letters, digits and interior hyphens", "")
		return
	}
	var body struct {
		To string `json:"to"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), "")
		return
	}
	if !wfValidName(body.To) {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "to must be a valid workflow name: lowercase letters, digits and interior hyphens", "")
		return
	}
	if body.To == name {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "clone target must differ from the source workflow", "")
		return
	}
	if d.Clone == nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "workflow store not configured", "")
		return
	}
	switch err := d.Clone(name, body.To); {
	case err == nil:
		writeJSONResponse(w, http.StatusOK, map[string]any{"from": name, "to": body.To})
	case errors.Is(err, errWfConsoleExists):
		writeStatusError(w, http.StatusConflict, "ABORTED", "workflow "+body.To+" already exists", "")
	case errors.Is(err, os.ErrNotExist):
		writeStatusError(w, http.StatusNotFound, "NOT_FOUND", "no such workflow", "")
	case errors.Is(err, errWfConsoleInvalid):
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), "")
	default:
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "clone workflow: "+err.Error(), "")
	}
}

// wfCloneWorkflow copies the saved workflow from under the new name to. It
// resolves the source, refuses to clobber an existing target (fail closed),
// stages a copy with the definition's name rewritten to the target, validates
// it, and installs it through Store.Add. Add is called with replace=false, but
// the existence pre-check means a normal collision is reported as
// errWfConsoleExists (409) rather than Add's generic "already exists" error.
func wfCloneWorkflow(store *workflow.Store, from, to string) error {
	srcPath, _, err := store.ResolveWorkflow(from)
	if err != nil {
		return err // a missing source wraps os.ErrNotExist -> 404
	}
	// Never clobber an existing workflow.
	if _, statErr := os.Stat(store.WorkflowPath(to)); statErr == nil {
		return fmt.Errorf("%w: %s", errWfConsoleExists, to)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	tmp, err := os.MkdirTemp("", "bp-wf-clone-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := wfCopyTree(srcPath, tmp); err != nil {
		return err
	}
	if err := wfRewriteName(filepath.Join(tmp, "workflow.yaml"), to); err != nil {
		return fmt.Errorf("%w: %v", errWfConsoleInvalid, err)
	}
	def, err := workflow.ReadDefinition(tmp)
	if err != nil {
		return fmt.Errorf("%w: %v", errWfConsoleInvalid, err)
	}
	if def.Name != to {
		return fmt.Errorf("%w: cloned workflow is named %q, not %q", errWfConsoleInvalid, def.Name, to)
	}
	if _, _, err := store.Add(tmp, false); err != nil {
		return err
	}
	return nil
}

// wfRewriteName replaces the top-level name field in a workflow.yaml with name.
// The workflow name is a top-level key, so it is the first line that begins at
// column zero with "name:". Rewriting it textually avoids a YAML dependency; the
// result is re-parsed and re-validated by ReadDefinition before it is installed,
// so a bad rewrite fails closed rather than installing a malformed workflow.
func wfRewriteName(path, name string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(data))
	replaced := false
	for i, line := range lines {
		if hasYAMLKeyPrefix(line, "name") {
			lines[i] = "name: " + name
			replaced = true
			break
		}
	}
	if !replaced {
		return errors.New("workflow.yaml has no top-level name field")
	}
	return os.WriteFile(path, []byte(joinLines(lines)), 0o600)
}

// hasYAMLKeyPrefix reports whether line is a top-level mapping of key (no
// indentation, "key:" with an optional value). It is deliberately narrow: only
// an un-indented key counts, so a nested "name:" is never mistaken for the
// workflow name.
func hasYAMLKeyPrefix(line, key string) bool {
	if len(line) <= len(key) || line[:len(key)] != key {
		return false
	}
	return line[len(key)] == ':'
}

// splitLines splits on "\n", preserving a trailing empty element for a trailing
// newline so joinLines round-trips the file's shape.
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, trimCR(s[start:i]))
			start = i + 1
		}
	}
	lines = append(lines, s[start:])
	return lines
}

func trimCR(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\r' {
		return s[:len(s)-1]
	}
	return s
}

func joinLines(lines []string) string {
	out := ""
	for i, line := range lines {
		if i > 0 {
			out += "\n"
		}
		out += line
	}
	return out
}

// wfCopyTree copies the regular files and directories of source into
// destination, which must already exist. It mirrors the workflow store's own
// copy discipline: it refuses symlinks and other non-regular files so a clone
// cannot smuggle in an outside file, and it preserves the owner-execute bit.
func wfCopyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return fmt.Errorf("workflow contains unsupported file %s", rel)
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if info.Mode().Perm()&0o100 != 0 {
			mode |= 0o100
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}
