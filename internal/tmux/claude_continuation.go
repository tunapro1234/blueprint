package tmux

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return "", err
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
		next := ""
		var nextAt time.Time
		for scan.Scan() {
			var row struct{ Type, SessionID, ContinuedInSessionID, Timestamp string }
			if json.Unmarshal(scan.Bytes(), &row) != nil {
				f.Close()
				return "", fmt.Errorf("unreadable Claude continuation evidence")
			}
			if row.Type != "continued-in" {
				continue
			}
			stamp, e := time.Parse(time.RFC3339Nano, row.Timestamp)
			if e != nil {
				f.Close()
				return "", fmt.Errorf("invalid Claude continuation timestamp")
			}
			if stamp.Before(after) {
				continue
			}
			if row.SessionID != id || !codexThreadID.MatchString(row.ContinuedInSessionID) || stamp.After(time.Now().Add(time.Minute)) {
				f.Close()
				return "", fmt.Errorf("invalid Claude continuation binding")
			}
			if next != "" && next != row.ContinuedInSessionID {
				f.Close()
				return "", fmt.Errorf("conflicting Claude continuations")
			}
			next, nextAt = row.ContinuedInSessionID, stamp
		}
		err = scan.Err()
		f.Close()
		if err != nil {
			return "", err
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
