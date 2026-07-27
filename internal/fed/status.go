package fed

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// RecentRateCounts reconstructs visible one-hour send counts from the audit
// trail for the CLI. The authoritative limiter remains in daemon memory.
func RecentRateCounts(stateDir string, now time.Time) (map[string]int, error) {
	counts := map[string]int{}
	for _, name := range []string{"log.jsonl.1", "log.jsonl"} {
		if err := addRecentRateCounts(filepath.Join(stateDir, "fed", name), now, counts); err != nil {
			return nil, err
		}
	}
	return counts, nil
}

func addRecentRateCounts(path string, now time.Time, counts map[string]int) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	cutoff := now.Add(-time.Hour)
	for scanner.Scan() {
		var entry auditEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Endpoint != "/v1/send" || entry.Peer == "" {
			continue
		}
		when, err := time.Parse(time.RFC3339Nano, entry.TS)
		if err == nil && !when.Before(cutoff) && (entry.Result == 200 || entry.Result == 500) {
			counts[entry.Peer]++
		}
	}
	return scanner.Err()
}
