package usagecli

import (
	"strings"
	"testing"
	"time"

	"blueprint/internal/codexauth"
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
	lines := Lines(r, Options{})
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
	lines := Lines(r, Options{})
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
	line := Lines(r, Options{})[2]
	want := "Codex:  %52 (reset x5-2026-08-03T18:22:09Z)"
	if line != want {
		t.Fatalf("codex line = %q, want %q", line, want)
	}
}

func TestLinesRenderBothCodexWindowsWhenSecondaryExists(t *testing.T) {
	r := resolve([]Sample{sample("2026-07-11T06:00:00Z", 12.0, 39.0, 52.0, 44.0)})
	line := Lines(r, Options{})[2]
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
	line := Lines(r, Options{})[1]
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

// The healthy line is the contract: this is byte-for-byte what a working fleet
// printed before the null-rendering fix, reset timestamp included.
func TestLinesHealthyCodexLineIsUnchanged(t *testing.T) {
	r := resolve([]Sample{{
		TS:          "2026-08-10T13:20:00Z",
		Codex5:      52.0,
		CodexResets: Resets{Five: "2026-08-10T13:15:18Z"},
	}})
	line := Lines(r, Options{Now: time.Date(2026, 8, 10, 13, 25, 0, 0, time.UTC)})[2]
	want := "Codex:  %52 (reset 2026-08-10T13:15:18Z)"
	if line != want {
		t.Fatalf("codex line = %q, want %q", line, want)
	}
	// A working session must not add commentary either.
	withAuth := Lines(r, Options{Auth: codexauth.State{Status: codexauth.OK, Reason: "last_refresh 3m old, id_token valid for 57m"}})[2]
	if withAuth != want {
		t.Fatalf("codex line with auth state = %q, want %q", withAuth, want)
	}
}

// The bug: a null value rendered through %v printed "%<nil> (reset )".
func TestLinesNeverRenderNilAsAValue(t *testing.T) {
	rows := []Sample{
		sample("2026-08-07T11:31:07Z", 12.0, 39.0, 100.0, nil),
		sample("2026-08-10T13:26:07Z", 5.0, 9.0, nil, nil),
	}
	r := resolve(rows)
	for _, auth := range []codexauth.State{
		{},
		{Status: codexauth.OK, Reason: "fine"},
		{Status: codexauth.Stale, Reason: "meh"},
		{Status: codexauth.Expired, Reason: "gone"},
	} {
		for _, line := range Lines(r, Options{Auth: auth}) {
			if strings.Contains(line, "<nil>") {
				t.Fatalf("nil leaked into output with auth %q: %q", auth.Status, line)
			}
		}
	}
}

func TestLinesCodexAbsence(t *testing.T) {
	// The real timeline: last reading 2026-08-08T11:31:07Z, nulls ever since,
	// which is well outside fallbackWindow so the value stays nil.
	history := []Sample{
		sample("2026-08-08T11:31:07Z", 12.0, 39.0, 100.0, nil),
		sample("2026-08-10T13:26:07Z", 5.0, 9.0, nil, nil),
	}
	noHistory := []Sample{sample("2026-08-10T13:26:07Z", 5.0, 9.0, nil, nil)}
	now := time.Date(2026, 8, 10, 13, 30, 0, 0, time.UTC)

	cases := []struct {
		name string
		rows []Sample
		opts Options
		want string
	}{
		{
			name: "broken access names the cause",
			rows: history,
			opts: Options{Now: now, Auth: codexauth.State{
				Status: codexauth.Expired,
				Reason: "last_refresh 3d old (>1d), id_token expired 3d 4h ago",
			}},
			want: "Codex:  ERISIM YOK (codex auth: last_refresh 3d old (>1d), id_token expired 3d 4h ago)",
		},
		{
			name: "plausible access reports a data gap",
			rows: history,
			opts: Options{Now: now, Auth: codexauth.State{Status: codexauth.OK, Reason: "last_refresh 4m old, id_token valid for 56m"}},
			want: "Codex:  veri yok (son deger 2d 1h once)",
		},
		{
			name: "unknown access reports a data gap",
			rows: history,
			opts: Options{Now: now},
			want: "Codex:  veri yok (son deger 2d 1h once)",
		},
		{
			name: "stale but unexpired access reports a data gap",
			rows: history,
			opts: Options{Now: now, Auth: codexauth.State{Status: codexauth.Stale, Reason: "awaiting next refresh"}},
			want: "Codex:  veri yok (son deger 2d 1h once)",
		},
		{
			name: "no clock falls back to the newest sample",
			rows: history,
			opts: Options{},
			want: "Codex:  veri yok (son deger 2d 1h once)",
		},
		{
			name: "no reading ever recorded",
			rows: noHistory,
			opts: Options{Now: now},
			want: "Codex:  veri yok (kayitli deger yok)",
		},
		{
			name: "no reading ever recorded, access broken",
			rows: noHistory,
			opts: Options{Now: now, Auth: codexauth.State{Status: codexauth.Expired, Reason: "auth.json not found at /root/.codex/auth.json"}},
			want: "Codex:  ERISIM YOK (codex auth: auth.json not found at /root/.codex/auth.json)",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			line := Lines(resolve(testCase.rows), testCase.opts)[2]
			if line != testCase.want {
				t.Fatalf("codex line = %q, want %q", line, testCase.want)
			}
		})
	}
}

// The last-reading timestamp is evidence about the gap, so it must survive
// beyond the window that governs which values may be rendered.
func TestResolveRecordsLastCodexReadingOutsideWindow(t *testing.T) {
	r := resolve([]Sample{
		sample("2026-07-01T00:00:00Z", 90.0, 80.0, 70.0, 60.0),
		sample("2026-08-10T13:26:07Z", 5.0, 9.0, nil, nil),
	})
	if r.Codex.Codex5 != nil {
		t.Fatalf("value outside the window must not be rendered: %+v", r.Codex)
	}
	if r.CodexLastTS != "2026-07-01T00:00:00Z" {
		t.Fatalf("CodexLastTS = %q, want the old reading's timestamp", r.CodexLastTS)
	}
	if line := Lines(r, Options{Now: time.Date(2026, 8, 10, 13, 26, 7, 0, time.UTC)})[2]; line != "Codex:  veri yok (son deger 40d 13h once)" {
		t.Fatalf("codex line = %q", line)
	}
}

// Age arithmetic must refuse to guess when the timestamps cannot support it.
func TestLinesCodexAbsenceWithUnusableTimestamps(t *testing.T) {
	r := resolve([]Sample{sample("not-a-time", 5.0, 9.0, nil, nil)})
	r.CodexLastTS = "also-not-a-time"
	if line := Lines(r, Options{})[2]; line != "Codex:  veri yok (kayitli deger yok)" {
		t.Fatalf("codex line = %q", line)
	}
	// A reading stamped after the reference clock is not a negative age.
	future := resolve([]Sample{sample("2026-08-10T13:00:00Z", 5.0, 9.0, nil, nil)})
	future.CodexLastTS = "2026-08-11T13:00:00Z"
	if line := Lines(future, Options{})[2]; line != "Codex:  veri yok (kayitli deger yok)" {
		t.Fatalf("codex line = %q", line)
	}
}

// hasClaude() only vouches for the two windows, so the Fable figure can be
// absent on its own; and after a long collector outage every claude figure is
// absent. Neither may print a formatted nil.
func TestLinesClaudeFiguresStateAbsence(t *testing.T) {
	fableMissing := resolve([]Sample{{
		TS:           "2026-08-10T13:26:07Z",
		Claude5:      12.0,
		Claude7:      39.0,
		ClaudeResets: Resets{Five: "c5", Week: "c7"},
		Codex5:       52.0,
		CodexResets:  Resets{Five: "x5"},
	}})
	want := "Claude: 5h %12 (reset c5), 7d %39 (reset c7), Fable 7d veri yok"
	if line := Lines(fableMissing, Options{})[1]; line != want {
		t.Fatalf("claude line = %q, want %q", line, want)
	}
	blackout := resolve([]Sample{{TS: "2026-08-10T13:26:07Z"}})
	want = "Claude: 5h veri yok, 7d veri yok, Fable 7d veri yok"
	if line := Lines(blackout, Options{})[1]; line != want {
		t.Fatalf("claude line = %q, want %q", line, want)
	}
}

// A reading with no reset timestamp must not render an empty parenthesis.
func TestLinesOmitsEmptyReset(t *testing.T) {
	r := resolve([]Sample{{TS: "2026-08-10T13:26:07Z", Codex5: 52.0}})
	if line := Lines(r, Options{})[2]; line != "Codex:  %52" {
		t.Fatalf("codex line = %q, want %q", line, "Codex:  %52")
	}
}
