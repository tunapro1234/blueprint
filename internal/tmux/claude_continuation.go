package tmux

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Follow only native handoffs made during this verified process lifetime.
// An old /resume may deliberately revisit a historical parent, so its older
// continued-in records are not evidence of the current process's conversation.
func claudeContinuation(projects, cwd, id string, startedAt int64) (string, error) {
	if startedAt <= 0 {
		return id, nil
	}
	seen := map[string]bool{}
	after := time.UnixMilli(startedAt)
	for hop := 0; hop < 16; hop++ {
		if seen[id] {
			return "", fmt.Errorf("Claude continuation cycle")
		}
		seen[id] = true
		path, err := ClaudeSessionPathForID(projects, cwd, id)
		if os.IsNotExist(err) && hop == 0 {
			return id, nil
		}
		if err != nil {
			return "", fmt.Errorf("Claude continuation transcript: %w", err)
		}
		events, scanErr, err := claudeContinuationEvents(path)
		if err != nil {
			return "", err
		}
		next := ""
		var nextAt time.Time
		for _, row := range events {
			if row.unreadable {
				return "", fmt.Errorf("unreadable Claude continuation evidence")
			}
			stamp, e := time.Parse(time.RFC3339Nano, row.Timestamp)
			if e != nil {
				return "", fmt.Errorf("invalid Claude continuation timestamp")
			}
			if stamp.Before(after) {
				continue
			}
			if row.SessionID != id || !codexThreadID.MatchString(row.ContinuedInSessionID) || stamp.After(time.Now().Add(time.Minute)) {
				return "", fmt.Errorf("invalid Claude continuation binding")
			}
			if next != "" && next != row.ContinuedInSessionID {
				return "", fmt.Errorf("conflicting Claude continuations")
			}
			next, nextAt = row.ContinuedInSessionID, stamp
		}
		if scanErr != nil {
			return "", scanErr
		}
		if next == "" {
			return id, nil
		}
		if !claudeContinuationTarget(projects, cwd, next) {
			return "", fmt.Errorf("Claude continuation target lacks matching session/cwd evidence")
		}
		id, after = next, nextAt
	}
	return "", fmt.Errorf("Claude continuation exceeds limit")
}

// claudeContinuationEvent is one line of a transcript's continuation window
// that the hop logic looks at: a continued-in record, or the unreadable line
// that ends the window.
type claudeContinuationEvent struct {
	Type, SessionID, ContinuedInSessionID, Timestamp string
	unreadable                                       bool
}

type claudeContinuationScan struct {
	size     int64
	modified time.Time
	events   []claudeContinuationEvent
	scanErr  error
}

// The bar asks every scan for every Claude agent; its transcript usually has
// not grown since, and decoding 256 KiB of JSON per agent every two seconds
// was the renderer's largest cost. Transcripts are appended, so an unchanged
// size and modification time mean an unchanged window.
var claudeContinuationScans = struct {
	sync.Mutex
	byPath map[string]claudeContinuationScan
}{byPath: make(map[string]claudeContinuationScan)}

// claudeContinuationEvents returns the continued-in records of the last
// 256 KiB of path in order, cut at the first unreadable line, plus the
// scanner's error. err reports only open and stat failures.
func claudeContinuationEvents(path string) (events []claudeContinuationEvent, scanErr, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	claudeContinuationScans.Lock()
	cached, ok := claudeContinuationScans.byPath[path]
	claudeContinuationScans.Unlock()
	if ok && cached.size == info.Size() && cached.modified.Equal(info.ModTime()) {
		return cached.events, cached.scanErr, nil
	}
	const limit int64 = 256 * 1024
	offset := info.Size() - limit
	if offset < 0 {
		offset = 0
	}
	scan := bufio.NewScanner(io.NewSectionReader(f, offset, info.Size()-offset))
	scan.Buffer(make([]byte, 4096), int(limit))
	if offset > 0 {
		scan.Scan()
	}
	for scan.Scan() {
		var row claudeContinuationEvent
		if json.Unmarshal(scan.Bytes(), &row) != nil {
			events = append(events, claudeContinuationEvent{unreadable: true})
			break
		}
		if row.Type == "continued-in" {
			events = append(events, row)
		}
	}
	scanErr = scan.Err()
	claudeContinuationScans.Lock()
	if len(claudeContinuationScans.byPath) >= 256 {
		claudeContinuationScans.byPath = make(map[string]claudeContinuationScan)
	}
	claudeContinuationScans.byPath[path] = claudeContinuationScan{size: info.Size(), modified: info.ModTime(), events: events, scanErr: scanErr}
	claudeContinuationScans.Unlock()
	return events, scanErr, nil
}

func claudeContinuationTarget(projects, cwd, id string) bool {
	path, err := ClaudeSessionPathForID(projects, cwd, id)
	if err != nil {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	scan := bufio.NewScanner(io.LimitReader(f, 256*1024))
	scan.Buffer(make([]byte, 4096), 256*1024)
	for scan.Scan() {
		var row struct {
			SessionID, CWD string
			IsSidechain    bool
		}
		if json.Unmarshal(scan.Bytes(), &row) == nil && !row.IsSidechain && row.SessionID == id && filepath.Clean(row.CWD) == filepath.Clean(cwd) && strings.HasPrefix(row.CWD, "/") {
			return true
		}
	}
	return false
}
