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
	Source      string // "local" | "tag"
	TagName     string // full git tag name (when Source == "tag")
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
	meta.Source = getString(parsed, "source")
	meta.TagName = getString(parsed, "tag_name")
	if meta.Source == "" {
		meta.Source = "local"
	}
	if meta.ID == "" {
		meta.ID = id
	}
	return meta, nil
}

// GetHistoryWithTags returns local snapshot history merged with git tag
// snapshots. Local entries win when IDs collide. Results are sorted
// newest-first by timestamp.
func (b *Blueprint) GetHistoryWithTags() ([]HistoryEntry, error) {
	history, err := b.GetHistory()
	if err != nil {
		return nil, err
	}
	if !GitAvailable(b.Dir) {
		return history, nil
	}
	repoRoot, err := GitRepoRoot(b.Dir)
	if err != nil {
		return history, nil
	}
	rel, err := filepath.Rel(repoRoot, b.Dir)
	if err != nil {
		return history, nil
	}
	pattern := "bp/" + filepath.ToSlash(rel) + "/*"
	tags, err := GitListTags(b.Dir, pattern)
	if err != nil {
		return history, nil
	}
	if len(tags) == 0 {
		return history, nil
	}

	localIDs := map[string]struct{}{}
	for _, entry := range history {
		localIDs[entry.ID] = struct{}{}
	}

	for _, tag := range tags {
		tagID := ExtractTagID(tag.Name)
		if _, exists := localIDs[tagID]; exists {
			continue
		}
		ts := tag.Timestamp
		// Normalize ISO8601 strict to local format if possible
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			ts = t.Format("2006-01-02T15:04:05")
		}
		entry := HistoryEntry{
			ID:        tagID,
			Timestamp: ts,
			Message:   tagID,
			Source:    "tag",
			TagName:  tag.Name,
		}
		history = append(history, entry)
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
