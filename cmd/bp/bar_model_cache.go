package main

import (
	"os"
	"path/filepath"
	"sync"
)

type cachedBarModel struct {
	modTime int64
	size    int64
	model   string
	effort  string
}

// barModelCache retains only small model labels. File metadata invalidates a
// value when a rollout or settings file changes without keeping transcript data
// in memory.
type barModelCache struct {
	mu     sync.Mutex
	values map[string]cachedBarModel
}

func newBarModelCache() *barModelCache {
	return &barModelCache{values: make(map[string]cachedBarModel)}
}

func (c *barModelCache) cached(path string, info os.FileInfo) (string, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.values[path]
	if !ok || value.modTime != info.ModTime().UnixNano() || value.size != info.Size() {
		return "", "", false
	}
	return value.model, value.effort, true
}

func (c *barModelCache) store(path string, info os.FileInfo, model, effort string) {
	c.mu.Lock()
	c.values[path] = cachedBarModel{modTime: info.ModTime().UnixNano(), size: info.Size(), model: model, effort: effort}
	c.mu.Unlock()
}

func (c *barModelCache) remember(path, model, effort string) (string, string) {
	info, err := os.Stat(path)
	if err != nil {
		return model, effort
	}
	if cachedModel, cachedEffort, ok := c.cached(path, info); ok {
		return cachedModel, cachedEffort
	}
	c.store(path, info, model, effort)
	return model, effort
}

func (c *barModelCache) read(path string, read func(string) (string, string)) (string, string) {
	info, err := os.Stat(path)
	if err != nil {
		return read(path)
	}
	if model, effort, ok := c.cached(path, info); ok {
		return model, effort
	}
	model, effort := read(path)
	if after, err := os.Stat(path); err == nil && after.Size() == info.Size() && after.ModTime().Equal(info.ModTime()) {
		c.store(path, after, model, effort)
	}
	return model, effort
}

func (c *barModelCache) readClaude(folder, home string) (string, string) {
	if folder != "" {
		local := filepath.Join(folder, ".claude", "settings.local.json")
		model, effort, err := c.readClaudeFile(local)
		if err == nil && (model != "" || effort != "") {
			return model, effort
		}
		if err != nil && !os.IsNotExist(err) {
			return "", ""
		}
	}
	if home == "" {
		return "", ""
	}
	model, effort, _ := c.readClaudeFile(filepath.Join(home, ".claude", "settings.json"))
	return model, effort
}

func (c *barModelCache) readClaudeFile(path string) (string, string, error) {
	info, statErr := os.Stat(path)
	if statErr == nil {
		if model, effort, ok := c.cached(path, info); ok {
			return model, effort, nil
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	model, effort := parseClaudeModel(data)
	if info == nil {
		info, _ = os.Stat(path)
	}
	if info != nil {
		if after, err := os.Stat(path); err == nil && after.Size() == info.Size() && after.ModTime().Equal(info.ModTime()) {
			c.store(path, after, model, effort)
		}
	}
	return model, effort, nil
}
