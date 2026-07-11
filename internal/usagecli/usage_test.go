package usagecli

import (
	"strings"
	"testing"
)

func sample(ts string, claude5, claude7, codex5, codex7 any) Sample {
	return Sample{
		TS:           ts,
		Claude5:      claude5,
		Claude7:      claude7,
		ClaudeResets: Resets{Five: "c5-" + ts, Week: "c7-" + ts},
		Codex5:       codex5,
		Codex7:       codex7,
		CodexResets:  Resets{Five: "x5-" + ts, Week: "x7-" + ts},
	}
}

func TestResolveFallsBackPerProvider(t *testing.T) {
	rows := []Sample{
		sample("2026-07-10T20:00:00Z", nil, nil, 47.0, 44.0),
		sample("2026-07-11T05:00:00Z", 12.0, 3.0, nil, nil),
		sample("2026-07-11T06:00:00Z", nil, nil, nil, nil),
	}
	r := resolve(rows)
	if r.TS != "2026-07-11T06:00:00Z" {
		t.Fatalf("newest ts = %s", r.TS)
	}
	if r.Claude.TS != "2026-07-11T05:00:00Z" || r.Claude.Claude5 != 12.0 {
		t.Fatalf("claude fallback = %+v", r.Claude)
	}
	if r.Codex.TS != "2026-07-10T20:00:00Z" || r.Codex.Codex5 != 47.0 {
		t.Fatalf("codex fallback = %+v", r.Codex)
	}
	// Resets must come from the same row as the values.
	if r.Claude.ClaudeResets.Five != "c5-2026-07-11T05:00:00Z" {
		t.Fatalf("claude resets from wrong row: %+v", r.Claude.ClaudeResets)
	}
	lines := Lines(r)
	if !strings.Contains(lines[1], "(as of 2026-07-11T05:00:00Z)") {
		t.Fatalf("claude line missing as-of note: %s", lines[1])
	}
	if !strings.Contains(lines[2], "(as of 2026-07-10T20:00:00Z)") {
		t.Fatalf("codex line missing as-of note: %s", lines[2])
	}
}

func TestResolveFreshRowHasNoNote(t *testing.T) {
	rows := []Sample{sample("2026-07-11T06:00:00Z", 5.0, 1.0, 2.0, 3.0)}
	r := resolve(rows)
	lines := Lines(r)
	for _, line := range lines {
		if strings.Contains(line, "as of") {
			t.Fatalf("unexpected as-of note: %s", line)
		}
	}
}

func TestResolveRespects48hWindow(t *testing.T) {
	rows := []Sample{
		sample("2026-07-01T00:00:00Z", 90.0, 80.0, 70.0, 60.0),
		sample("2026-07-11T06:00:00Z", nil, nil, nil, nil),
	}
	r := resolve(rows)
	if r.Claude.Claude5 != nil || r.Codex.Codex5 != nil {
		t.Fatalf("stale row outside window must not be used: %+v", r)
	}
}
