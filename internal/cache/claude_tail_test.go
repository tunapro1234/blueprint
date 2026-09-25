package cache

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var referenceNow = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

// referenceReadClaudePath is the whole-window read the incremental cache must
// reproduce exactly.
func referenceReadClaudePath(path string) State {
	file, err := os.Open(path)
	if err != nil {
		return State{LastHumanAge: -1}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return State{LastHumanAge: -1}
	}
	data, ok := readTail(file, info.Size())
	if !ok {
		return State{LastHumanAge: -1}
	}

	var usageTime, humanTime, compactTime time.Time
	var cacheTime time.Time
	var cacheTTL time.Duration
	messageStarts := make(map[string]time.Time)
	ctxTokens := 0
	model, effort := "", ""
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record struct {
			Type             string `json:"type"`
			Subtype          string `json:"subtype"`
			IsCompactSummary bool   `json:"isCompactSummary"`
			IsSidechain      bool   `json:"isSidechain"`
			CompactMetadata  *struct {
				PostTokens *int `json:"postTokens"`
			} `json:"compactMetadata"`
			Timestamp string          `json:"timestamp"`
			Message   json.RawMessage `json:"message"`
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			Effort    json.RawMessage `json:"effort"`
		}
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		if record.IsSidechain {
			continue
		}
		if record.Type == "system" && record.Subtype == "compact_boundary" {
			compactTime, _ = parseTime(record.Timestamp)
			usageTime, ctxTokens = time.Time{}, 0
			cacheTime, cacheTTL = time.Time{}, 0
			if record.CompactMetadata != nil && record.CompactMetadata.PostTokens != nil && *record.CompactMetadata.PostTokens >= 0 {
				usageTime, ctxTokens = compactTime, *record.CompactMetadata.PostTokens
			}
			continue
		}
		var message struct {
			ID      string          `json:"id"`
			Role    string          `json:"role"`
			Model   string          `json:"model"`
			Content json.RawMessage `json:"content"`
			Usage   *struct {
				Input         int `json:"input_tokens"`
				CacheRead     int `json:"cache_read_input_tokens"`
				CacheCreation int `json:"cache_creation_input_tokens"`
				Creation      struct {
					Hour int `json:"ephemeral_1h_input_tokens"`
					Five int `json:"ephemeral_5m_input_tokens"`
				} `json:"cache_creation"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(record.Message, &message)
		if stamp, ok := parseTime(record.Timestamp); ok && message.ID != "" {
			if previous, exists := messageStarts[message.ID]; !exists || stamp.Before(previous) {
				messageStarts[message.ID] = stamp
			}
		}
		// "<synthetic>" marks interrupt/error placeholders, not a real turn.
		if message.Model != "" && message.Model != "<synthetic>" {
			model = message.Model
			// Claude stores effort on the outer assistant record. Keep it paired
			// with this model/turn; an older turn or another agent's global
			// default cannot fill an absent value. Unknown shapes don't discard
			// otherwise valid token usage in the record.
			effort = ""
			if record.Type == "assistant" || message.Role == "assistant" {
				_ = json.Unmarshal(record.Effort, &effort)
			}
		}
		if message.Usage != nil {
			if timestamp, ok := parseTime(record.Timestamp); ok && (compactTime.IsZero() || timestamp.After(compactTime)) {
				usageTime = timestamp
				ctxTokens = message.Usage.Input + message.Usage.CacheRead + message.Usage.CacheCreation
				cacheTTL, cacheTime = 0, timestamp
				if start, ok := messageStarts[message.ID]; ok {
					cacheTime = start
				}
				// Mixed/absent TTLs and read-only hits do not establish the
				// lifetime of the current prefix. Do not infer it from auth/model.
				c := message.Usage.Creation
				if c.Hour > 0 && c.Five == 0 && c.Hour == message.Usage.CacheCreation {
					cacheTTL = time.Hour
				} else if c.Five > 0 && c.Hour == 0 && c.Five == message.Usage.CacheCreation {
					cacheTTL = 5 * time.Minute
				}
			}
		}
		if record.Type != "user" && record.Role != "user" && message.Role != "user" {
			continue
		}
		content := message.Content
		if len(content) == 0 {
			content = record.Content
		}
		text := contentText(content)
		if record.IsCompactSummary || strings.TrimSpace(text) == "" || automatic(text) {
			continue
		}
		if timestamp, ok := parseTime(record.Timestamp); ok {
			humanTime = timestamp
		}
	}

	result := State{CtxTokens: ctxTokens, LastHumanAge: -1, Model: model, Effort: effort, Path: path, ThreadID: strings.TrimSuffix(filepath.Base(path), ".jsonl"), UsageAt: usageTime}
	now := referenceNow
	if cacheTTL > 0 {
		result.CacheTTL, result.CacheAge = cacheTTL, age(now, cacheTime)
	}
	if !usageTime.IsZero() {
		result.Known = true
		result.Age = age(now, usageTime)
	}
	if !humanTime.IsZero() {
		result.LastHumanAge = age(now, humanTime)
	}
	return result
}

func randomClaudeRow(rng *rand.Rand, n int, big int) string {
	stamp := referenceNow.Add(-time.Hour + time.Duration(n)*time.Second).Format(time.RFC3339Nano)
	if rng.Intn(9) == 0 {
		// Records written out of order and without a timestamp.
		stamp = []string{"", "not a time", referenceNow.Add(-2 * time.Hour).Format(time.RFC3339Nano)}[rng.Intn(3)]
	}
	id := fmt.Sprintf("msg_%d", rng.Intn(6))
	record := map[string]any{"timestamp": stamp}
	switch rng.Intn(13) {
	case 0:
		record["type"], record["subtype"] = "system", "compact_boundary"
		switch rng.Intn(3) {
		case 0:
			record["compactMetadata"] = map[string]any{"postTokens": rng.Intn(50000)}
		case 1:
			record["compactMetadata"] = map[string]any{"postTokens": -1}
		}
	case 1, 2, 3:
		creation := map[string]any{}
		cc := rng.Intn(3) * 1000
		switch rng.Intn(4) {
		case 0:
			creation["ephemeral_1h_input_tokens"] = cc
		case 1:
			creation["ephemeral_5m_input_tokens"] = cc
		case 2:
			creation["ephemeral_1h_input_tokens"], creation["ephemeral_5m_input_tokens"] = cc/2, cc/2
		}
		message := map[string]any{"id": id, "role": "assistant", "model": []string{"claude-a", "claude-b", "<synthetic>", ""}[rng.Intn(4)]}
		if rng.Intn(3) > 0 {
			message["usage"] = map[string]any{"input_tokens": rng.Intn(100), "cache_read_input_tokens": rng.Intn(90000), "cache_creation_input_tokens": cc, "cache_creation": creation}
		}
		record["type"], record["message"] = "assistant", message
		switch rng.Intn(4) {
		case 0:
			record["effort"] = "high"
		case 1:
			record["effort"] = 3
		case 2:
			record["effort"] = nil
		}
	case 4, 5:
		text := []string{"please fix it", "[ANNOUNCE] x", "health-watch: y", "  ", "[ 3 ANNOUNCEMENT (x)"}[rng.Intn(5)]
		record["type"] = "user"
		if rng.Intn(2) == 0 {
			record["message"] = map[string]any{"role": "user", "content": text}
		} else {
			record["message"] = map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": text}}}
		}
		if rng.Intn(6) == 0 {
			record["isCompactSummary"] = true
		}
	case 6:
		record["type"], record["role"], record["content"] = "queue", "user", "typed ahead"
	case 7:
		record["type"], record["isSidechain"] = "assistant", true
		record["message"] = map[string]any{"id": id, "model": "side", "usage": map[string]any{"input_tokens": 1}}
	case 8:
		return `{"type":"assistant","message":` // torn
	case 9:
		return []string{"", "   ", "\t", "garbage"}[rng.Intn(4)]
	case 10:
		record["type"] = "user"
		record["message"] = map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "text": strings.Repeat("r", rng.Intn(big))}}}
	case 11:
		record["type"], record["message"] = "assistant", "not an object"
	default:
		record["type"], record["message"] = "assistant", map[string]any{"id": 5, "model": "claude-c", "usage": map[string]any{"input_tokens": "x"}}
	}
	line, err := json.Marshal(record)
	if err != nil {
		panic(err)
	}
	return string(line)
}

func TestCachedClaudeTailMatchesWindowRead(t *testing.T) {
	saved := tailSize
	tailSize = 4096
	defer func() { tailSize = saved }()
	rng := rand.New(rand.NewSource(11))
	dir := t.TempDir()
	reused := 0
	for history := 0; history < 150; history++ {
		path := filepath.Join(dir, fmt.Sprintf("%08d-0000-0000-0000-000000000000.jsonl", history))
		var content bytes.Buffer
		rows := 0
		// Some histories hold records longer than the window, forcing the
		// widened read.
		big := 200
		if history%5 == 0 {
			big = 3 * int(tailSize)
		}
		for step := 0; step < 14; step++ {
			switch rng.Intn(12) {
			case 0:
				content.Reset()
			case 1:
				// A rewrite that keeps the size but changes an early byte.
				if content.Len() > 0 {
					b := content.Bytes()
					b[rng.Intn(len(b))] = 'Z'
				}
			}
			for i := rng.Intn([]int{3, 30, 120}[rng.Intn(3)]); i > 0; i-- {
				rows++
				content.WriteString(randomClaudeRow(rng, rows, big) + "\n")
			}
			written := content.Bytes()
			if rng.Intn(3) == 0 {
				rows++
				partial := randomClaudeRow(rng, rows, big)
				if rng.Intn(2) == 0 && len(partial) > 0 {
					partial = partial[:rng.Intn(len(partial))]
				}
				written = append(append([]byte{}, written...), partial...)
				if rng.Intn(2) == 0 {
					content.WriteString(partial + "\n")
				}
			}
			if err := os.WriteFile(path, written, 0o600); err != nil {
				t.Fatal(err)
			}
			stamp := time.Unix(1_700_000_000+int64(history*100+step), 0)
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			claudeTails.Lock()
			entry, cached := claudeTails.entries[path]
			claudeTails.Unlock()
			if cached && entry.size != int64(len(written)) && len(entry.decoded) > 0 {
				reused++
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			metrics, ok := cachedClaudeTail(file, info)
			file.Close()
			got := State{LastHumanAge: -1}
			if ok {
				got = metrics.state(path, referenceNow)
			}
			if want := referenceReadClaudePath(path); !reflect.DeepEqual(got, want) {
				t.Fatalf("history %d step %d:\n got %+v\nwant %+v", history, step, got, want)
			}
		}
	}
	if reused < 500 {
		t.Fatalf("only %d reads could reuse decoded records", reused)
	}
	t.Logf("%d reads reused decoded records", reused)
}
