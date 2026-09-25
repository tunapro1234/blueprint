package cache

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ReadCodex reads the latest interactive session in a workspace. A known thread
// id should be passed to ReadCodexSession so another session cannot replace it.
func ReadCodex(codexHome, folder string) State {
	return ReadCodexSession(codexHome, folder, "")
}

func ReadCodexSession(home, folder, id string) State {
	path, ok := CodexPath(home, folder, id)
	if !ok {
		return State{LastHumanAge: -1}
	}
	return ReadCodexPath(path)
}

// ReadCodexPath searches backwards for each metric independently. Tool output
// can separate token_count and turn_context by megabytes; a fixed tail loses
// valid measurements every time that happens. No model call or database write.
func ReadCodexPath(path string) State {
	scan := cachedCodexScan(path)
	result := scan.state
	now := time.Now()
	if result.Known {
		result.Age = age(now, result.UsageAt)
	}
	if !scan.human.IsZero() {
		result.LastHumanAge = age(now, scan.human)
	}
	// A crashed session must not keep a new pane busy indefinitely.
	if result.Busy && age(now, result.TurnAt) > 24*time.Hour {
		result.Busy = false
	}
	return result
}

// codexScan is everything ReadCodexPath learns from the file; ages are derived
// from it at call time so a cached scan never serves a stale age.
type codexScan struct {
	state State
	human time.Time
}

// codexFields is one reverse scan's findings. Each metric is decided by the
// newest row that carries it, independently of the others, so a scan of the
// rows appended since the last scan, merged field by field with the previous
// findings, equals a fresh scan of the whole file. at is the byte offset of
// the deciding row; -1 means no row decided it.
type codexFields struct {
	modelAt, usageAt, turnAt, humanAt int64

	model, effort, tier string
	known               bool
	ctx, window         int
	usageTime, turnTime time.Time
	busy                bool
	humanTime           time.Time
	metas               []codexMeta
}

// codexMeta is a session_meta row the scan passed. Only metas newer than the
// row that completed the scan count, and the oldest of those names the thread.
type codexMeta struct {
	at int64
	id string
}

func newCodexFields() codexFields {
	return codexFields{modelAt: -1, usageAt: -1, turnAt: -1, humanAt: -1}
}

func (f *codexFields) complete() bool {
	return f.modelAt >= 0 && f.usageAt >= 0 && f.turnAt >= 0 && f.humanAt >= 0
}

// stop is the offset of the oldest row the scan had to read: the row that
// decided its last metric, or 0 when some metric was never found.
func (f *codexFields) stop() int64 {
	if !f.complete() {
		return 0
	}
	stop := f.modelAt
	for _, at := range []int64{f.usageAt, f.turnAt, f.humanAt} {
		if at < stop {
			stop = at
		}
	}
	return stop
}

// visit applies one row, newest first. It reports whether the scan must go on.
func (f *codexFields) visit(at int64, line []byte) bool {
	var r struct {
		Type      string    `json:"type"`
		Timestamp time.Time `json:"timestamp"`
		Payload   struct {
			ID          string          `json:"id"`
			Type        string          `json:"type"`
			Role        string          `json:"role"`
			Model       string          `json:"model"`
			Effort      string          `json:"effort"`
			ServiceTier string          `json:"service_tier"`
			Content     json.RawMessage `json:"content"`
			Info        *struct {
				Last *struct {
					Total int `json:"total_tokens"`
				} `json:"last_token_usage"`
				Window int `json:"model_context_window"`
			} `json:"info"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &r) != nil {
		return true
	}
	if r.Type == "session_meta" {
		f.metas = append(f.metas, codexMeta{at: at, id: r.Payload.ID})
	}
	if r.Type == "turn_context" && f.modelAt < 0 {
		f.model, f.effort, f.tier = r.Payload.Model, r.Payload.Effort, r.Payload.ServiceTier
		f.modelAt = at
	}
	// Compaction invalidates the pre-compact context measurement. Wait for
	// a new token_count rather than showing the old full context or a guess.
	if f.usageAt < 0 && (r.Type == "compacted" || (r.Type == "event_msg" && r.Payload.Type == "context_compacted")) {
		f.usageAt = at
	}
	if r.Type == "event_msg" {
		if r.Payload.Type == "token_count" && f.usageAt < 0 && r.Payload.Info != nil && r.Payload.Info.Last != nil {
			f.known = true
			f.usageAt = at
			f.ctx, f.window = r.Payload.Info.Last.Total, r.Payload.Info.Window
			f.usageTime = r.Timestamp
		}
		if f.turnAt < 0 {
			switch r.Payload.Type {
			case "task_started":
				f.busy, f.turnAt, f.turnTime = true, at, r.Timestamp
			case "task_complete", "turn_aborted":
				f.busy, f.turnAt, f.turnTime = false, at, r.Timestamp
			}
		}
	}
	if f.humanAt < 0 && r.Type == "response_item" && r.Payload.Type == "message" && r.Payload.Role == "user" && !automatic(contentText(r.Payload.Content)) {
		f.humanTime, f.humanAt = r.Timestamp, at
	}
	return !f.complete()
}

// merge completes the findings of a scan over newer rows with an earlier scan
// of the rows before them.
func (f codexFields) merge(older codexFields) codexFields {
	if f.modelAt < 0 {
		f.modelAt, f.model, f.effort, f.tier = older.modelAt, older.model, older.effort, older.tier
	}
	if f.usageAt < 0 {
		f.usageAt, f.known, f.ctx, f.window, f.usageTime = older.usageAt, older.known, older.ctx, older.window, older.usageTime
	}
	if f.turnAt < 0 {
		f.turnAt, f.busy, f.turnTime = older.turnAt, older.busy, older.turnTime
	}
	if f.humanAt < 0 {
		f.humanAt, f.humanTime = older.humanAt, older.humanTime
	}
	stop := f.stop()
	metas := make([]codexMeta, 0, len(f.metas)+len(older.metas))
	for _, meta := range append(append([]codexMeta{}, f.metas...), older.metas...) {
		if meta.at >= stop {
			metas = append(metas, meta)
		}
	}
	f.metas = metas
	return f
}

func (f codexFields) scan(path string) codexScan {
	result := State{LastHumanAge: -1, Path: path, ThreadID: CodexID(path)}
	// A full scan meets session_meta rows newest first and keeps the last
	// one it passes, which is the oldest.
	oldest, stop := int64(-1), f.stop()
	for _, meta := range f.metas {
		if meta.at >= stop && (oldest < 0 || meta.at < oldest) {
			oldest, result.ThreadID = meta.at, meta.id
		}
	}
	if f.modelAt >= 0 {
		result.Model, result.Effort, result.ServiceTier = f.model, f.effort, f.tier
	}
	result.Known, result.CtxTokens, result.Window = f.known, f.ctx, f.window
	result.Busy = f.busy
	result.UsageAt, result.TurnAt, result.TurnKnown = f.usageTime, f.turnTime, f.turnAt >= 0
	return codexScan{state: result, human: f.humanTime}
}

// codexScans caches scans by path. The daemon's bar renderer reads every
// attached agent's rollout every scan; an idle rollout does not change, and a
// working one only grows, so only the appended rows are read again.
var codexScans = struct {
	sync.Mutex
	entries map[string]codexScanEntry
}{entries: map[string]codexScanEntry{}}

type codexScanEntry struct {
	size     int64
	modified time.Time
	// rows is the length of the newline-terminated prefix the fields cover;
	// seam holds its last bytes, so a rewritten file is not mistaken for
	// the one that was scanned.
	rows   int64
	seam   []byte
	fields codexFields
}

const codexSeamSize = 256

func cachedCodexScan(path string) codexScan {
	file, err := os.Open(path)
	if err != nil {
		return codexScan{state: State{LastHumanAge: -1, Path: path, ThreadID: CodexID(path)}}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return codexScan{state: State{LastHumanAge: -1, Path: path, ThreadID: CodexID(path)}}
	}
	size := info.Size()
	codexScans.Lock()
	entry, ok := codexScans.entries[path]
	codexScans.Unlock()
	if ok && entry.size == size && entry.modified.Equal(info.ModTime()) {
		return entry.fields.scan(path)
	}
	fields, floor := newCodexFields(), int64(0)
	if ok && entry.rows <= size && entry.fields.stop() < entry.rows && sameSeam(file, entry.rows, entry.seam) {
		floor = entry.rows
	}
	scanRowsReverse(file, floor, size, fields.visit)
	if floor > 0 {
		fields = fields.merge(entry.fields)
	}
	rows := lastRowEnd(file, size)
	codexScans.Lock()
	if len(codexScans.entries) > 256 {
		codexScans.entries = map[string]codexScanEntry{}
	}
	if fields.stop() < rows && decidedBefore(fields, rows) {
		codexScans.entries[path] = codexScanEntry{size: size, modified: info.ModTime(), rows: rows, seam: readSeam(file, rows), fields: fields}
	} else {
		delete(codexScans.entries, path)
	}
	codexScans.Unlock()
	return fields.scan(path)
}

// decidedBefore reports that no metric came from an unterminated final row,
// which the writer may still extend into a different record.
func decidedBefore(f codexFields, rows int64) bool {
	for _, at := range []int64{f.modelAt, f.usageAt, f.turnAt, f.humanAt} {
		if at >= rows {
			return false
		}
	}
	for _, meta := range f.metas {
		if meta.at >= rows {
			return false
		}
	}
	return true
}

// lastRowEnd is the offset just past the last newline before size.
func lastRowEnd(file *os.File, size int64) int64 {
	block := make([]byte, 64*1024)
	for end := size; end > 0; {
		start := end - int64(len(block))
		if start < 0 {
			start = 0
		}
		n, err := file.ReadAt(block[:end-start], start)
		if err != nil && n < int(end-start) {
			return 0
		}
		if i := bytes.LastIndexByte(block[:end-start], '\n'); i >= 0 {
			return start + int64(i) + 1
		}
		end = start
	}
	return 0
}

func readSeam(file *os.File, end int64) []byte {
	start := end - codexSeamSize
	if start < 0 {
		start = 0
	}
	seam := make([]byte, end-start)
	if n, err := file.ReadAt(seam, start); err != nil && n < len(seam) {
		return nil
	}
	return seam
}

func sameSeam(file *os.File, end int64, seam []byte) bool {
	if seam == nil {
		return false
	}
	current := readSeam(file, end)
	return current != nil && bytes.Equal(current, seam)
}

// ScanCodexReverse visits complete JSONL rows, newest first. A partial final row
// is ignored until the writer completes it; giant unrelated records are skipped
// without imposing a tail limit on the metrics behind them.
func ScanCodexReverse(path string, visit func([]byte) bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return
	}
	scanRowsReverse(f, 0, info.Size(), func(_ int64, row []byte) bool { return visit(row) })
}

// scanRowsReverse visits the rows between floor, which must be a row start,
// and size, newest first, with each row's starting offset.
func scanRowsReverse(f *os.File, floor, size int64, visit func(int64, []byte) bool) {
	offset := size
	var fragments [][]byte
	fragmentSize := 0
	discard := false
	emit := func(at int64, prefix []byte) bool {
		if discard {
			return true
		}
		if fragmentSize == 0 {
			return len(prefix) == 0 || visit(at, prefix)
		}
		row := make([]byte, 0, len(prefix)+fragmentSize)
		row = append(row, prefix...)
		for i := len(fragments) - 1; i >= 0; i-- {
			row = append(row, fragments[i]...)
		}
		return visit(at, row)
	}
	for offset > floor {
		n := int64(64 * 1024)
		if offset-floor < n {
			n = offset - floor
		}
		offset -= n
		block := make([]byte, n)
		got, err := f.ReadAt(block, offset)
		if err != nil && got == 0 {
			return
		}
		end := got
		for i := got - 1; i >= 0; i-- {
			if block[i] != '\n' {
				continue
			}
			if !emit(offset+int64(i)+1, block[i+1:end]) {
				return
			}
			fragments, fragmentSize, discard = nil, 0, false
			end = i
		}
		if !discard && end > 0 {
			fragments = append(fragments, block[:end])
			fragmentSize += end
			if fragmentSize > maxTailSize {
				fragments, fragmentSize, discard = nil, 0, true
			}
		}
	}
	emit(floor, nil)
}

// newestRollout finds the most recently WRITTEN rollout whose session started
// in folder. Every date directory is listed — a long-running or resumed
// session lives in the day directory it was born in, weeks behind today, and
// mtime orders the candidates when no thread id has been pinned.
func newestRollout(sessionsDir, folder, id string) (string, bool) {
	type candidate struct {
		path    string
		modTime time.Time
	}
	var files []candidate
	for _, day := range recentDayDirs(sessionsDir) {
		entries, err := os.ReadDir(day)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || filepath.Ext(name) != ".jsonl" || (id != "" && !strings.HasSuffix(name, "-"+id+".jsonl")) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			files = append(files, candidate{filepath.Join(day, name), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })

	for _, file := range files {
		if rolloutCwd(file.path) == folder && (id == "" || CodexID(file.path) == id) {
			return file.path, true
		}
	}
	return "", false
}

// recentDayDirs returns sessions/YYYY/MM/DD directories, newest day first.
// Directory names sort correctly as strings because every component is
// zero-padded.
func recentDayDirs(sessionsDir string) []string {
	var days []string
	years := sortedDirsDesc(sessionsDir)
	for _, year := range years {
		for _, month := range sortedDirsDesc(filepath.Join(sessionsDir, year)) {
			for _, day := range sortedDirsDesc(filepath.Join(sessionsDir, year, month)) {
				days = append(days, filepath.Join(sessionsDir, year, month, day))
			}
		}
	}
	return days
}

func sortedDirsDesc(path string) []string {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names
}

func rolloutCwd(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	// The session_meta line embeds the model's full base instructions, ~20KB
	// today: the buffer must swallow the whole line or the JSON is truncated.
	head := make([]byte, 256*1024)
	n, err := file.Read(head)
	if n == 0 && err != nil {
		return ""
	}
	head = head[:n]
	if newline := bytes.IndexByte(head, '\n'); newline >= 0 {
		head = head[:newline]
	}
	var record struct {
		Payload struct {
			Cwd        string          `json:"cwd"`
			Source     json.RawMessage `json:"source"`
			Originator string          `json:"originator"`
		} `json:"payload"`
	}
	if json.Unmarshal(head, &record) != nil {
		return ""
	}
	var source string
	_ = json.Unmarshal(record.Payload.Source, &source)
	if len(record.Payload.Source) > 0 && source != "cli" && source != "vscode" && source != "appServer" && source != "app-server" {
		return ""
	}
	if strings.Contains(record.Payload.Originator, "exec") {
		return ""
	}
	return record.Payload.Cwd
}

// RolloutPath is newestRollout for callers outside this package: the live
// session file of a codex agent working in folder, or false when there is none.
//
// It exists so the delivery witness can read a codex agent's own record. Until
// 2026-08-25 the witness only knew Claude transcripts, so every unverified
// delivery to a codex pane aged out with "may have gone missing" — including
// ones that had provably arrived and been acted on (q177804375: probot-out-codex
// had already read the message and resent four replies while bp was telling its
// sender the delivery could not be confirmed).
func RolloutPath(codexHome, folder string) (string, bool) {
	return CodexPath(codexHome, folder, "")
}

func CodexPath(codexHome, folder, id string) (string, bool) {
	folder = FolderPath(folder)
	if codexHome == "" || folder == "" {
		return "", false
	}
	if id != "" {
		paths := RolloutPathsByID(codexHome, id)
		if len(paths) != 1 || CodexID(paths[0]) != id || rolloutCwd(paths[0]) != folder {
			return "", false
		}
		return paths[0], true
	}
	return newestRollout(filepath.Join(codexHome, "sessions"), folder, id)
}

// rolloutIndexes remembers, per Codex home, which rollout files each thread id
// matched. Globbing every date directory reads thousands of entries per call;
// the answer can only change when a file is created, removed or renamed in a
// date directory, which changes that directory's mtime (appends do not). So
// a remembered answer — one path, none, or a duplicate — is reused while every
// date directory still has the mtime it had when the answer was computed.
var rolloutIndexes = struct {
	sync.Mutex
	byHome map[string]*rolloutIndex
}{byHome: map[string]*rolloutIndex{}}

type rolloutIndex struct {
	dirs   map[string]time.Time
	newest time.Time
	byID   map[string]rolloutEntry
}

// rolloutEntry is one remembered answer and when its computation began.
type rolloutEntry struct {
	paths []string
	at    time.Time
}

// rolloutRacyWindow guards the mtime rule against coarse timestamps: a file
// created in the same clock tick as an earlier one leaves its directory's
// mtime unchanged. An answer computed while some date directory was modified
// within this window of it is not reused, so such a file is seen next call.
const rolloutRacyWindow = time.Second

func newestDirTime(dirs map[string]time.Time) time.Time {
	var newest time.Time
	for _, at := range dirs {
		if at.After(newest) {
			newest = at
		}
	}
	return newest
}

// RolloutPathsByID returns the rollout files named for thread id under home.
func RolloutPathsByID(home, id string) []string {
	sessions := filepath.Join(home, "sessions")
	started := time.Now()
	dirs, ok := rolloutDayDirs(sessions)
	if ok {
		rolloutIndexes.Lock()
		index := rolloutIndexes.byHome[home]
		if index != nil && sameDirTimes(index.dirs, dirs) {
			if entry, hit := index.byID[id]; hit && index.newest.Before(entry.at.Add(-rolloutRacyWindow)) {
				rolloutIndexes.Unlock()
				return append([]string(nil), entry.paths...)
			}
		}
		rolloutIndexes.Unlock()
	}
	paths, err := filepath.Glob(filepath.Join(sessions, "*", "*", "*", "*-"+id+".jsonl"))
	if err != nil {
		return nil
	}
	if ok {
		rolloutIndexes.Lock()
		index := rolloutIndexes.byHome[home]
		if index == nil || !sameDirTimes(index.dirs, dirs) || len(index.byID) > 1024 {
			if len(rolloutIndexes.byHome) > 16 {
				rolloutIndexes.byHome = map[string]*rolloutIndex{}
			}
			index = &rolloutIndex{dirs: dirs, newest: newestDirTime(dirs), byID: map[string]rolloutEntry{}}
			rolloutIndexes.byHome[home] = index
		}
		index.byID[id] = rolloutEntry{paths: append([]string(nil), paths...), at: started}
		rolloutIndexes.Unlock()
	}
	return paths
}

// rolloutDayDirs returns the mtime of sessions/ and of every directory below
// it down to the date directories. Any read error disables the cache.
func rolloutDayDirs(sessions string) (map[string]time.Time, bool) {
	dirs := map[string]time.Time{}
	var walk func(dir string, depth int) bool
	walk = func(dir string, depth int) bool {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return false
		}
		dirs[dir] = info.ModTime()
		if depth == 3 {
			return true
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return false
		}
		for _, entry := range entries {
			if entry.IsDir() && !walk(filepath.Join(dir, entry.Name()), depth+1) {
				return false
			}
		}
		return true
	}
	return dirs, walk(sessions, 0)
}

func sameDirTimes(a, b map[string]time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for dir, at := range a {
		if other, ok := b[dir]; !ok || !other.Equal(at) {
			return false
		}
	}
	return true
}

// CodexID reads the identity, never instructions or credentials.
func CodexID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), maxTailSize)
	if !scanner.Scan() {
		return ""
	}
	var row struct {
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if json.Unmarshal(scanner.Bytes(), &row) != nil {
		return ""
	}
	return row.Payload.ID
}
