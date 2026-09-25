package book

import (
	"bytes"
	"encoding/json"
	"hash/maphash"
	"sync"
)

var turnRecordDecodes = struct {
	sync.Mutex
	seed    maphash.Seed
	entries map[uint64][]turnRecordDecodeEntry
	bytes   int
}{seed: maphash.MakeSeed(), entries: map[uint64][]turnRecordDecodeEntry{}}

type turnRecordDecodeEntry struct {
	line   []byte
	record turnRecord
	valid  bool
}

var unmarshalTurnRecord = json.Unmarshal

// decodeTurnRecord shares only parsed row values between the phase and
// awaiting-user scanners in this process. It caches no activity decision.
func decodeTurnRecord(line []byte) (turnRecord, bool) {
	key := maphash.Bytes(turnRecordDecodes.seed, line)
	turnRecordDecodes.Lock()
	for _, entry := range turnRecordDecodes.entries[key] {
		if bytes.Equal(entry.line, line) {
			turnRecordDecodes.Unlock()
			return entry.record, entry.valid
		}
	}
	turnRecordDecodes.Unlock()

	var record turnRecord
	valid := unmarshalTurnRecord(line, &record) == nil
	if valid && len(record.Message.Content) > 0 {
		record.Message.Content = bytes.Clone(record.Message.Content)
	}
	if len(line) > 256*1024 {
		return record, valid
	}
	turnRecordDecodes.Lock()
	if turnRecordDecodes.bytes+len(line) > 4*1024*1024 {
		turnRecordDecodes.entries = map[uint64][]turnRecordDecodeEntry{}
		turnRecordDecodes.bytes = 0
	}
	if turnRecordDecodes.bytes+len(line) <= 4*1024*1024 {
		copyOfLine := bytes.Clone(line)
		turnRecordDecodes.entries[key] = append(turnRecordDecodes.entries[key], turnRecordDecodeEntry{line: copyOfLine, record: record, valid: valid})
		turnRecordDecodes.bytes += len(copyOfLine)
	}
	turnRecordDecodes.Unlock()
	return record, valid
}
