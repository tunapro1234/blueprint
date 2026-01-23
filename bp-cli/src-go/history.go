package bp

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type HistoryEntry struct {
	ID          string
	Timestamp   string
	Message     string
	ContentHash string
	APIHash     string
	SpecHash    string
	ImplHash    string
	Rotten      bool
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
		if !IsSnapshotID(name) {
			continue
		}
		meta, err := LoadSnapshotMeta(b.StateDir, name)
		if err != nil {
			continue
		}
		if meta.ID == "" {
			meta.ID = name
		}
		if meta.ID == "" || meta.Timestamp == "" || !IsSnapshotID(meta.ID) {
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

func LoadSnapshotMeta(stateDir, id string) (HistoryEntry, error) {
	metaPath := filepath.Join(stateDir, "history", id, "meta.yaml")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return HistoryEntry{}, err
	}
	parsed, err := ParseYAML(data)
	if err != nil {
		return HistoryEntry{}, err
	}
	meta := HistoryEntry{
		ID:          getString(parsed, "id"),
		Timestamp:   getString(parsed, "timestamp"),
		Message:     getString(parsed, "message"),
		ContentHash: getString(parsed, "content_hash"),
		APIHash:     getString(parsed, "api_hash"),
		SpecHash:    getString(parsed, "spec_hash"),
		ImplHash:    getString(parsed, "impl_hash"),
	}
	if v, ok := parsed["rotten"].(bool); ok {
		meta.Rotten = v
	}
	if meta.ID == "" {
		meta.ID = id
	}
	return meta, nil
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
