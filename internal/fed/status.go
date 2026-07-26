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
	file, err := os.Open(filepath.Join(stateDir, "fed", "log.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]int{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	counts := map[string]int{}
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
	return counts, scanner.Err()
}
