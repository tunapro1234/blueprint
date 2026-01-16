package bp

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type HistoryEntry struct {
	ID        string
	Timestamp string
	Message   string
}

func (b *Blueprint) GetHistory() ([]HistoryEntry, error) {
	historyDir := filepath.Join(b.StateDir, "history")
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []HistoryEntry{}, nil
		}
		return nil, err
	}
	var history []HistoryEntry
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) != 4 || !isDigits(name) {
			continue
		}
		metaPath := filepath.Join(historyDir, name, "meta.yaml")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		parsed, err := ParseYAML(data)
		if err != nil {
			continue
		}
		meta := HistoryEntry{
			ID:        getString(parsed, "id"),
			Timestamp: getString(parsed, "timestamp"),
			Message:   getString(parsed, "message"),
		}
		if meta.ID == "" {
			meta.ID = name
		}
		history = append(history, meta)
	}
	sort.Slice(history, func(i, j int) bool {
		iid := parseSnapshotID(history[i].ID)
		jid := parseSnapshotID(history[j].ID)
		return iid > jid
	})
	return history, nil
}

func parseSnapshotID(id string) int {
	i, err := strconv.Atoi(strings.TrimLeft(id, "0"))
	if err != nil {
		if id == "0000" {
			return 0
		}
		return -1
	}
	return i
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
