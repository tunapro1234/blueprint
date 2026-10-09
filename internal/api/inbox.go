package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Inbox agents are agents bp has no terminal for: a script, a chat bot, an
// agent in an IDE. They register a name, and messages to that name wait in a
// per-agent inbox until the agent reads them over HTTP or MCP. Reading marks
// the items read, and a read item is what "delivered" means for them.

// Registration is one inbox agent.
type Registration struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	RegisteredAt string `json:"registeredAt"`
	RegisteredBy string `json:"registeredBy,omitempty"`
	Transport    string `json:"transport,omitempty"`
}

// InboxItem is one message waiting for, or already read by, an inbox agent.
type InboxItem struct {
	ID        string  `json:"id"`
	To        string  `json:"to"`
	From      string  `json:"from"`
	Text      string  `json:"text"`
	TS        float64 `json:"ts"`
	ContextID string  `json:"contextId,omitempty"`
	Room      string  `json:"room,omitempty"`
	// Untrusted marks text that crossed a trust boundary. It is stored raw and
	// framed when the inbox is read.
	Untrusted bool    `json:"untrusted,omitempty"`
	ReadAt    float64 `json:"readAt,omitempty"`
	// Key is the idempotency key the item was stored under, so a retried send
	// returns the same item instead of a second copy.
	Key string `json:"key,omitempty"`
}

// readRetention is how long read items are kept for status lookups.
const readRetention = 48 * time.Hour

// maxInboxItems bounds one inbox. An agent that never reads cannot grow a
// file without limit; the sender is told the inbox is full.
const maxInboxItems = 1000

type inboxStore struct {
	dir string
	now func() time.Time
}

func (s inboxStore) registryPath() string { return filepath.Join(s.dir, "agents.json") }
func (s inboxStore) path(agent string) string {
	return filepath.Join(s.dir, "inbox", agent+".jsonl")
}

func (s inboxStore) registrations() (map[string]Registration, error) {
	out := map[string]Registration{}
	err := readJSON(s.registryPath(), &out)
	return out, err
}

func (s inboxStore) register(reg Registration) (Registration, bool, error) {
	created := false
	err := withLock(s.registryPath(), func() error {
		all, err := s.registrations()
		if err != nil {
			return err
		}
		if old, ok := all[reg.Name]; ok {
			if reg.Description != "" {
				old.Description = reg.Description
			}
			all[reg.Name] = old
			reg = old
		} else {
			reg.RegisteredAt = s.now().UTC().Format(time.RFC3339)
			all[reg.Name] = reg
			created = true
		}
		return writeJSON(s.registryPath(), all)
	})
	return reg, created, err
}

func (s inboxStore) unregister(name string) (bool, error) {
	found := false
	err := withLock(s.registryPath(), func() error {
		all, err := s.registrations()
		if err != nil {
			return err
		}
		if _, found = all[name]; !found {
			return nil
		}
		delete(all, name)
		return writeJSON(s.registryPath(), all)
	})
	return found, err
}

func (s inboxStore) load(agent string) ([]InboxItem, error) {
	var items []InboxItem
	err := readJSONL(s.path(agent), func(item InboxItem) bool {
		items = append(items, item)
		return true
	})
	return items, err
}

// add stores item unless an item with the same key exists, in which case the
// existing one is returned.
func (s inboxStore) add(item InboxItem) (InboxItem, error) {
	path := s.path(item.To)
	err := withLock(path, func() error {
		items, err := s.load(item.To)
		if err != nil {
			return err
		}
		unread := 0
		for _, old := range items {
			if item.Key != "" && old.Key == item.Key {
				if old.From != item.From {
					return fmt.Errorf("idempotency key reused with different content")
				}
				item = old
				return nil
			}
			if old.ReadAt == 0 {
				unread++
			}
		}
		if unread >= maxInboxItems {
			return fmt.Errorf("inbox of %s is full (%d unread)", item.To, unread)
		}
		return appendJSONL(path, item)
	})
	return item, err
}

// take returns up to limit unread items, oldest first. Unless peek is set
// they are marked read, and read items older than readRetention are dropped.
func (s inboxStore) take(agent string, limit int, peek bool) ([]InboxItem, int, error) {
	var out []InboxItem
	remaining := 0
	path := s.path(agent)
	err := withLock(path, func() error {
		items, err := s.load(agent)
		if err != nil {
			return err
		}
		now := s.now()
		stamp := float64(now.UnixNano()) / 1e9
		cutoff := float64(now.Add(-readRetention).UnixNano()) / 1e9
		kept := items[:0]
		changed := false
		for _, item := range items {
			if item.ReadAt == 0 {
				if limit <= 0 || len(out) < limit {
					if !peek {
						item.ReadAt = stamp
						changed = true
					}
					out = append(out, item)
				} else {
					remaining++
				}
			} else if item.ReadAt < cutoff {
				changed = true
				continue
			}
			kept = append(kept, item)
		}
		if !changed {
			return nil
		}
		var buf strings.Builder
		for _, item := range kept {
			line, err := jsonLine(item)
			if err != nil {
				return err
			}
			buf.Write(line)
		}
		return writeFileAtomic(path, []byte(buf.String()))
	})
	return out, remaining, err
}

// find looks an item up by id across every inbox.
func (s inboxStore) find(id string) (InboxItem, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "inbox"))
	if errors.Is(err, os.ErrNotExist) {
		return InboxItem{}, os.ErrNotExist
	}
	if err != nil {
		return InboxItem{}, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var found *InboxItem
		err := readJSONL(filepath.Join(s.dir, "inbox", name), func(item InboxItem) bool {
			if item.ID == id {
				found = &item
				return false
			}
			return true
		})
		if err != nil {
			return InboxItem{}, err
		}
		if found != nil {
			return *found, nil
		}
	}
	return InboxItem{}, os.ErrNotExist
}
