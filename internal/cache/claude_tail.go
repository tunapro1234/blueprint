package cache

import (
	"bytes"
	"hash/maphash"
	"os"
	"sync"
	"time"
)

// claudeTails caches decoded transcript windows by path. Status and the bar
// renderer read every Claude agent's transcript on each refresh. Reading the
// window is cheap; decoding its records is not, and a working transcript only
// appends, so records are decoded once and reused by the hash of their bytes.
var claudeTails = struct {
	sync.Mutex
	entries map[string]claudeTailEntry
}{entries: map[string]claudeTailEntry{}}

type claudeTailEntry struct {
	size     int64
	modified time.Time
	metrics  claudeMetrics
	decoded  map[uint64]claudeRow
}

var claudeRowSeed = maphash.MakeSeed()

// cachedClaudeTail folds exactly the records readClaudeWindow does. Only the
// decode of a record already seen at this path is skipped; a record is a pure
// function of its bytes, so any rewrite of the file is still read correctly.
func cachedClaudeTail(file *os.File, info os.FileInfo) (claudeMetrics, bool) {
	path, size := file.Name(), info.Size()
	claudeTails.Lock()
	entry, ok := claudeTails.entries[path]
	claudeTails.Unlock()
	if ok && entry.size == size && entry.modified.Equal(info.ModTime()) {
		return entry.metrics, true
	}
	data, read := readTail(file, size)
	if !read {
		forgetClaudeTail(path)
		return claudeMetrics{}, false
	}
	decoded := make(map[uint64]claudeRow, len(entry.decoded)+16)
	fold := newClaudeFold()
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		key := maphash.Bytes(claudeRowSeed, line)
		row, seen := decoded[key]
		if !seen {
			if row, seen = entry.decoded[key]; !seen {
				row = decodeClaudeRow(line)
			}
			decoded[key] = row
		}
		fold.add(row)
	}
	metrics := fold.metrics()
	claudeTails.Lock()
	if len(claudeTails.entries) > 256 {
		claudeTails.entries = map[string]claudeTailEntry{}
	}
	claudeTails.entries[path] = claudeTailEntry{size: size, modified: info.ModTime(), metrics: metrics, decoded: decoded}
	claudeTails.Unlock()
	return metrics, true
}

func forgetClaudeTail(path string) {
	claudeTails.Lock()
	delete(claudeTails.entries, path)
	claudeTails.Unlock()
}
