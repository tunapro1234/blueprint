package monitorcli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/codexauth"
)

func TestDecodeKeepsValidSectionsWhenOneSectionIsMalformed(t *testing.T) {
	doc, err := Decode([]byte(`{
  "generated_at":"2026-07-10T12:00:00Z",
  "usage":{"current":{"claude_5h":"42","resets":{}},"history":[]},
  "cost":[],
  "services":{"units":[]}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Usage == nil || !doc.Usage.Current.Claude5H.Valid || doc.Usage.Current.Claude5H.Value != 42 {
		t.Fatalf("usage was not decoded: %+v", doc.Usage)
	}
	if doc.Cost != nil || !strings.Contains(doc.Notes["cost"], "could not be decoded") {
		t.Fatalf("malformed cost note missing: cost=%+v notes=%v", doc.Cost, doc.Notes)
	}
	if doc.Services == nil {
		t.Fatal("valid services section was discarded")
	}
}

func TestLoadPrefersReadableLocalData(t *testing.T) {
	local := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(local, []byte(`{"generated_at":"local"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"generated_at":"remote"}`))
	}))
	defer server.Close()

	doc, source, err := Load(context.Background(), SourceOptions{LocalDataPath: local, BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if doc.GeneratedAt != "local" || source != local || requests != 0 {
		t.Fatalf("doc=%+v source=%q requests=%d", doc, source, requests)
	}
}

func TestLoadFallsBackToConfiguredDashboard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/monitor/data.json" {
			t.Fatalf("path=%q, want /monitor/data.json", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"generated_at":"remote"}`))
	}))
	defer server.Close()

	doc, source, err := Load(context.Background(), SourceOptions{
		LocalDataPath: filepath.Join(t.TempDir(), "missing.json"),
		BaseURL:       server.URL + "/monitor/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if doc.GeneratedAt != "remote" || source != server.URL+"/monitor/data.json" {
		t.Fatalf("doc=%+v source=%q", doc, source)
	}
}

func TestRenderOverviewShowsStatesAndResetCountdowns(t *testing.T) {
	doc, err := Decode([]byte(`{"usage":{"current":{
  "ts":"2026-07-10T12:00:00Z",
  "claude_5h":95,"claude_7d":75,"codex_5h":10,
  "resets":{"claude_5h":"2026-07-10T14:30:00Z","claude_7d":"2026-07-11T12:00:00Z"}
}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = Render(&output, doc, "overview", RenderOptions{Now: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Claude 5h", "95%", "critical", "2h 30m", "Claude 7d", "warning", "Codex", "normal"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q:\n%s", want, output.String())
		}
	}
	// The codex meter carries no window label (its length is plan-dependent)
	// and the absent secondary window must not produce a row at all.
	if strings.Contains(output.String(), "Codex 5h") || strings.Contains(output.String(), "Codex 7d") {
		t.Errorf("codex meter must not claim a window length:\n%s", output.String())
	}
}

func TestRenderOverviewKeepsCodexSecondaryWhenReported(t *testing.T) {
	doc, err := Decode([]byte(`{"usage":{"current":{
  "ts":"2026-07-10T12:00:00Z",
  "codex_5h":10,"codex_7d":44,
  "resets":{"codex_5h":"2026-07-10T14:30:00Z","codex_7d":"2026-07-11T12:00:00Z"}
}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Render(&output, doc, "overview", RenderOptions{Now: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Codex ", "10%", "Codex 7d", "44%"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q:\n%s", want, output.String())
		}
	}
}

func TestRenderUsageSummarizes24HoursAndSevenDays(t *testing.T) {
	doc, err := Decode([]byte(`{"usage":{"history":[
  {"ts":"2026-07-03T12:00:00Z","claude_5h":90},
  {"ts":"2026-07-09T11:00:00Z","claude_5h":10},
  {"ts":"2026-07-10T12:00:00Z","claude_5h":30}
]}}`))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Render(&output, doc, "usage", RenderOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "24h    Claude 5h  30%  30%  30%  1") {
		t.Fatalf("24h summary mismatch:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "7d     Claude 5h  10%  90%  30%  3") {
		t.Fatalf("7d summary mismatch:\n%s", output.String())
	}
}

func TestRenderProjectsUsesEachDashboardRange(t *testing.T) {
	doc, err := Decode([]byte(`{"tokens":{"series":[
  {"ts":"2026-07-04T12:00:00Z","projects":{"-srv-old":{"out":100}}},
  {"ts":"2026-07-08T12:00:00Z","projects":{"-srv-mid":{"out":20}}},
  {"ts":"2026-07-10T12:00:00Z","projects":{"-srv-new":{"out":5}}}
]}}`))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Render(&output, doc, "projects", RenderOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"new      5    5   5",
		"mid      0    20  20",
		"old      0    0   100",
		"each column is clipped to available data",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q:\n%s", want, output.String())
		}
	}
}

func TestRenderServicesMergesDashboardAndJobsState(t *testing.T) {
	doc, err := Decode([]byte(`{"services":{"ts":"2026-07-10T12:00:00Z","units":[
  {"name":"pulse","service":{"active":"inactive","sub":"ok","result":"success","desc":"collector","last_run":"old"},"timer":{"active":"active","next_run":"later"}},
  {"name":"web","service":{"active":"active","sub":"running","result":"success","desc":"server"}}
]}}`))
	if err != nil {
		t.Fatal(err)
	}
	jobs := map[string]JobState{
		"pulse": {Status: "failed", LastRun: "2026-07-10T12:01:00Z", Error: "boom"},
		"queue": {Status: "ok", Duration: "12ms"},
	}
	var output bytes.Buffer
	if err := Render(&output, doc, "services", RenderOptions{Jobs: jobs}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pulse", "error", "failed", "boom", "queue", "healthy", "12ms", "web", "running"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q:\n%s", want, output.String())
		}
	}
}

func TestMissingSectionRendersNote(t *testing.T) {
	doc, err := Decode([]byte(`{"generated_at":"2026-07-10T12:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"overview", "usage", "cost", "agents", "projects", "services"} {
		var output bytes.Buffer
		if err := Render(&output, doc, view, RenderOptions{}); err != nil {
			t.Fatalf("render %s: %v", view, err)
		}
		if !strings.Contains(output.String(), "Note:") {
			t.Errorf("%s did not explain missing data:\n%s", view, output.String())
		}
	}
}

func TestLoadRadarPrefersLocalThenEmbedded(t *testing.T) {
	local := filepath.Join(t.TempDir(), "radar.json")
	if err := os.WriteFile(local, []byte(`{"generated_at":"local"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	embedded := &Document{Radar: &Radar{GeneratedAt: "embedded"}}
	radar, source, err := LoadRadar(context.Background(), embedded, SourceOptions{LocalRadarPath: local})
	if err != nil || radar.GeneratedAt != "local" || source != local {
		t.Fatalf("local radar=%+v source=%q err=%v", radar, source, err)
	}
	radar, source, err = LoadRadar(context.Background(), embedded, SourceOptions{LocalRadarPath: filepath.Join(t.TempDir(), "missing")})
	if err != nil || radar.GeneratedAt != "embedded" || source != "data.json" {
		t.Fatalf("embedded radar=%+v source=%q err=%v", radar, source, err)
	}
}

// A missing codex meter during a proven outage must be explained, not simply
// left out of the table.
func TestRenderOverviewExplainsMissingCodexMeterWhenAuthIsBroken(t *testing.T) {
	body := []byte(`{"usage":{"current":{
  "ts":"2026-08-10T13:26:07Z",
  "claude_5h":5,"claude_7d":9,"codex_5h":null,
  "resets":{"claude_5h":"2026-08-10T18:10:00Z","claude_7d":"2026-08-14T23:00:00Z"}
}}}`)
	doc, err := Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 10, 13, 30, 0, 0, time.UTC)
	broken := codexauth.State{Status: codexauth.Expired, Reason: "last_refresh 3d old (>1d), id_token expired 3d 4h ago"}

	var output bytes.Buffer
	if err := Render(&output, doc, "overview", RenderOptions{Now: now, CodexAuth: broken}); err != nil {
		t.Fatal(err)
	}
	want := "Note: Codex ERISIM YOK (codex auth: last_refresh 3d old (>1d), id_token expired 3d 4h ago); codex meters omitted."
	if !strings.Contains(output.String(), want) {
		t.Fatalf("output missing %q:\n%s", want, output.String())
	}

	// Without proof there is no claim, and a present meter is never annotated.
	var quiet bytes.Buffer
	if err := Render(&quiet, doc, "overview", RenderOptions{Now: now, CodexAuth: codexauth.State{Status: codexauth.Stale, Reason: "awaiting next refresh"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(quiet.String(), "ERISIM YOK") {
		t.Fatalf("unproven auth trouble must not be reported:\n%s", quiet.String())
	}
	healthyDoc, err := Decode([]byte(`{"usage":{"current":{"ts":"2026-08-10T13:26:07Z","codex_5h":52}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var healthy bytes.Buffer
	if err := Render(&healthy, healthyDoc, "overview", RenderOptions{Now: now, CodexAuth: broken}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(healthy.String(), "ERISIM YOK") {
		t.Fatalf("a present codex meter must not be annotated:\n%s", healthy.String())
	}
}
