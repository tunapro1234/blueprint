package cache

import (
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

// referenceCodexScan is the whole-file reverse scan the incremental cache must
// reproduce exactly.
func referenceCodexScan(path string) codexScan {
	result := State{LastHumanAge: -1, Path: path, ThreadID: CodexID(path)}
	var usageTime, humanTime, turnTime time.Time
	modelSeen, humanSeen, turnSeen, usageSeen := false, false, false, false
	ScanCodexReverse(path, func(line []byte) bool {
		var r struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Payload   struct {
				ID          string          `json:"id"`
				Type        string          `json:"type"`
				Role        string          `json:"role"`
				Model       string          `json:"model"`
				Effort      string          `json:"effort"`
				ServiceTier string          `json:"service_tier"`
				Content     json.RawMessage `json:"content"`
				Info        *struct {
					Last *struct {
						Total int `json:"total_tokens"`
					} `json:"last_token_usage"`
					Window int `json:"model_context_window"`
				} `json:"info"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &r) != nil {
			return true
		}
		if r.Type == "session_meta" {
			result.ThreadID = r.Payload.ID
		}
		if r.Type == "turn_context" && !modelSeen {
			result.Model, result.Effort, result.ServiceTier = r.Payload.Model, r.Payload.Effort, r.Payload.ServiceTier
			modelSeen = true
		}
		if r.Type == "compacted" || (r.Type == "event_msg" && r.Payload.Type == "context_compacted") {
			usageSeen = true
		}
		if r.Type == "event_msg" {
			if r.Payload.Type == "token_count" && !usageSeen && r.Payload.Info != nil && r.Payload.Info.Last != nil {
				result.Known = true
				usageSeen = true
				result.CtxTokens, result.Window = r.Payload.Info.Last.Total, r.Payload.Info.Window
				usageTime = r.Timestamp
			}
			if !turnSeen {
				switch r.Payload.Type {
				case "task_started":
					result.Busy, turnSeen, turnTime = true, true, r.Timestamp
				case "task_complete", "turn_aborted":
					result.Busy, turnSeen, turnTime = false, true, r.Timestamp
				}
			}
		}
		if !humanSeen && r.Type == "response_item" && r.Payload.Type == "message" && r.Payload.Role == "user" && !automatic(contentText(r.Payload.Content)) {
			humanTime, humanSeen = r.Timestamp, true
		}
		return !(usageSeen && modelSeen && humanSeen && turnSeen)
	})
	result.UsageAt, result.TurnAt, result.TurnKnown = usageTime, turnTime, turnSeen
	return codexScan{state: result, human: humanTime}
}

func randomCodexRow(rng *rand.Rand, n int) string {
	stamp := time.Date(2026, 9, 25, 0, 0, n, 0, time.UTC).Format(time.RFC3339Nano)
	switch rng.Intn(14) {
	case 0:
		return fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":"thread-%d"}}`, stamp, rng.Intn(4))
	case 1:
		return fmt.Sprintf(`{"timestamp":%q,"type":"turn_context","payload":{"model":"m%d","effort":"e%d","service_tier":"t%d"}}`, stamp, rng.Intn(3), rng.Intn(3), rng.Intn(2))
	case 2:
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":%d},"model_context_window":%d}}}`, stamp, rng.Intn(1e6), rng.Intn(3)*1e5)
	case 3:
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":null}}`, stamp)
	case 4:
		if rng.Intn(2) == 0 {
			return fmt.Sprintf(`{"timestamp":%q,"type":"compacted","payload":{}}`, stamp)
		}
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"context_compacted"}}`, stamp)
	case 5:
		kinds := []string{"task_started", "task_complete", "turn_aborted"}
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":%q}}`, stamp, kinds[rng.Intn(3)])
	case 6:
		texts := []string{"please fix it", "[ANNOUNCE] x", "health-watch: y", "[usage-policy] z", ""}
		return fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}}`, stamp, texts[rng.Intn(len(texts))])
	case 7:
		return fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"message","role":"assistant","content":"ok"}}`, stamp)
	case 8:
		return `{"type":"event_msg","payload":` // torn
	case 9:
		return ""
	case 10:
		return "not json"
	case 11:
		return fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"function_call_output","output":%q}}`, stamp, strings.Repeat("x", rng.Intn(3000)))
	default:
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"agent_message"}}`, stamp)
	}
}

func TestCachedCodexScanMatchesWholeFileScan(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	dir := t.TempDir()
	incremental := 0
	for history := 0; history < 120; history++ {
		path := filepath.Join(dir, fmt.Sprintf("rollout-2026-09-25T00-00-00-%08d-0000-0000-0000-000000000000.jsonl", history))
		var content strings.Builder
		rows := 0
		for step := 0; step < 12; step++ {
			switch rng.Intn(10) {
			case 0:
				// A rewrite, not an append: the cache must not merge across it.
				content.Reset()
				for i := rng.Intn(20); i > 0; i-- {
					rows++
					content.WriteString(randomCodexRow(rng, rows) + "\n")
				}
			default:
				for i := rng.Intn(25); i > 0; i-- {
					rows++
					content.WriteString(randomCodexRow(rng, rows) + "\n")
				}
			}
			written := content.String()
			// Sometimes the writer is caught mid-row.
			if rng.Intn(4) == 0 {
				rows++
				partial := randomCodexRow(rng, rows)
				if rng.Intn(2) == 0 && len(partial) > 0 {
					partial = partial[:rng.Intn(len(partial))]
				}
				written += partial
				content.WriteString(partial)
				if rng.Intn(2) == 0 {
					content.WriteString("\n")
				}
			}
			if err := os.WriteFile(path, []byte(written), 0o600); err != nil {
				t.Fatal(err)
			}
			// Size and modification time can repeat within the clock's
			// resolution; give every step its own time as the writer would.
			stamp := time.Unix(1_700_000_000+int64(history*100+step), 0)
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			codexScans.Lock()
			entry, cached := codexScans.entries[path]
			codexScans.Unlock()
			if cached && entry.rows > 0 && entry.rows < int64(len(written)) {
				incremental++
			}
			got, want := cachedCodexScan(path), referenceCodexScan(path)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("history %d step %d:\n got %+v\nwant %+v\nfile:\n%s", history, step, got, want, written)
			}
		}
	}
	if incremental < 500 {
		t.Fatalf("only %d scans could reuse a cached prefix", incremental)
	}
	t.Logf("%d scans reused a cached prefix", incremental)
}
