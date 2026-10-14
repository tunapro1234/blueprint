package tokens

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// claudeSubAssistant is an assistant usage record as it appears in a subagent or
// workflow transcript (and, as a sidechain, in the main transcript): it carries a
// message id alongside the request id and is marked isSidechain.
func claudeSubAssistant(uuid, requestID, messageID, sessionID, timestamp string, in, cw, cr, out int64) map[string]any {
	return map[string]any{
		"type": "assistant", "uuid": uuid, "requestId": requestID,
		"sessionId": sessionID, "timestamp": timestamp, "isSidechain": true,
		"message": map[string]any{
			"id": messageID, "model": "claude-sub", "content": []any{},
			"usage": map[string]any{
				"input_tokens": in, "output_tokens": out,
				"cache_creation_input_tokens": cw, "cache_read_input_tokens": cr,
			},
		},
	}
}

// Subagent and workflow transcripts are counted, attributed to the owning main
// agent, tagged with their Kind, and deduped against a copy of the same request
// that also appears in the main transcript as a sidechain.
func TestSubagentAndWorkflowUsageAttributedWithKind(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, istanbul)
	config := testConfig(t, now)
	book := filepath.Join(t.TempDir(), "agentbook.json")
	if err := os.WriteFile(book, []byte(`{"agents":[{"name":"tester","folder":"/srv/test"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	config.Agentbooks = []string{book}

	proj, sess, ts := "-srv-test", "sess1", "2026-10-13T08:00:00Z"
	writeLines(t, filepath.Join(config.ClaudeRoot, proj, sess+".jsonl"),
		claudeUser("u1", "", "prompt-1", ts, "main prompt", false),
		claudeAssistant("a1", "u1", "req-main", ts, 100),
		// A subagent request that also surfaced in the main transcript as a
		// sidechain. It must be counted once, as the main agent's own usage.
		claudeSubAssistant("sc", "req-dup", "msg-dup", sess, ts, 1, 0, 0, 7),
	)
	writeLines(t, filepath.Join(config.ClaudeRoot, proj, sess, "subagents", "agent-aaa.jsonl"),
		claudeSubAssistant("s1", "req-sub1", "msg-s1", sess, ts, 10, 20, 30, 40),
		claudeSubAssistant("s2", "req-dup", "msg-dup", sess, ts, 999, 999, 999, 999),
	)
	writeLines(t, filepath.Join(config.ClaudeRoot, proj, sess, "subagents", "workflows", "wf_abc", "agent-bbb.jsonl"),
		claudeSubAssistant("w1", "req-wf1", "msg-w1", sess, ts, 5, 6, 7, 8),
	)

	stats, err := Collect(config)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Deduped < 1 {
		t.Fatalf("expected the duplicated request to be deduped, stats=%+v", stats)
	}

	rows, err := readRawDay(config.StoreDir, "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]RawRecord{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	if len(rows) != 4 {
		t.Fatalf("raw rows=%d, want 4 (req-dup counted once): %+v", len(rows), rows)
	}
	if dup := byKey["claude:req-dup"]; dup.Out != 7 || dup.Kind != "" {
		t.Fatalf("req-dup=%+v, want out=7 and main kind (the main sidechain, not the 999 subagent copy)", dup)
	}
	if sub := byKey["claude:req-sub1"]; sub.Kind != "subagent" || sub.rollupAgent() != "tester" || sub.Out != 40 {
		t.Fatalf("subagent row=%+v, want kind=subagent agent=tester out=40", sub)
	}
	if wf := byKey["claude:req-wf1"]; wf.Kind != "workflow" || wf.WF != "wf_abc" || wf.rollupAgent() != "tester" {
		t.Fatalf("workflow row=%+v, want kind=workflow wf=wf_abc agent=tester", wf)
	}

	summary, err := Summary(config.StoreDir, DayRange(now), "")
	if err != nil {
		t.Fatal(err)
	}
	var tester *SummaryRow
	for i := range summary.Rows {
		if summary.Rows[i].Agent == "tester" {
			tester = &summary.Rows[i]
		}
	}
	if tester == nil {
		t.Fatalf("no tester row in summary: %+v", summary.Rows)
	}
	// Total folds in the subagent and workflow work: 100 + 7 + 40 + 8.
	if tester.Out != 155 {
		t.Fatalf("tester total out=%d, want 155", tester.Out)
	}
	// The subagent portion is the subagent (40) plus the workflow (8).
	if tester.SubOut != 48 {
		t.Fatalf("tester sub out=%d, want 48", tester.SubOut)
	}
}

// The daily rollup is what outlives the 14-day raw files, so it must carry the
// Kind split per agent rather than collapsing subagent usage into the main total.
func TestDailyRollupKeepsKindSplit(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, istanbul)
	config := testConfig(t, now)
	proj, sess, ts := "-srv-split", "sess1", "2026-10-13T08:00:00Z"
	writeLines(t, filepath.Join(config.ClaudeRoot, proj, sess+".jsonl"),
		claudeUser("u1", "", "p1", ts, "prompt", false),
		claudeAssistant("a1", "u1", "req-main", ts, 100),
	)
	writeLines(t, filepath.Join(config.ClaudeRoot, proj, sess, "subagents", "agent-aaa.jsonl"),
		claudeSubAssistant("s1", "req-sub1", "msg-s1", sess, ts, 10, 0, 0, 40),
	)
	if _, err := Collect(config); err != nil {
		t.Fatal(err)
	}
	var daily []DailyRecord
	if err := readJSONLines(filepath.Join(config.StoreDir, "daily", "2026.jsonl"), &daily); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int64{}
	for _, row := range daily {
		if row.Day == "2026-10-13" {
			kinds[row.Kind] += row.Out
		}
	}
	if kinds[""] != 100 || kinds["subagent"] != 40 {
		t.Fatalf("daily kind split=%+v, want main=100 subagent=40", kinds)
	}
}
