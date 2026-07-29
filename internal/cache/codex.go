package cache

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// maxRolloutScan bounds how many recent rollout files a lookup may open: the
// active session's file is always near the top by mtime, the cap only guards
// against a pathological sessions directory.
const maxRolloutScan = 200

// ReadCodex is Read for a codex agent. Codex has no per-agent session registry,
// but every rollout file under CODEX_HOME/sessions opens with a session_meta
// line carrying the cwd it was started in — the freshest rollout for the
// agent's folder is that agent's live session.
func ReadCodex(codexHome, folder string) State {
	if folder == "" {
		return State{LastHumanAge: -1}
	}
	path, ok := newestRollout(filepath.Join(codexHome, "sessions"), folder)
	if !ok {
		return State{LastHumanAge: -1}
	}
	file, err := os.Open(path)
	if err != nil {
		return State{LastHumanAge: -1}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return State{LastHumanAge: -1}
	}
	size := info.Size()
	start := size - tailSize
	if start < 0 {
		start = 0
	}
	data := make([]byte, size-start)
	n, err := file.ReadAt(data, start)
	if err != nil && n == 0 {
		return State{LastHumanAge: -1}
	}
	data = data[:n]
	if start > 0 {
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			data = data[newline+1:]
		}
	}

	var eventTime time.Time
	ctxTokens, window := 0, 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record struct {
			Timestamp string `json:"timestamp"`
			Payload   struct {
				Type string `json:"type"`
				Info *struct {
					Last struct {
						TotalTokens int `json:"total_tokens"`
					} `json:"last_token_usage"`
					ContextWindow int `json:"model_context_window"`
				} `json:"info"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		if timestamp, ok := parseTime(record.Timestamp); ok {
			eventTime = timestamp
		}
		if record.Payload.Type == "token_count" && record.Payload.Info != nil {
			ctxTokens = record.Payload.Info.Last.TotalTokens
			window = record.Payload.Info.ContextWindow
		}
	}
	if eventTime.IsZero() {
		eventTime = info.ModTime()
	}
	return State{
		Known:        ctxTokens > 0,
		Age:          age(time.Now(), eventTime),
		CtxTokens:    ctxTokens,
		Window:       window,
		LastHumanAge: -1,
	}
}

// newestRollout finds the most recently WRITTEN rollout whose session started
// in folder. Every date directory is listed — a long-running or resumed
// session lives in the day directory it was born in, weeks behind today, and
// only its mtime says it is the live one. Directory listings are cheap; the
// per-file first-line read is what the cap bounds, and mtime ordering puts a
// live session at the front long before the cap matters.
func newestRollout(sessionsDir, folder string) (string, bool) {
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
			if entry.IsDir() || filepath.Ext(name) != ".jsonl" {
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
	if len(files) > maxRolloutScan {
		files = files[:maxRolloutScan]
	}
	for _, file := range files {
		if rolloutCwd(file.path) == folder {
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
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if json.Unmarshal(head, &record) != nil {
		return ""
	}
	return record.Payload.Cwd
}
