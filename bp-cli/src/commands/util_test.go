package commands

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	bp "blueprint"
)

func TestFormatPath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	if got := formatPath(""); got != "." {
		t.Fatalf("expected '.', got %s", got)
	}
	if got := formatPath(tmp); got != "." {
		t.Fatalf("expected '.', got %s", got)
	}
	if got := formatPath(filepath.Join(tmp, "sub", "file.txt")); got != "./sub/file.txt" {
		t.Fatalf("unexpected path: %s", got)
	}
}

func TestNormalizeYAMLError(t *testing.T) {
	err := errors.New("yaml: invalid")
	if got := normalizeYAMLError(err); got != "invalid" {
		t.Fatalf("unexpected normalize: %s", got)
	}
	err = errors.New("YAML parse error at line 3, column 1")
	if got := normalizeYAMLError(err); !strings.HasPrefix(got, "line 3") {
		t.Fatalf("unexpected normalize: %s", got)
	}
}

func TestResolveBlueprintPath(t *testing.T) {
	dir := t.TempDir()
	bpPath := filepath.Join(dir, "BLUEPRINT.yaml")
	writeFile(t, bpPath, "_meta:\n  version: \"1\"\n")
	resolved, err := resolveBlueprintPath(dir)
	if err != nil {
		t.Fatalf("resolveBlueprintPath: %v", err)
	}
	if resolved != bpPath {
		t.Fatalf("unexpected resolved path: %s", resolved)
	}
	resolved, err = resolveBlueprintPath(bpPath)
	if err != nil {
		t.Fatalf("resolveBlueprintPath: %v", err)
	}
	if resolved != bpPath {
		t.Fatalf("unexpected resolved path: %s", resolved)
	}
}

func TestFindBlueprintsRecursive(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "one", "BLUEPRINT.yaml"), "_meta:\n  version: \"1\"\n")
	writeFile(t, filepath.Join(root, "two", "BLUEPRINT.yaml"), "_meta:\n  version: \"1\"\n")
	writeFile(t, filepath.Join(root, ".blueprint", "skip", "BLUEPRINT.yaml"), "_meta:\n  version: \"1\"\n")
	files, err := findBlueprintsRecursive(root)
	if err != nil {
		t.Fatalf("findBlueprintsRecursive: %v", err)
	}
	for i := range files {
		files[i] = filepath.Base(filepath.Dir(files[i]))
	}
	sort.Strings(files)
	if !reflect.DeepEqual(files, []string{"one", "two"}) {
		t.Fatalf("unexpected files: %v", files)
	}
}

func TestReadCurrentSnapshotID(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".blueprint")
	if _, err := readCurrentSnapshotID(stateDir); !errors.Is(err, bp.ErrNoSnapshot) {
		t.Fatalf("expected ErrNoSnapshot, got %v", err)
	}
	writeFile(t, filepath.Join(stateDir, "current"), "not-a-snapshot")
	if _, err := readCurrentSnapshotID(stateDir); !errors.Is(err, ErrInvalidCurrentID) {
		t.Fatalf("expected ErrInvalidCurrentID, got %v", err)
	}
	valid := "20240102-0304-abcd-test"
	writeFile(t, filepath.Join(stateDir, "current"), valid)
	id, err := readCurrentSnapshotID(stateDir)
	if err != nil {
		t.Fatalf("readCurrentSnapshotID: %v", err)
	}
	if id != valid {
		t.Fatalf("unexpected id: %s", id)
	}
}

func TestResolveSnapshotID(t *testing.T) {
	stateDir := t.TempDir()
	id1 := "20240102-0304-abcd-test"
	id2 := "20240102-0304-ffff-other"
	id3 := "20240102-0305-acde-next"
	writeFile(t, filepath.Join(stateDir, "history", id1, "meta.yaml"), "id: "+id1)
	writeFile(t, filepath.Join(stateDir, "history", id2, "meta.yaml"), "id: "+id2)
	writeFile(t, filepath.Join(stateDir, "history", id3, "meta.yaml"), "id: "+id3)

	if got, err := resolveSnapshotID(stateDir, "current"); err != nil || got != "current" {
		t.Fatalf("expected current, got %s, err %v", got, err)
	}
	if got, err := resolveSnapshotID(stateDir, id1); err != nil || got != id1 {
		t.Fatalf("expected id1, got %s, err %v", got, err)
	}
	if _, err := resolveSnapshotID(stateDir, "20240102-0304"); err == nil {
		t.Fatalf("expected ambiguous error")
	}
	if got, err := resolveSnapshotID(stateDir, "20240102-0305"); err != nil || got != id3 {
		t.Fatalf("expected id3, got %s, err %v", got, err)
	}
	if _, err := resolveSnapshotID(stateDir, "2024"); err == nil {
		t.Fatalf("expected not found error")
	}

	noHistory := t.TempDir()
	if _, err := resolveSnapshotID(noHistory, id1); !errors.Is(err, bp.ErrNoSnapshot) {
		t.Fatalf("expected ErrNoSnapshot, got %v", err)
	}
}

func TestMatchSnapshotPrefixNoHistory(t *testing.T) {
	_, err := matchSnapshotPrefix(t.TempDir(), "20240101")
	if !errors.Is(err, bp.ErrNoSnapshot) {
		t.Fatalf("expected ErrNoSnapshot, got %v", err)
	}
}

func TestFormatTimestamp(t *testing.T) {
	if got := formatTimestamp("2024-01-02T15:04:05"); got != "2024-01-02 15:04" {
		t.Fatalf("unexpected timestamp: %s", got)
	}
	if got := formatTimestamp("2024-01-02T15:04"); got != "2024-01-02 15:04" {
		t.Fatalf("unexpected timestamp: %s", got)
	}
	if got := formatTimestamp("bad"); got != "bad" {
		t.Fatalf("unexpected timestamp: %s", got)
	}
}

func TestCompareIDs(t *testing.T) {
	if compareIDs("a", "b") >= 0 {
		t.Fatalf("expected a < b")
	}
	if compareIDs("aa", "a") <= 0 {
		t.Fatalf("expected aa > a")
	}
}

func TestDepUpgradesFromState(t *testing.T) {
	deps := map[string]bp.DepState{
		"./dep1": {Pinned: "a", Latest: "b"},
		"./dep2": {Pinned: "c", Latest: "c"},
		"./dep3": {Pinned: "d", Latest: "e", APIHash: "h1", LatestAPIHash: "h2"},
	}
	upgrades := depUpgradesFromState(deps)
	if len(upgrades) != 2 {
		t.Fatalf("expected 2 upgrades, got %d", len(upgrades))
	}
	if upgrades[0].Path != "./dep1" || upgrades[1].Path != "./dep3" {
		t.Fatalf("unexpected upgrades: %+v", upgrades)
	}
	if !upgrades[1].APIChanged {
		t.Fatalf("expected APIChanged true")
	}
	warnings := formatDepUpgradeWarnings(deps)
	if len(warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %d", len(warnings))
	}
	if !strings.HasPrefix(warnings[0], "./dep1") {
		t.Fatalf("unexpected warning: %s", warnings[0])
	}
}

func TestRelativeLabel(t *testing.T) {
	from := filepath.Join("/tmp", "root")
	to := filepath.Join(from, "sub")
	label, err := relativeLabel(from, to)
	if err != nil {
		t.Fatalf("relativeLabel: %v", err)
	}
	if label != "./sub" {
		t.Fatalf("unexpected label: %s", label)
	}
}

func TestAPIHashForSnapshot(t *testing.T) {
	dir := t.TempDir()
	bpPath := filepath.Join(dir, "BLUEPRINT.yaml")
	writeFile(t, bpPath, "_meta:\n  version: \"1\"\napi:\n  foo: bar\n")
	bpObj, err := bp.LoadBlueprint(dir)
	if err != nil {
		t.Fatalf("LoadBlueprint: %v", err)
	}
	id := "20240102-0304-abcd-test"
	writeFile(t, filepath.Join(bpObj.StateDir, "history", id, "meta.yaml"), "id: "+id+"\napi_hash: sha256:meta\n")
	hash, err := apiHashForSnapshot(bpObj, id)
	if err != nil {
		t.Fatalf("apiHashForSnapshot: %v", err)
	}
	if hash != "sha256:meta" {
		t.Fatalf("unexpected hash: %s", hash)
	}
	fallback, err := apiHashForSnapshot(bpObj, "missing")
	if err != nil {
		t.Fatalf("apiHashForSnapshot fallback: %v", err)
	}
	if fallback == "" {
		t.Fatalf("expected fallback hash")
	}
}

func TestMaxExit(t *testing.T) {
	if maxExit(0, 2) != 2 {
		t.Fatalf("unexpected maxExit")
	}
	if maxExit(2, 1) != 1 {
		t.Fatalf("unexpected maxExit")
	}
	if maxExit(0, 0) != 0 {
		t.Fatalf("unexpected maxExit")
	}
}

func TestSectionLineCountAndWarnings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "BLUEPRINT.yaml")
	content := "api:\n  line1\n  # comment\n  line2\n\nimplementation:\n  line3\n"
	writeFile(t, path, content)
	count, ok, err := sectionLineCount(path, "api")
	if err != nil {
		t.Fatalf("sectionLineCount: %v", err)
	}
	if !ok || count != 4 {
		t.Fatalf("unexpected count: %d", count)
	}

	var b strings.Builder
	b.WriteString("api:\n")
	for i := 0; i < 301; i++ {
		b.WriteString("  line\n")
	}
	writeFile(t, path, b.String())
	warnings, err := lineLimitWarnings(path)
	if err != nil {
		t.Fatalf("lineLimitWarnings: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "api exceeds") {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

func TestIndentLevel(t *testing.T) {
	if indentLevel("  foo") != 2 {
		t.Fatalf("unexpected indent")
	}
	if indentLevel("\tfoo") != 2 {
		t.Fatalf("unexpected indent for tab")
	}
	if indentLevel(" \tfoo") != 3 {
		t.Fatalf("unexpected indent for space+tab")
	}
}

func TestSplitLinesAndLabelForID(t *testing.T) {
	if got := splitLines("a\nb\n"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("unexpected split: %v", got)
	}
	if got := splitLines(""); len(got) != 0 {
		t.Fatalf("unexpected split: %v", got)
	}
	if labelForID("current") != "current" {
		t.Fatalf("unexpected label")
	}
	if labelForID("id") != "snapshot #id" {
		t.Fatalf("unexpected label")
	}
}
