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

// The collector maps the API's primary window to codex_5h whatever its real
// length, so a plan without a secondary window (prolite, current) must not be
// rendered with a window label or an empty second field.
func TestLinesRenderCodexWithoutWindowLabelWhenSecondaryIsNull(t *testing.T) {
	r := resolve([]Sample{sample("2026-08-03T18:22:09Z", 12.0, 39.0, 52.0, nil)})
	line := Lines(r)[2]
	want := "Codex:  %52 (reset x5-2026-08-03T18:22:09Z)"
	if line != want {
		t.Fatalf("codex line = %q, want %q", line, want)
	}
}

func TestLinesRenderBothCodexWindowsWhenSecondaryExists(t *testing.T) {
	r := resolve([]Sample{sample("2026-07-11T06:00:00Z", 12.0, 39.0, 52.0, 44.0)})
	line := Lines(r)[2]
	want := "Codex:  5h %52 (reset x5-2026-07-11T06:00:00Z), 7d %44 (reset x7-2026-07-11T06:00:00Z)"
	if line != want {
		t.Fatalf("codex line = %q, want %q", line, want)
	}
}

// Claude genuinely has two windows; the codex fix must not touch its line.
func TestLinesKeepClaudeWindowsUnchanged(t *testing.T) {
	r := resolve([]Sample{{
		TS:           "2026-08-03T18:22:09Z",
		Claude5:      12.0,
		Claude7:      39.0,
		ClaudeFable7: 36.0,
		ClaudeResets: Resets{Five: "c5", Week: "c7"},
		Codex5:       52.0,
		CodexResets:  Resets{Five: "x5"},
	}})
	line := Lines(r)[1]
	want := "Claude: 5h %12 (reset c5), 7d %39 (reset c7), Fable 7d %36"
	if line != want {
		t.Fatalf("claude line = %q, want %q", line, want)
	}
}

func TestResolveFallsBackWhenOnlyCodexPrimaryIsPresent(t *testing.T) {
	rows := []Sample{
		sample("2026-08-03T17:00:00Z", 12.0, 39.0, 52.0, nil),
		sample("2026-08-03T18:00:00Z", 12.0, 39.0, nil, nil),
	}
	r := resolve(rows)
	if r.Codex.TS != "2026-08-03T17:00:00Z" || r.Codex.Codex5 != 52.0 {
		t.Fatalf("codex fallback ignored a row without a secondary window: %+v", r.Codex)
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
