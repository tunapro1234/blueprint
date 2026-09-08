package cache

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	result := State{LastHumanAge: -1, Path: path, ThreadID: CodexID(path)}
	var usageTime, humanTime, turnTime time.Time
	modelSeen, humanSeen, turnSeen, usageSeen := false, false, false, false
	ScanCodexReverse(path, func(line []byte) bool {
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
			result.ThreadID = r.Payload.ID
		}
		if r.Type == "turn_context" && !modelSeen {
			result.Model, result.Effort, result.ServiceTier = r.Payload.Model, r.Payload.Effort, r.Payload.ServiceTier
			modelSeen = true
		}
		// Compaction invalidates the pre-compact context measurement. Wait for
		// a new token_count rather than showing the old full context or a guess.
		if r.Type == "compacted" || (r.Type == "event_msg" && r.Payload.Type == "context_compacted") {
			usageSeen = true
		}
		if r.Type == "event_msg" {
			if r.Payload.Type == "token_count" && !usageSeen && r.Payload.Info != nil && r.Payload.Info.Last != nil {
				result.Known = true
				usageSeen = true
				result.CtxTokens, result.Window = r.Payload.Info.Last.Total, r.Payload.Info.Window
				usageTime = r.Timestamp
			}
			if !turnSeen {
				switch r.Payload.Type {
				case "task_started":
					result.Busy, turnSeen, turnTime = true, true, r.Timestamp
				case "task_complete", "turn_aborted":
					result.Busy, turnSeen, turnTime = false, true, r.Timestamp
				}
			}
		}
		if !humanSeen && r.Type == "response_item" && r.Payload.Type == "message" && r.Payload.Role == "user" && !automatic(contentText(r.Payload.Content)) {
			humanTime, humanSeen = r.Timestamp, true
		}
		return !(usageSeen && modelSeen && humanSeen && turnSeen)
	})
	now := time.Now()
	result.UsageAt, result.TurnAt, result.TurnKnown = usageTime, turnTime, turnSeen
	if result.Known {
		result.Age = age(now, usageTime)
	}
	if !humanTime.IsZero() {
		result.LastHumanAge = age(now, humanTime)
	}
	// A crashed session must not keep a new pane busy indefinitely.
	if result.Busy && age(now, turnTime) > 24*time.Hour {
		result.Busy = false
	}
	return result
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
	offset := info.Size()
	var fragments [][]byte
	size := 0
	discard := false
	emit := func(prefix []byte) bool {
		if discard {
			return true
		}
		if size == 0 {
			return len(prefix) == 0 || visit(prefix)
		}
		row := make([]byte, 0, len(prefix)+size)
		row = append(row, prefix...)
		for i := len(fragments) - 1; i >= 0; i-- {
			row = append(row, fragments[i]...)
		}
		return visit(row)
	}
	for offset > 0 {
		n := int64(64 * 1024)
		if offset < n {
			n = offset
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
			if !emit(block[i+1 : end]) {
				return
			}
			fragments, size, discard = nil, 0, false
			end = i
		}
		if !discard && end > 0 {
			fragments = append(fragments, block[:end])
			size += end
			if size > maxTailSize {
				fragments, size, discard = nil, 0, true
			}
		}
	}
	emit(nil)
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
		paths, err := filepath.Glob(filepath.Join(codexHome, "sessions", "*", "*", "*", "*-"+id+".jsonl"))
		if err != nil || len(paths) != 1 || CodexID(paths[0]) != id || rolloutCwd(paths[0]) != folder {
			return "", false
		}
		return paths[0], true
	}
	return newestRollout(filepath.Join(codexHome, "sessions"), folder, id)
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
