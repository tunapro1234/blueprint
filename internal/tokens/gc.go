package tokens

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func GC(config Config) (GCStats, error) {
	config = config.normalized()
	unlock, err := lockStore(config.StoreDir)
	if err != nil {
		return GCStats{}, err
	}
	defer unlock()
	return gcLocked(config)
}

func gcLocked(config Config) (GCStats, error) {
	var stats GCStats
	now := config.Now().In(istanbul)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, istanbul)

	rawFiles, _ := filepath.Glob(filepath.Join(config.StoreDir, "raw", "*.jsonl"))
	for _, path := range rawFiles {
		day, ok := parseDayName(path)
		if !ok || !tierReady(config.StoreDir, day) {
			continue
		}
		if !day.After(today.AddDate(0, 0, -14)) {
			if freed, err := removeLogged(path, "raw retention", config.Log); err != nil {
				return stats, err
			} else {
				stats.Deleted++
				stats.BytesFreed += freed
			}
		} else if !day.After(today.AddDate(0, 0, -2)) {
			if err := gzipAtomic(path); err != nil {
				return stats, err
			}
			stats.Compressed++
		}
	}
	rawGzip, _ := filepath.Glob(filepath.Join(config.StoreDir, "raw", "*.jsonl.gz"))
	for _, path := range rawGzip {
		day, ok := parseDayName(path)
		if ok && !day.After(today.AddDate(0, 0, -14)) && tierReady(config.StoreDir, day) {
			if freed, err := removeLogged(path, "raw retention", config.Log); err != nil {
				return stats, err
			} else {
				stats.Deleted++
				stats.BytesFreed += freed
			}
		}
	}

	promptFiles, _ := filepath.Glob(filepath.Join(config.StoreDir, "prompts", "*.jsonl"))
	for _, path := range promptFiles {
		day, ok := parseDayName(path)
		if !ok {
			continue
		}
		if !day.After(today.AddDate(0, 0, -90)) {
			if freed, err := removeLogged(path, "prompt retention", config.Log); err != nil {
				return stats, err
			} else {
				stats.Deleted++
				stats.BytesFreed += freed
			}
		} else if !day.After(today.AddDate(0, 0, -7)) {
			if err := gzipAtomic(path); err != nil {
				return stats, err
			}
			stats.Compressed++
		}
	}
	promptGzip, _ := filepath.Glob(filepath.Join(config.StoreDir, "prompts", "*.jsonl.gz"))
	for _, path := range promptGzip {
		day, ok := parseDayName(path)
		if ok && !day.After(today.AddDate(0, 0, -90)) {
			if freed, err := removeLogged(path, "prompt retention", config.Log); err != nil {
				return stats, err
			} else {
				stats.Deleted++
				stats.BytesFreed += freed
			}
		}
	}

	seenStats, err := trimSeen(config, today)
	stats.Deleted += seenStats.Deleted
	stats.BytesFreed += seenStats.BytesFreed
	if err != nil {
		return stats, err
	}

	if err := trimHourly(config.StoreDir, today.AddDate(0, 0, -400)); err != nil {
		return stats, err
	}
	budgetStats, err := enforceBudget(config)
	stats.Deleted += budgetStats.Deleted
	stats.BytesFreed += budgetStats.BytesFreed
	if err != nil {
		return stats, err
	}
	stats.StoreBytes, err = storeSize(config.StoreDir)
	return stats, err
}

func trimSeen(config Config, today time.Time) (GCStats, error) {
	var stats GCStats
	files, _ := filepath.Glob(filepath.Join(config.StoreDir, "seen", "*.jsonl"))
	cutoff := today.AddDate(0, 0, -30)
	for _, path := range files {
		day, ok := parseDayName(path)
		reason := "seen retention"
		if !ok {
			base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
			if len(base) != 4 {
				continue
			}
			if _, err := time.Parse("2006", base); err != nil {
				continue
			}
			reason = "legacy yearly seen file; checkpoints preserved"
		} else if day.After(cutoff) {
			continue
		}
		freed, err := removeLogged(path, reason, config.Log)
		if err != nil {
			return stats, err
		}
		stats.Deleted++
		stats.BytesFreed += freed
	}
	return stats, nil
}

func tierReady(root string, day time.Time) bool {
	dayText := day.Format("2006-01-02")
	month := day.Format("2006-01")
	year := day.Format("2006")
	prompt := filepath.Join(root, "prompts", dayText+".jsonl")
	if _, err := os.Stat(prompt); err != nil {
		if _, gzipErr := os.Stat(prompt + ".gz"); gzipErr != nil {
			return false
		}
	}
	for _, path := range []string{
		filepath.Join(root, "hourly", month+".jsonl"),
		filepath.Join(root, "daily", year+".jsonl"),
	} {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}

func gzipAtomic(path string) error {
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	defer input.Close()
	target := path + ".gz"
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	writer := gzip.NewWriter(tmp)
	_, copyErr := io.Copy(writer, bufio.NewReader(input))
	closeGzipErr := writer.Close()
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	for _, candidate := range []error{copyErr, closeGzipErr, syncErr, closeErr} {
		if candidate != nil {
			return candidate
		}
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	return os.Remove(path)
}

func trimHourly(root string, cutoff time.Time) error {
	files, _ := filepath.Glob(filepath.Join(root, "hourly", "*.jsonl"))
	cutoffDay := cutoff.Format("2006-01-02")
	for _, path := range files {
		var rows []HourlyRecord
		if err := readJSONLines(path, &rows); err != nil {
			return err
		}
		kept := rows[:0]
		for _, row := range rows {
			if row.Day >= cutoffDay {
				kept = append(kept, row)
			}
		}
		if len(kept) == len(rows) {
			continue
		}
		if len(kept) == 0 {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if err := writeJSONLines(path, kept); err != nil {
			return err
		}
	}
	return nil
}

type agedFile struct {
	path string
	day  time.Time
}

func enforceBudget(config Config) (GCStats, error) {
	var stats GCStats
	size, err := storeSize(config.StoreDir)
	if err != nil {
		return stats, err
	}
	if size <= config.BudgetBytes {
		stats.StoreBytes = size
		return stats, nil
	}
	var candidates []agedFile
	for _, directory := range []string{"raw", "prompts"} {
		files, _ := filepath.Glob(filepath.Join(config.StoreDir, directory, "*.jsonl*"))
		group := make([]agedFile, 0, len(files))
		for _, path := range files {
			day, ok := parseDayName(path)
			if !ok || (directory == "raw" && !tierReady(config.StoreDir, day)) {
				continue
			}
			group = append(group, agedFile{path, day})
		}
		sort.Slice(group, func(i, j int) bool {
			if group[i].day.Equal(group[j].day) {
				return group[i].path < group[j].path
			}
			return group[i].day.Before(group[j].day)
		})
		candidates = append(candidates, group...)
	}
	for _, candidate := range candidates {
		if size <= config.BudgetBytes {
			break
		}
		freed, err := removeLogged(candidate.path, "store budget", config.Log)
		if err != nil {
			return stats, err
		}
		size -= freed
		stats.Deleted++
		stats.BytesFreed += freed
	}
	stats.StoreBytes = size
	if size > config.HardLimitBytes {
		return stats, fmt.Errorf("token store is %d bytes after GC, above hard limit %d", size, config.HardLimitBytes)
	}
	return stats, nil
}

func removeLogged(path, reason string, log io.Writer) (int64, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err := os.Remove(path); err != nil {
		return 0, err
	}
	fmt.Fprintf(log, "tokens gc: deleted %s (%d bytes, %s)\n", path, info.Size(), reason)
	return info.Size(), nil
}

func storeSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || strings.Contains(entry.Name(), ".tmp-") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return total, err
}
