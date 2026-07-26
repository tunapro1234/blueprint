package tokens

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

func Collect(config Config) (CollectStats, error) {
	config = config.normalized()
	var stats CollectStats
	unlock, err := lockStore(config.StoreDir)
	if err != nil {
		return stats, err
	}
	defer unlock()
	if _, err := gcLocked(config); err != nil {
		return stats, err
	}

	checkpoints := map[string]checkpoint{}
	if err := readJSON(filepath.Join(config.StoreDir, "checkpoints.json"), &checkpoints); err != nil && !errors.Is(err, os.ErrNotExist) {
		return stats, fmt.Errorf("read checkpoints: %w", err)
	}
	contexts := contextFile{Sources: map[string]sourceContext{}}
	if err := readJSON(filepath.Join(config.StoreDir, "contexts.json"), &contexts); err != nil && !errors.Is(err, os.ErrNotExist) {
		return stats, fmt.Errorf("read source contexts: %w", err)
	}
	if contexts.Sources == nil {
		contexts.Sources = map[string]sourceContext{}
	}
	names := loadResolver(config.Agentbooks)
	liveSources := map[string]bool{}

	var candidates []RawRecord
	for _, source := range []struct {
		root string
		kind string
	}{
		{config.ClaudeRoot, "claude"},
		{config.CodexRoot, "codex"},
	} {
		files, err := discoverFiles(source.root, source.kind)
		if err != nil {
			return stats, fmt.Errorf("discover %s logs: %w", source.kind, err)
		}
		for _, path := range files {
			liveSources[path] = true
			stats.Files++
			cp := checkpoints[path]
			ctx := contexts.Sources[path]
			var rows []RawRecord
			var next checkpoint
			var nextContext sourceContext
			if source.kind == "claude" {
				rows, next, nextContext, err = scanClaude(path, cp, ctx, names)
			} else {
				rows, next, nextContext, err = scanCodex(path, cp, ctx, names)
			}
			if err != nil {
				return stats, fmt.Errorf("read %s: %w", path, err)
			}
			candidates = append(candidates, rows...)
			checkpoints[path] = next
			contexts.Sources[path] = nextContext
		}
	}

	seenByDay := map[string]map[string]bool{}
	newSeenByDay := map[string][]string{}
	dayCandidates := map[string][]RawRecord{}
	for _, row := range candidates {
		seen := seenByDay[row.Day]
		if seen == nil {
			seen, err = loadSeen(filepath.Join(config.StoreDir, "seen", row.Day+".jsonl"))
			if err != nil {
				return stats, err
			}
			seenByDay[row.Day] = seen
		}
		if seen[row.Key] {
			stats.Deduped++
			continue
		}
		seen[row.Key] = true
		newSeenByDay[row.Day] = append(newSeenByDay[row.Day], row.Key)
		dayCandidates[row.Day] = append(dayCandidates[row.Day], row)
	}

	dayRows := map[string][]RawRecord{}
	for day, incoming := range dayCandidates {
		existing, err := readRawDay(config.StoreDir, day)
		if err != nil {
			return stats, err
		}
		byKey := make(map[string]RawRecord, len(existing)+len(incoming))
		for _, row := range existing {
			row = names.normalizeRawAgent(row)
			byKey[row.Key] = row
		}
		for _, row := range incoming {
			if _, exists := byKey[row.Key]; exists {
				stats.Deduped++
				continue
			}
			byKey[row.Key] = row
			stats.Added++
		}
		merged := make([]RawRecord, 0, len(byKey))
		for _, row := range byKey {
			merged = append(merged, row)
		}
		sortRaw(merged)
		dayRows[day] = merged
	}

	if len(dayRows) > 0 {
		if err := writeRollups(config.StoreDir, dayRows); err != nil {
			return stats, err
		}
		for day, rows := range dayRows {
			path := filepath.Join(config.StoreDir, "raw", day+".jsonl")
			if err := writeJSONLines(path, rows); err != nil {
				return stats, fmt.Errorf("write raw day %s: %w", day, err)
			}
			_ = os.Remove(path + ".gz")
		}
	}
	for day, keys := range newSeenByDay {
		if len(keys) == 0 {
			continue
		}
		all := seenByDay[day]
		sorted := make([]string, 0, len(all))
		for key := range all {
			sorted = append(sorted, key)
		}
		sort.Strings(sorted)
		if err := writeSeen(filepath.Join(config.StoreDir, "seen", day+".jsonl"), sorted); err != nil {
			return stats, err
		}
	}
	pruneContexts(&contexts, liveSources, config.Now(), config.Log)
	if err := writeCompactJSON(filepath.Join(config.StoreDir, "contexts.json"), contexts); err != nil {
		return stats, fmt.Errorf("write source contexts: %w", err)
	}
	if err := writeJSON(filepath.Join(config.StoreDir, "checkpoints.json"), checkpoints); err != nil {
		return stats, fmt.Errorf("write checkpoints: %w", err)
	}
	stats.Days = len(dayRows)
	if _, err := gcLocked(config); err != nil {
		return stats, err
	}
	return stats, nil
}

func pruneContexts(contexts *contextFile, live map[string]bool, now time.Time, log io.Writer) {
	cutoff := now.In(istanbul).AddDate(0, 0, -14)
	droppedNodes := 0
	droppedSources := 0
	for path, context := range contexts.Sources {
		if !live[path] {
			droppedNodes += len(context.Nodes)
			droppedSources++
			delete(contexts.Sources, path)
			continue
		}
		kept := make([]nodeRef, 0, len(context.Nodes))
		for _, node := range context.Nodes {
			if node.TS != "" {
				if ts, ok := parseTimestamp(node.TS); ok && ts.Before(cutoff) {
					droppedNodes++
					continue
				}
			}
			kept = append(kept, node)
		}
		if len(kept) > 2000 {
			droppedNodes += len(kept) - 2000
			kept = kept[len(kept)-2000:]
		}
		context.Nodes = kept
		normalizeContextPrompts(&context)
		contexts.Sources[path] = context
	}
	if droppedNodes > 0 || droppedSources > 0 {
		fmt.Fprintf(log, "tokens collect: pruned %d context nodes and %d missing sources\n", droppedNodes, droppedSources)
	}
}

func writeRollups(root string, days map[string][]RawRecord) error {
	prompts := map[string][]PromptRecord{}
	hours := map[string][]HourlyRecord{}
	dailies := map[string][]DailyRecord{}
	for day, rows := range days {
		prompts[day], hours[day], dailies[day] = rollup(rows)
	}
	for day, records := range prompts {
		path := filepath.Join(root, "prompts", day+".jsonl")
		if err := writeJSONLines(path, records); err != nil {
			return fmt.Errorf("write prompt rollup %s: %w", day, err)
		}
		_ = os.Remove(path + ".gz")
	}
	months := map[string]map[string][]HourlyRecord{}
	years := map[string]map[string][]DailyRecord{}
	for day, records := range hours {
		month := day[:7]
		if months[month] == nil {
			months[month] = map[string][]HourlyRecord{}
		}
		months[month][day] = records
	}
	for day, records := range dailies {
		year := day[:4]
		if years[year] == nil {
			years[year] = map[string][]DailyRecord{}
		}
		years[year][day] = records
	}
	for month, replacements := range months {
		path := filepath.Join(root, "hourly", month+".jsonl")
		var existing []HourlyRecord
		if err := readJSONLines(path, &existing); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		merged := existing[:0]
		for _, row := range existing {
			if _, replace := replacements[row.Day]; !replace {
				merged = append(merged, row)
			}
		}
		for _, rows := range replacements {
			merged = append(merged, rows...)
		}
		sortHourly(merged)
		if err := writeJSONLines(path, merged); err != nil {
			return fmt.Errorf("write hourly rollup %s: %w", month, err)
		}
	}
	for year, replacements := range years {
		path := filepath.Join(root, "daily", year+".jsonl")
		var existing []DailyRecord
		if err := readJSONLines(path, &existing); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		merged := existing[:0]
		for _, row := range existing {
			if _, replace := replacements[row.Day]; !replace {
				merged = append(merged, row)
			}
		}
		for _, rows := range replacements {
			merged = append(merged, rows...)
		}
		sortDaily(merged)
		if err := writeJSONLines(path, merged); err != nil {
			return fmt.Errorf("write daily rollup %s: %w", year, err)
		}
	}
	return nil
}

func rollup(rows []RawRecord) ([]PromptRecord, []HourlyRecord, []DailyRecord) {
	type promptKey struct {
		Agent, Src, Prompt string
	}
	type hourKey struct {
		Agent, Src, Model string
		Hour              int
	}
	type dayKey struct {
		Agent, Src, Model string
	}
	promptMap := map[promptKey]*PromptRecord{}
	hourMap := map[hourKey]*HourlyRecord{}
	dayMap := map[dayKey]*DailyRecord{}
	for _, raw := range rows {
		agent := raw.rollupAgent()
		pk := promptKey{agent, raw.Src, raw.PromptID}
		prompt := promptMap[pk]
		if prompt == nil {
			prompt = &PromptRecord{
				Day: raw.Day, PromptID: raw.PromptID, FirstTS: raw.TS, Agent: agent,
				Src: raw.Src, Model: raw.Model, Preview: truncate(raw.Prompt, 200),
			}
			promptMap[pk] = prompt
		} else {
			if raw.TS < prompt.FirstTS {
				prompt.FirstTS = raw.TS
			}
			if prompt.Model != raw.Model {
				prompt.Model = "multiple"
			}
		}
		addPrompt(prompt, raw)

		hk := hourKey{agent, raw.Src, raw.Model, raw.Hour}
		hour := hourMap[hk]
		if hour == nil {
			hour = &HourlyRecord{Day: raw.Day, Hour: raw.Hour, Agent: agent, Src: raw.Src, Model: raw.Model}
			hourMap[hk] = hour
		}
		addHourly(hour, raw)

		dk := dayKey{agent, raw.Src, raw.Model}
		daily := dayMap[dk]
		if daily == nil {
			daily = &DailyRecord{Day: raw.Day, Agent: agent, Src: raw.Src, Model: raw.Model}
			dayMap[dk] = daily
		}
		addDaily(daily, raw)
	}
	prompts := make([]PromptRecord, 0, len(promptMap))
	for _, row := range promptMap {
		prompts = append(prompts, *row)
	}
	hourly := make([]HourlyRecord, 0, len(hourMap))
	for _, row := range hourMap {
		hourly = append(hourly, *row)
	}
	daily := make([]DailyRecord, 0, len(dayMap))
	for _, row := range dayMap {
		daily = append(daily, *row)
	}
	sort.Slice(prompts, func(i, j int) bool {
		if prompts[i].FirstTS == prompts[j].FirstTS {
			return prompts[i].Agent < prompts[j].Agent
		}
		return prompts[i].FirstTS < prompts[j].FirstTS
	})
	sortHourly(hourly)
	sortDaily(daily)
	return prompts, hourly, daily
}

func addPrompt(target *PromptRecord, raw RawRecord) {
	target.Requests += raw.Requests
	target.In += raw.In
	target.Out += raw.Out
	target.CacheWrite += raw.CacheWrite
	target.CacheRead += raw.CacheRead
}

func addHourly(target *HourlyRecord, raw RawRecord) {
	target.Requests += raw.Requests
	target.In += raw.In
	target.Out += raw.Out
	target.CacheWrite += raw.CacheWrite
	target.CacheRead += raw.CacheRead
}

func addDaily(target *DailyRecord, raw RawRecord) {
	target.Requests += raw.Requests
	target.In += raw.In
	target.Out += raw.Out
	target.CacheWrite += raw.CacheWrite
	target.CacheRead += raw.CacheRead
}

func readRawDay(root, day string) ([]RawRecord, error) {
	var rows []RawRecord
	path := filepath.Join(root, "raw", day+".jsonl")
	err := readJSONLines(path, &rows)
	if errors.Is(err, os.ErrNotExist) {
		err = readGzipJSONLines(path+".gz", &rows)
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return rows, err
}

func loadSeen(path string) (map[string]bool, error) {
	result := map[string]bool{}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var row struct {
			Key string `json:"key"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) == nil && row.Key != "" {
			result[row.Key] = true
		}
	}
	return result, scanner.Err()
}

func writeSeen(path string, keys []string) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	for _, key := range keys {
		if err := encoder.Encode(struct {
			Key string `json:"key"`
		}{key}); err != nil {
			return err
		}
	}
	return writeAtomic(path, buffer.Bytes(), 0o644)
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeAtomic(path, data, 0o644)
}

func writeCompactJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeAtomic(path, data, 0o644)
}

func readJSONLines[T any](path string, target *[]T) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return decodeJSONLines(file, target)
}

func readGzipJSONLines[T any](path string, target *[]T) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer reader.Close()
	return decodeJSONLines(reader, target)
}

func decodeJSONLines[T any](reader io.Reader, target *[]T) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var row T
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return err
		}
		*target = append(*target, row)
	}
	return scanner.Err()
}

func writeJSONLines[T any](path string, rows []T) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	for _, row := range rows {
		if err := encoder.Encode(row); err != nil {
			return err
		}
	}
	return writeAtomic(path, buffer.Bytes(), 0o644)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err := file.Chmod(mode); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func lockStore(root string) (func(), error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

func sortRaw(rows []RawRecord) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TS == rows[j].TS {
			return rows[i].Key < rows[j].Key
		}
		return rows[i].TS < rows[j].TS
	})
}

func sortHourly(rows []HourlyRecord) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Day != b.Day {
			return a.Day < b.Day
		}
		if a.Hour != b.Hour {
			return a.Hour < b.Hour
		}
		if a.Agent != b.Agent {
			return a.Agent < b.Agent
		}
		if a.Src != b.Src {
			return a.Src < b.Src
		}
		return a.Model < b.Model
	})
}

func sortDaily(rows []DailyRecord) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Day != b.Day {
			return a.Day < b.Day
		}
		if a.Agent != b.Agent {
			return a.Agent < b.Agent
		}
		if a.Src != b.Src {
			return a.Src < b.Src
		}
		return a.Model < b.Model
	})
}

func parseDayName(path string) (time.Time, bool) {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".gz")
	base = strings.TrimSuffix(base, ".jsonl")
	day, err := time.ParseInLocation("2006-01-02", base, istanbul)
	return day, err == nil
}
