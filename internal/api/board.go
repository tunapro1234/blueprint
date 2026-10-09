package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"blueprint/internal/messagetext"
)

// The board is a small shared key/value store with authorship and history,
// so a team of agents can keep shared state (a plan, who owns what, a
// decision) without passing it around in messages. Writes take an optional
// expected version, so two agents cannot overwrite each other silently.

// BoardEntry is the current value of one key.
type BoardEntry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Author    string `json:"author"`
	Version   int    `json:"version"`
	UpdatedAt string `json:"updatedAt"`
	Untrusted bool   `json:"untrusted,omitempty"`
	// Source describes an external author for framing.
	Source *FrameSource `json:"source,omitempty"`
}

// BoardChange is one line of a board's history.
type BoardChange struct {
	BoardEntry
	Op string `json:"op"` // put or delete
}

const (
	maxBoardKeys  = 1000
	maxKeyBytes   = 200
	maxValueBytes = 64 << 10
	// DefaultBoard is used when a caller names none.
	DefaultBoard = "main"
)

// ErrConflict reports a write whose expected version is stale.
var ErrConflict = errors.New("version conflict")

func (c *Core) boardPath(board string) string {
	return filepath.Join(c.dir(), "boards", board+".json")
}
func (c *Core) boardHistory(board string) string {
	return filepath.Join(c.dir(), "boards", board+".jsonl")
}

func boardName(name string) (string, error) {
	if name == "" {
		return DefaultBoard, nil
	}
	return name, validRoom(name)
}

var remoteKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)

func validKey(key string) error {
	if key == "" || len(key) > maxKeyBytes || strings.TrimSpace(key) != key || strings.ContainsAny(key, "\n\t") || messagetext.Validate(key) != nil {
		return invalid("key must be 1-%d printable characters on one line", maxKeyBytes)
	}
	return nil
}

func (c *Core) loadBoard(board string) (map[string]BoardEntry, error) {
	entries := map[string]BoardEntry{}
	return entries, readJSON(c.boardPath(board), &entries)
}

// Boards lists board names.
func (c *Core) Boards() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(c.dir(), "boards"))
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, entry := range entries {
		if name, ok := strings.CutSuffix(entry.Name(), ".json"); ok && validRoom(name) == nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// BoardGet returns entries whose key starts with prefix (all with ""),
// sorted by key, or the one entry when key is exact.
func (c *Core) BoardGet(board, key, prefix string) ([]BoardEntry, error) {
	board, err := boardName(board)
	if err != nil {
		return nil, err
	}
	entries, err := c.loadBoard(board)
	if err != nil {
		return nil, err
	}
	if key != "" {
		entry, ok := entries[key]
		if !ok {
			return nil, fmt.Errorf("%w: key %q on board %s", ErrNotFound, key, board)
		}
		entry, err := c.renderEntry(board, entry)
		if err != nil {
			return nil, err
		}
		return []BoardEntry{entry}, nil
	}
	out := []BoardEntry{}
	for k, entry := range entries {
		if strings.HasPrefix(k, prefix) {
			entry, err := c.renderEntry(board, entry)
			if err != nil {
				return nil, err
			}
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// renderEntry frames an external value for the reader; values are stored
// raw. The channel is board/key@version, stable for one stored value.
func (c *Core) renderEntry(board string, entry BoardEntry) (BoardEntry, error) {
	value, err := c.render(entry.Untrusted, entry.Source, entry.Author,
		fmt.Sprintf("%s/%s@%d", board, entry.Key, entry.Version), entry.Value)
	entry.Value = value
	return entry, err
}

// BoardPut writes key. expect < 0 writes unconditionally; expect == 0
// requires the key to be new; expect > 0 requires that current version.
// An empty value with del set removes the key.
func (c *Core) BoardPut(caller Caller, board, key, value string, expect int, del bool) (BoardEntry, error) {
	entry, err := c.boardPut(caller, board, key, value, expect, del)
	kind := "board.put"
	if del {
		kind = "board.delete"
	}
	c.auditResult(caller, kind, board, key, err)
	return entry, err
}

func (c *Core) boardPut(caller Caller, board, key, value string, expect int, del bool) (BoardEntry, error) {
	if err := ValidateCaller(caller); err != nil {
		return BoardEntry{}, err
	}
	board, err := boardName(board)
	if err != nil {
		return BoardEntry{}, err
	}
	if err := validKey(key); err != nil {
		return BoardEntry{}, err
	}
	if caller.Remote && !remoteKey.MatchString(key) {
		// Keys are shown unframed, so a remote key is a plain identifier.
		return BoardEntry{}, invalid("remote clients may use only letters, digits and . _ / - in keys (at most 100)")
	}
	if !del {
		if len(value) > maxValueBytes || messagetext.Validate(value) != nil {
			return BoardEntry{}, invalid("value must be at most %d bytes of printable text", maxValueBytes)
		}
	}
	var out BoardEntry
	path := c.boardPath(board)
	err = withLock(path, func() error {
		entries, err := c.loadBoard(board)
		if err != nil {
			return err
		}
		current, exists := entries[key]
		if expect >= 0 && current.Version != expect {
			return fmt.Errorf("%w: %s is at version %d, not %d", ErrConflict, key, current.Version, expect)
		}
		if del && !exists {
			return fmt.Errorf("%w: key %q on board %s", ErrNotFound, key, board)
		}
		if !del && !exists && len(entries) >= maxBoardKeys {
			return invalid("board %s is full (%d keys)", board, maxBoardKeys)
		}
		out = BoardEntry{Key: key, Value: value, Author: caller.Label(), Version: current.Version + 1,
			UpdatedAt: c.now().UTC().Format(time.RFC3339Nano), Untrusted: caller.Remote, Source: sourceOf(caller, "")}
		op := "put"
		if del {
			op = "delete"
			out.Value = ""
			delete(entries, key)
		} else {
			entries[key] = out
		}
		if err := writeJSON(path, entries); err != nil {
			return err
		}
		return appendHistory(c.boardHistory(board), BoardChange{BoardEntry: out, Op: op})
	})
	return out, err
}

// BoardHistory returns the latest changes, oldest first, optionally for one key.
func (c *Core) BoardHistory(board, key string, limit int) ([]BoardChange, error) {
	board, err := boardName(board)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	changes := []BoardChange{}
	err = readJSONL(c.boardHistory(board), func(change BoardChange) bool {
		if key == "" || change.Key == key {
			changes = append(changes, change)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if len(changes) > limit {
		changes = changes[len(changes)-limit:]
	}
	for i := range changes {
		entry, err := c.renderEntry(board, changes[i].BoardEntry)
		if err != nil {
			return nil, err
		}
		changes[i].BoardEntry = entry
	}
	return changes, nil
}
