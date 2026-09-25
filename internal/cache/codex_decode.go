package cache

import (
	"bytes"
	"encoding/json"
	"hash/maphash"
	"sync"
	"time"
)

// CodexRecord is the shared decoded shape used by rollout state and attention
// readers. Raw content is copied before it enters the in-process row cache.
type CodexRecord struct {
	Type      string       `json:"type"`
	Timestamp time.Time    `json:"timestamp"`
	Payload   CodexPayload `json:"payload"`
}

type CodexPayload struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Role        string          `json:"role"`
	Model       string          `json:"model"`
	Effort      string          `json:"effort"`
	ServiceTier string          `json:"service_tier"`
	Content     json.RawMessage `json:"content"`
	Info        *CodexInfo      `json:"info"`
}

type CodexInfo struct {
	Last   *CodexTokenUsage `json:"last_token_usage"`
	Window int              `json:"model_context_window"`
}

type CodexTokenUsage struct {
	Total int `json:"total_tokens"`
}

var codexRecordDecodes = struct {
	sync.Mutex
	seed    maphash.Seed
	entries map[uint64][]codexRecordDecodeEntry
	bytes   int
}{seed: maphash.MakeSeed(), entries: map[uint64][]codexRecordDecodeEntry{}}

type codexRecordDecodeEntry struct {
	line   []byte
	record CodexRecord
	valid  bool
}

var unmarshalCodexRecord = json.Unmarshal

// DecodeCodexRecord reuses only a parsed row value. It does not cache rollout
// activity or make a cross-process status claim.
func DecodeCodexRecord(line []byte) (CodexRecord, bool) {
	key := maphash.Bytes(codexRecordDecodes.seed, line)
	codexRecordDecodes.Lock()
	for _, entry := range codexRecordDecodes.entries[key] {
		if bytes.Equal(entry.line, line) {
			codexRecordDecodes.Unlock()
			return entry.record, entry.valid
		}
	}
	codexRecordDecodes.Unlock()

	var record CodexRecord
	valid := unmarshalCodexRecord(line, &record) == nil
	if valid && len(record.Payload.Content) > 0 {
		record.Payload.Content = bytes.Clone(record.Payload.Content)
	}
	if len(line) > 256*1024 {
		return record, valid
	}
	codexRecordDecodes.Lock()
	if codexRecordDecodes.bytes+len(line) > 4*1024*1024 {
		codexRecordDecodes.entries = map[uint64][]codexRecordDecodeEntry{}
		codexRecordDecodes.bytes = 0
	}
	if codexRecordDecodes.bytes+len(line) <= 4*1024*1024 {
		copyOfLine := bytes.Clone(line)
		codexRecordDecodes.entries[key] = append(codexRecordDecodes.entries[key], codexRecordDecodeEntry{line: copyOfLine, record: record, valid: valid})
		codexRecordDecodes.bytes += len(copyOfLine)
	}
	codexRecordDecodes.Unlock()
	return record, valid
}
