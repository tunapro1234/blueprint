// Package ledger is bp's durable work ledger: projects, tasks with leases and
// fencing, evidence-backed findings, a cursor-based change feed and a
// deterministic briefing. It sits beside the board and rooms: those carry
// free text, the ledger carries state with rules.
//
// Storage, per project, under <dir>/<project>/:
//
//   - events.jsonl — the append-only source of truth. Each accepted change
//     is one transaction: one or more events, each carrying the full entity
//     after the change, a monotonic sequence number and the transaction's
//     first sequence and size (tx, txSize). The append is fsynced before
//     the change is acknowledged.
//   - state.json — a snapshot of the materialized state at some sequence.
//     It is a cache: loading replays every event after the snapshot's
//     sequence, so a crash between the log append and the snapshot write
//     loses nothing, and a failed snapshot write does not undo a durable
//     change.
//   - archive/<first>-<last>.jsonl — events dropped from the live log by
//     retention, kept so task and finding history keeps its provenance.
//   - subs.json — per-caller feed subscriptions (cursor and filter). Acks
//     are not events, so reading the feed never feeds itself.
//
// Recovery is fail-closed: a malformed line, a sequence gap, a foreign
// epoch or an incomplete transaction anywhere but at the very end of the
// log is reported as corruption (ErrCorrupt), never skipped. Only an
// incomplete final transaction — a crash mid-append, which was never
// acknowledged — is ignored on read and cut before the next append.
//
// Every read and write of a project holds one exclusive flock on
// <project>/lock, so the CLI, bp mcp and bp serve --api can share a project.
//
// The ledger never treats a transport receipt as work: a task is verified
// only by an explicit verification from an independent actor.
package ledger

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Errors. Callers match them with errors.Is; the API maps them to its own
// error classes and HTTP statuses.
var (
	ErrNotFound      = errors.New("not found")
	ErrInvalid       = errors.New("invalid request")
	ErrForbidden     = errors.New("forbidden")
	ErrConflict      = errors.New("conflict")
	ErrStaleOwner    = errors.New("stale owner")
	ErrLeaseExpired  = errors.New("lease expired")
	ErrCycle         = errors.New("dependency cycle")
	ErrDependency    = errors.New("dependency not satisfied")
	ErrStaleEvidence = errors.New("stale evidence")
	ErrCursorExpired = errors.New("cursor expired")
	ErrCursorInvalid = errors.New("cursor invalid")
	ErrHalted        = errors.New("project not active")
	ErrCorrupt       = errors.New("ledger corrupt")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func corrupt(path string, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s; bp refuses to guess — restore the file from a backup or move the project directory aside", ErrCorrupt, path, fmt.Sprintf(format, args...))
}

// Actor is who asks. Label is the identity the caller presents; Verified
// says bp proved it (a pane bp started) rather than the caller declaring
// it over a transport. Authorization is decided per operation from the
// ledger's own records (project lead, task owner and fence, verification
// independence and proof), never from the label's shape alone.
type Actor struct {
	Label    string
	Verified bool
}

// Store is the ledger rooted at Dir.
type Store struct {
	Dir string
	// Now is the clock; tests set a fixed one.
	Now func() time.Time
	// MaxEvents bounds the live log. When it grows past this, the oldest
	// events move to an archive segment, down to half of it; feed cursors
	// older than the live floor are refused with ErrCursorExpired.
	MaxEvents int
	// failSnapshot is a test hook that makes snapshot writes fail.
	failSnapshot bool
}

const (
	defaultMaxEvents = 10000
	dirMode          = 0o700
	fileMode         = 0o600
	maxLineBytes     = 4 << 20
)

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Store) maxEvents() int {
	if s.MaxEvents > 0 {
		return s.MaxEvents
	}
	return defaultMaxEvents
}

var projectName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func validProject(name string) error {
	if !projectName.MatchString(name) || strings.Contains(name, "..") {
		return invalid("project name must be 1-64 lowercase letters, digits, '.', '_' or '-'")
	}
	return nil
}

func (s *Store) projectDir(name string) string { return filepath.Join(s.Dir, name) }

func (s *Store) logPath(name string) string { return filepath.Join(s.projectDir(name), "events.jsonl") }

// State is a project's materialized state.
type State struct {
	Project  Project             `json:"project"`
	Tasks    map[string]*Task    `json:"tasks"`
	Findings map[string]*Finding `json:"findings"`
	// Seq is the sequence of the last applied event.
	Seq int64 `json:"seq"`

	// Derived from the live log on every load, never trusted from the
	// snapshot: the oldest live event, the live event count and the byte
	// offset where the last complete transaction ends.
	FirstSeq int64 `json:"-"`
	Events   int   `json:"-"`
	validEnd int64
}

// Event is one accepted change. It carries the whole entity after the
// change, so replay is a plain overwrite.
type Event struct {
	Seq int64 `json:"seq"`
	// Tx is the first sequence of the transaction this event belongs to;
	// TxSize is how many events the transaction has.
	Tx      int64    `json:"tx"`
	TxSize  int      `json:"txSize"`
	Epoch   string   `json:"epoch"`
	At      string   `json:"at"`
	Actor   string   `json:"actor"`
	Kind    string   `json:"kind"` // project, task or finding
	ID      string   `json:"id"`
	Op      string   `json:"op"`
	Version int      `json:"version"`
	Owner   string   `json:"owner,omitempty"`
	State   string   `json:"state,omitempty"`
	Note    string   `json:"note,omitempty"`
	Project *Project `json:"project,omitempty"`
	Task    *Task    `json:"task,omitempty"`
	Finding *Finding `json:"finding,omitempty"`
}

func newState() *State {
	return &State{Tasks: map[string]*Task{}, Findings: map[string]*Finding{}}
}

// apply overwrites the entity an event carries.
func (st *State) apply(ev Event) {
	switch {
	case ev.Project != nil:
		st.Project = *clone(ev.Project)
	case ev.Task != nil:
		st.Tasks[ev.ID] = clone(ev.Task)
	case ev.Finding != nil:
		st.Findings[ev.ID] = clone(ev.Finding)
	}
	st.Seq = ev.Seq
}

// clone deep-copies through JSON, so an event never aliases live state.
func clone[T any](v *T) *T {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	out := new(T)
	if err := json.Unmarshal(data, out); err != nil {
		panic(err)
	}
	return out
}

// lock runs fn while holding the project's exclusive flock.
func (s *Store) lock(project string, fn func() error) error {
	if err := validProject(project); err != nil {
		return err
	}
	dir := s.projectDir(project)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, fileMode)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}

// logScan is a validated read of a log file.
type logScan struct {
	events   []Event // complete transactions only
	validEnd int64   // byte offset after the last complete transaction
	tail     int     // events of an incomplete final transaction (ignored)
	torn     bool    // a malformed final line (ignored)
}

// scanLog reads and validates a log: contiguous sequences, one epoch,
// complete transactions. Only the end of the file may be incomplete.
func scanLog(path string) (logScan, error) {
	var sc logScan
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return sc, nil
	}
	if err != nil {
		return sc, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	var offset int64
	var pending []Event // the transaction being read
	lineNo := 0
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > maxLineBytes {
			return sc, corrupt(path, "line %d is over %d bytes", lineNo+1, maxLineBytes)
		}
		if errors.Is(err, io.EOF) {
			if len(bytes.TrimSpace(line)) > 0 {
				sc.torn = true // no newline: a crash mid-write
			}
			break
		}
		if err != nil {
			return sc, err
		}
		lineNo++
		offset += int64(len(line))
		var ev Event
		if json.Unmarshal(line, &ev) != nil || ev.Seq <= 0 || ev.Tx <= 0 || ev.TxSize <= 0 {
			// A bad line is acceptable only as the very last line.
			if _, peekErr := r.Peek(1); errors.Is(peekErr, io.EOF) {
				sc.torn = true
				break
			}
			return sc, corrupt(path, "line %d is not a ledger event", lineNo)
		}
		prev := int64(0)
		switch {
		case len(pending) > 0:
			prev = pending[len(pending)-1].Seq
		case len(sc.events) > 0:
			prev = sc.events[len(sc.events)-1].Seq
		}
		if prev != 0 && ev.Seq != prev+1 {
			return sc, corrupt(path, "line %d has sequence %d after %d", lineNo, ev.Seq, prev)
		}
		epoch := ""
		if len(sc.events) > 0 {
			epoch = sc.events[0].Epoch
		} else if len(pending) > 0 {
			epoch = pending[0].Epoch
		}
		if epoch != "" && ev.Epoch != epoch {
			return sc, corrupt(path, "line %d belongs to another project incarnation", lineNo)
		}
		if len(pending) == 0 {
			if ev.Tx != ev.Seq {
				return sc, corrupt(path, "line %d starts mid-transaction %d", lineNo, ev.Tx)
			}
		} else if ev.Tx != pending[0].Tx || ev.TxSize != pending[0].TxSize {
			return sc, corrupt(path, "transaction %d is incomplete before line %d", pending[0].Tx, lineNo)
		}
		pending = append(pending, ev)
		if len(pending) == ev.TxSize {
			sc.events = append(sc.events, pending...)
			pending = nil
			sc.validEnd = offset
		}
	}
	sc.tail = len(pending)
	return sc, nil
}

// load reads the snapshot, validates the live log and replays it. A
// project that was never created yields ErrNotFound.
func (s *Store) load(project string) (*State, error) {
	dir := s.projectDir(project)
	st := newState()
	snapPath := filepath.Join(dir, "state.json")
	data, err := os.ReadFile(snapPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, st); err != nil {
			return nil, corrupt(snapPath, "%v", err)
		}
		if st.Tasks == nil {
			st.Tasks = map[string]*Task{}
		}
		if st.Findings == nil {
			st.Findings = map[string]*Finding{}
		}
	}
	logPath := s.logPath(project)
	sc, err := scanLog(logPath)
	if err != nil {
		return nil, err
	}
	if len(sc.events) == 0 {
		if st.Seq > 0 {
			return nil, corrupt(logPath, "the log is empty but the snapshot is at sequence %d", st.Seq)
		}
		return nil, fmt.Errorf("%w: project %q (create it with bp work project create)", ErrNotFound, project)
	}
	first, last := sc.events[0], sc.events[len(sc.events)-1]
	switch {
	case st.Project.Epoch == "" && (first.Seq != 1 || first.Kind != "project" || first.Op != "create"):
		return nil, corrupt(snapPath, "the snapshot is missing and the log does not start at the project's creation")
	case st.Project.Epoch != "" && first.Epoch != st.Project.Epoch:
		return nil, corrupt(logPath, "the log belongs to another incarnation of the project than the snapshot")
	case first.Seq > st.Seq+1:
		return nil, corrupt(logPath, "events %d..%d are missing between the snapshot and the log", st.Seq+1, first.Seq-1)
	case last.Seq < st.Seq:
		return nil, corrupt(logPath, "the snapshot (sequence %d) is ahead of the log (sequence %d)", st.Seq, last.Seq)
	}
	for _, ev := range sc.events {
		if ev.Seq > st.Seq {
			st.apply(ev)
		}
	}
	st.FirstSeq, st.Events, st.validEnd = first.Seq, len(sc.events), sc.validEnd
	return st, nil
}

// appendTx writes one transaction after cutting anything past validEnd
// (an incomplete transaction or torn line from an earlier crash), then
// fsyncs the file and, when it was just created, its directory.
func appendTx(path string, validEnd int64, events []Event) (int64, error) {
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, fileMode)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() > validEnd {
		if err := f.Truncate(validEnd); err != nil {
			return 0, err
		}
	}
	var buf []byte
	for _, ev := range events {
		line, err := json.Marshal(ev)
		if err != nil {
			return 0, err
		}
		if len(line)+1 > maxLineBytes {
			return 0, invalid("event for %s %s is too large", ev.Kind, ev.ID)
		}
		buf = append(append(buf, line...), '\n')
	}
	if _, err := f.WriteAt(buf, validEnd); err != nil {
		return 0, err
	}
	if err := f.Sync(); err != nil {
		return 0, err
	}
	if created {
		if err := syncDir(filepath.Dir(path)); err != nil {
			return 0, err
		}
	}
	return validEnd + int64(len(buf)), nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(fileMode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// change is what a mutation hands back: the events to record, in order.
type change struct {
	events []Event
}

func (c *change) add(ev Event) { c.events = append(c.events, ev) }

// mutate loads the project, runs fn, and commits the events it produced.
func (s *Store) mutate(project string, actor Actor, fn func(st *State, ch *change) error) error {
	if err := validActor(actor); err != nil {
		return err
	}
	return s.lock(project, func() error {
		st, err := s.load(project)
		if err != nil {
			return err
		}
		ch := &change{}
		if err := fn(st, ch); err != nil {
			return err
		}
		return s.commit(project, st, ch, actor)
	})
}

// commit appends the transaction (the durable step), then refreshes the
// snapshot and applies retention. Once the append is fsynced the change is
// committed: a snapshot failure is not reported as a failed change,
// because the next load replays the log; retention waits for a good
// snapshot so it never drops events the snapshot lacks.
func (s *Store) commit(project string, st *State, ch *change, actor Actor) error {
	if len(ch.events) == 0 {
		return nil
	}
	at := s.now().Format(time.RFC3339Nano)
	tx := st.Seq + 1
	for i := range ch.events {
		ev := &ch.events[i]
		ev.Seq, ev.Tx, ev.TxSize = tx+int64(i), tx, len(ch.events)
		ev.Epoch, ev.At, ev.Actor = st.Project.Epoch, at, actor.Label
		// Snapshot the entity as it is now, so later edits in the same
		// mutation do not leak backwards.
		switch {
		case ev.Project != nil:
			ev.Project = clone(ev.Project)
			if ev.Epoch == "" {
				ev.Epoch = ev.Project.Epoch
			}
		case ev.Task != nil:
			ev.Task = clone(ev.Task)
			ev.Owner, ev.State = ev.Task.Owner, ev.Task.State
		case ev.Finding != nil:
			ev.Finding = clone(ev.Finding)
			ev.Owner, ev.State = ev.Finding.Author, ev.Finding.Status
		}
	}
	end, err := appendTx(s.logPath(project), st.validEnd, ch.events)
	if err != nil {
		return err
	}
	st.validEnd = end
	st.Seq = ch.events[len(ch.events)-1].Seq
	if st.FirstSeq == 0 {
		st.FirstSeq = ch.events[0].Seq
	}
	st.Events += len(ch.events)
	if s.failSnapshot {
		return nil
	}
	if err := writeJSON(filepath.Join(s.projectDir(project), "state.json"), st); err != nil {
		return nil //nolint:nilerr // committed; the log is the source of truth
	}
	if st.Events > s.maxEvents() {
		// Retention failing leaves a longer log, which is safe.
		_ = s.compact(project, st)
	}
	return nil
}

// compact moves the oldest live events to an archive segment, down to half
// of MaxEvents. The archive segment is durable before the live log is
// rewritten; a crash in between leaves the events in both places, which
// history reads deduplicate by sequence.
func (s *Store) compact(project string, st *State) error {
	keep := s.maxEvents() / 2
	if keep < 1 {
		keep = 1
	}
	sc, err := scanLog(s.logPath(project))
	if err != nil {
		return err
	}
	if len(sc.events) <= keep {
		return nil
	}
	cut := len(sc.events) - keep
	// Never split a transaction.
	for cut < len(sc.events) && sc.events[cut].Tx != sc.events[cut].Seq {
		cut++
	}
	if cut >= len(sc.events) {
		return nil
	}
	dropped, kept := sc.events[:cut], sc.events[cut:]
	archive := filepath.Join(s.projectDir(project), "archive")
	if err := os.MkdirAll(archive, dirMode); err != nil {
		return err
	}
	name := fmt.Sprintf("%012d-%012d.jsonl", dropped[0].Seq, dropped[len(dropped)-1].Seq)
	if err := writeFileAtomic(filepath.Join(archive, name), eventLines(dropped)); err != nil {
		return err
	}
	if err := writeFileAtomic(s.logPath(project), eventLines(kept)); err != nil {
		return err
	}
	st.FirstSeq, st.Events = kept[0].Seq, len(kept)
	st.validEnd = int64(len(eventLines(kept)))
	return nil
}

func eventLines(events []Event) []byte {
	var buf []byte
	for _, ev := range events {
		line, err := json.Marshal(ev)
		if err != nil {
			panic(err)
		}
		buf = append(append(buf, line...), '\n')
	}
	return buf
}

// history returns every retained event (archive segments, then the live
// log) of the current incarnation that matches keep, deduplicated by
// sequence, oldest first.
func (s *Store) history(project string, st *State, keep func(Event) bool) ([]Event, error) {
	var out []Event
	seen := map[int64]bool{}
	add := func(events []Event) {
		for _, ev := range events {
			if ev.Epoch == st.Project.Epoch && !seen[ev.Seq] && ev.Seq <= st.Seq && keep(ev) {
				seen[ev.Seq] = true
				out = append(out, ev)
			}
		}
	}
	segments, err := filepath.Glob(filepath.Join(s.projectDir(project), "archive", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(segments)
	for _, seg := range segments {
		sc, err := scanLog(seg)
		if err != nil {
			return nil, err
		}
		add(sc.events)
	}
	sc, err := scanLog(s.logPath(project))
	if err != nil {
		return nil, err
	}
	add(sc.events)
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// read loads a project under the lock for a consistent view. A project
// that has no directory is not found without creating one, so reads of
// unknown names leave nothing behind.
func (s *Store) read(project string, fn func(st *State) error) error {
	if err := validProject(project); err != nil {
		return err
	}
	if _, err := os.Stat(s.projectDir(project)); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: project %q (create it with bp work project create)", ErrNotFound, project)
	}
	return s.lock(project, func() error {
		st, err := s.load(project)
		if err != nil {
			return err
		}
		return fn(st)
	})
}

func validActor(a Actor) error {
	if strings.TrimSpace(a.Label) == "" || len(a.Label) > 200 || strings.ContainsAny(a.Label, "\n\t ") {
		return invalid("caller identity required")
	}
	return nil
}

func randomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// Cursor is a feed position: the project epoch and a sequence number.
func formatCursor(epoch string, seq int64) string { return epoch + ":" + strconv.FormatInt(seq, 10) }

func parseCursor(cursor string) (string, int64, error) {
	epoch, seqText, ok := strings.Cut(cursor, ":")
	if !ok || epoch == "" {
		return "", 0, fmt.Errorf("%w: %q is not a cursor (want <epoch>:<seq>)", ErrCursorInvalid, cursor)
	}
	seq, err := strconv.ParseInt(seqText, 10, 64)
	if err != nil || seq < 0 {
		return "", 0, fmt.Errorf("%w: %q is not a cursor (want <epoch>:<seq>)", ErrCursorInvalid, cursor)
	}
	return epoch, seq, nil
}

// idNumber orders ids like t2 < t10.
func idNumber(id string) int {
	n, _ := strconv.Atoi(strings.TrimLeft(id, "tf"))
	return n
}

func sortIDs(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		a, b := idNumber(ids[i]), idNumber(ids[j])
		if a != b {
			return a < b
		}
		return ids[i] < ids[j]
	})
}

func textField(name, value string, max int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return invalid("%s is required", name)
	}
	if len(value) > max {
		return invalid("%s is %d bytes, over the %d byte limit", name, len(value), max)
	}
	for _, r := range value {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return invalid("%s contains control characters", name)
		}
	}
	return nil
}
