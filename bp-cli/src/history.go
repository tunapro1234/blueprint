package bp

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type HistoryEntry struct {
	ID        string
	Timestamp string
	Message   string
	ImplHash  string
	DepsHash  string
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
		if !isSnapshotID(name) {
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
			ImplHash:  getString(parsed, "impl_hash"),
			DepsHash:  getString(parsed, "deps_hash"),
		}
		if meta.ID == "" {
			meta.ID = name
		}
		if meta.ID == "" || meta.Timestamp == "" || !isSnapshotID(meta.ID) {
			continue
		}
		history = append(history, meta)
	}
	sort.Slice(history, func(i, j int) bool {
		ti, err1 := time.Parse("2006-01-02T15:04:05", history[i].Timestamp)
		tj, err2 := time.Parse("2006-01-02T15:04:05", history[j].Timestamp)
		if err1 == nil && err2 == nil {
			return ti.After(tj)
		}
		if err1 == nil {
			return true
		}
		if err2 == nil {
			return false
		}
		return strings.Compare(history[i].Timestamp, history[j].Timestamp) > 0
	})
	return history, nil
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
