package tokens

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrNotCollected means the store has never completed a collection, so it
// cannot vouch for any agent's usage.
var ErrNotCollected = errors.New("token store has no completed collection")

// Usage is token usage summed over raw records.
type Usage struct {
	Requests   int64
	In         int64
	Out        int64
	CacheWrite int64
	CacheRead  int64
	// Seen is true when the agent has at least one record in the day files
	// searched (the day before since through the day of until), inside the
	// interval or not. A metered agent that was working always leaves
	// records; none at all means the collector attributes its transcript to
	// another name (or has not collected it yet), so zero usage would be a
	// guess.
	Seen bool
}

// AgentUsage sums the collected raw usage attributed to agent, its own
// transcript plus the subagent and Claude workflow transcripts it owns, with a
// timestamp in [since, until). It only reads the store: raw day files are
// replaced by atomic rename, so no lock is taken and the collector is never
// raced or blocked. asOf is the end of the last completed collection
// (checkpoints.json is rewritten by every Collect); usage written to a
// transcript after it is not counted yet.
//
// Attribution is the collector's: a record belongs to agent when its rollup
// name (the owner for subagent and workflow transcripts, else the agent
// label) equals agent exactly.
func AgentUsage(storeDir, agent string, since, until time.Time) (Usage, time.Time, error) {
	info, err := os.Stat(filepath.Join(storeDir, "checkpoints.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Usage{}, time.Time{}, ErrNotCollected
	}
	if err != nil {
		return Usage{}, time.Time{}, err
	}
	asOf := info.ModTime()
	var usage Usage
	if !until.After(since) {
		return usage, asOf, nil
	}
	first := since.In(istanbul)
	// Start one day early: that day is only searched for Seen, so an interval
	// starting just after midnight still finds the agent's earlier records.
	day := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, istanbul).AddDate(0, 0, -1)
	for ; day.Before(until); day = day.AddDate(0, 0, 1) {
		rows, err := cachedRawDay(storeDir, day.Format("2006-01-02"))
		if err != nil {
			return Usage{}, time.Time{}, err
		}
		for _, row := range rows {
			if row.agent != agent {
				continue
			}
			usage.Seen = true
			if row.at.Before(since) || !row.at.Before(until) {
				continue
			}
			usage.Requests += row.requests
			usage.In += row.in
			usage.Out += row.out
			usage.CacheWrite += row.cacheWrite
			usage.CacheRead += row.cacheRead
		}
	}
	return usage, asOf, nil
}

type usageRow struct {
	at         time.Time
	agent      string
	requests   int64
	in         int64
	out        int64
	cacheWrite int64
	cacheRead  int64
}

type rawDayEntry struct {
	path  string
	size  int64
	mtime time.Time
	rows  []usageRow
}

// rawDayCache keeps the last parse of each raw day file read by this process.
// A day file is re-read only after the collector replaced it.
var rawDayCache = struct {
	sync.Mutex
	entries map[string]rawDayEntry
}{entries: map[string]rawDayEntry{}}

func cachedRawDay(storeDir, day string) ([]usageRow, error) {
	path := filepath.Join(storeDir, "raw", day+".jsonl")
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		path += ".gz"
		info, err = os.Stat(path)
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	key := filepath.Join(storeDir, "raw", day)
	rawDayCache.Lock()
	entry, ok := rawDayCache.entries[key]
	rawDayCache.Unlock()
	if ok && entry.path == path && entry.size == info.Size() && entry.mtime.Equal(info.ModTime()) {
		return entry.rows, nil
	}
	var raw []RawRecord
	if filepath.Ext(path) == ".gz" {
		err = readGzipJSONLines(path, &raw)
	} else {
		err = readJSONLines(path, &raw)
	}
	if errors.Is(err, os.ErrNotExist) {
		// Compressed or removed by GC between the stat and the read.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows := make([]usageRow, 0, len(raw))
	for _, record := range raw {
		at, err := time.Parse(time.RFC3339Nano, record.TS)
		if err != nil {
			continue
		}
		rows = append(rows, usageRow{at: at, agent: record.rollupAgent(), requests: record.Requests, in: record.In, out: record.Out, cacheWrite: record.CacheWrite, cacheRead: record.CacheRead})
	}
	rawDayCache.Lock()
	rawDayCache.entries[key] = rawDayEntry{path: path, size: info.Size(), mtime: info.ModTime(), rows: rows}
	rawDayCache.Unlock()
	return rows, nil
}
