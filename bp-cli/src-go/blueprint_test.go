package bp

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadBlueprintAndValidate(t *testing.T) {
	content := "_meta:\n  version: \"1\"\nintent: test\n"
	bpObj := loadBlueprintFromDir(t, t.TempDir(), content)
	if bpObj.Path == "" || bpObj.Dir == "" || bpObj.StateDir == "" {
		t.Fatalf("expected blueprint paths to be populated")
	}
	res := bpObj.Validate()
	if !res.Valid {
		t.Fatalf("expected valid blueprint, got errors: %v", res.Errors)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", res.Warnings)
	}
}

func TestLoadBlueprintStateDirOverride(t *testing.T) {
	content := "_meta:\n  version: \"1\"\n  state_dir: \"blueprint\"\nintent: test\n"
	dir := t.TempDir()
	bpObj := loadBlueprintFromDir(t, dir, content)
	if bpObj.StateDirRel != "blueprint" {
		t.Fatalf("unexpected state dir rel: %s", bpObj.StateDirRel)
	}
	if bpObj.StateDir != filepath.Join(dir, "blueprint") {
		t.Fatalf("unexpected state dir: %s", bpObj.StateDir)
	}
}

func TestValidateSpecSkipsIntentWarning(t *testing.T) {
	content := "_meta:\n  version: \"1\"\n  type: spec\n"
	bpObj := loadBlueprintFromDir(t, t.TempDir(), content)
	res := bpObj.Validate()
	if !res.Valid {
		t.Fatalf("expected valid blueprint, got errors: %v", res.Errors)
	}
	for _, w := range res.Warnings {
		if w == "missing intent" {
			t.Fatalf("did not expect missing intent warning")
		}
	}
}

func TestGetSectionFromRef(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "external.yaml"), "api:\n  foo: bar\nimplementation:\n  structure:\n    - file.txt\n")
	bpObj := loadBlueprintFromDir(t, dir, "_meta:\n  version: \"1\"\napi: external.yaml#api\nimplementation: external.yaml\n")

	api, err := bpObj.GetSection("api")
	if err != nil {
		t.Fatalf("GetSection api: %v", err)
	}
	if api["foo"] != "bar" {
		t.Fatalf("expected api.foo=bar, got %v", api["foo"])
	}

	impl, err := bpObj.GetSection("implementation")
	if err != nil {
		t.Fatalf("GetSection implementation: %v", err)
	}
	if impl == nil || impl["structure"] == nil {
		t.Fatalf("expected implementation structure, got %v", impl)
	}
}

func TestGetSectionMissingAnchor(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "external.yaml"), "api:\n  foo: bar\n")
	bpObj := loadBlueprintFromDir(t, dir, "_meta:\n  version: \"1\"\napi: external.yaml#missing\n")
	_, err := bpObj.GetSection("api")
	if err == nil {
		t.Fatalf("expected error for missing anchor")
	}
}

func TestGetSectionInvalidType(t *testing.T) {
	bpObj := &Blueprint{Dir: t.TempDir(), Data: map[string]interface{}{"api": 123}}
	_, err := bpObj.GetSection("api")
	if !errors.Is(err, ErrInvalidSection) {
		t.Fatalf("expected ErrInvalidSection, got %v", err)
	}
}

func TestGetSectionConvertsInterfaceMap(t *testing.T) {
	bpObj := &Blueprint{Dir: t.TempDir(), Data: map[string]interface{}{"api": map[interface{}]interface{}{"foo": "bar"}}}
	sec, err := bpObj.GetSection("api")
	if err != nil {
		t.Fatalf("GetSection: %v", err)
	}
	if sec["foo"] != "bar" {
		t.Fatalf("expected converted map, got %v", sec)
	}
}

func TestImplementationStructure(t *testing.T) {
	content := "_meta:\n  version: \"1\"\nimplementation:\n  structure:\n    - dir/\n    - file.txt\n    - dep/:\n        has_blueprint: true\n"
	bpObj := loadBlueprintFromDir(t, t.TempDir(), content)
	entries, err := bpObj.ImplementationStructure()
	if err != nil {
		t.Fatalf("ImplementationStructure: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if !entries[0].DirHint || entries[0].Path != "dir/" {
		t.Fatalf("unexpected entry[0]: %+v", entries[0])
	}
	if entries[1].DirHint || entries[1].Path != "file.txt" {
		t.Fatalf("unexpected entry[1]: %+v", entries[1])
	}
	if entries[2].HasBlueprint == nil || !*entries[2].HasBlueprint {
		t.Fatalf("expected has_blueprint true, got %+v", entries[2])
	}
}

func TestImplementationStructureErrors(t *testing.T) {
	content := "_meta:\n  version: \"1\"\nimplementation:\n  structure: nope\n"
	bpObj := loadBlueprintFromDir(t, t.TempDir(), content)
	if _, err := bpObj.ImplementationStructure(); err == nil {
		t.Fatalf("expected error for non-list structure")
	}

	bpObj = &Blueprint{Dir: t.TempDir(), Data: map[string]interface{}{"implementation": map[string]interface{}{"structure": []interface{}{1}}}}
	if _, err := bpObj.ImplementationStructure(); err == nil {
		t.Fatalf("expected error for unsupported entry type")
	}
}

func TestDependencies(t *testing.T) {
	bpObj := &Blueprint{Data: map[string]interface{}{
		"dependencies": []interface{}{
			"./a",
			map[string]interface{}{"path": "./b"},
		},
	}}
	deps, err := bpObj.Dependencies()
	if err != nil {
		t.Fatalf("Dependencies: %v", err)
	}
	if !reflect.DeepEqual(deps, []string{"./a", "./b"}) {
		t.Fatalf("unexpected deps: %v", deps)
	}

	bpObj = &Blueprint{Data: map[string]interface{}{
		"dependencies": map[string]interface{}{
			"runtime":     []interface{}{map[string]interface{}{"path": "./c"}},
			"from_parent": []interface{}{map[string]interface{}{"path": "./ignored"}},
		},
	}}
	deps, err = bpObj.Dependencies()
	if err != nil {
		t.Fatalf("Dependencies: %v", err)
	}
	if !reflect.DeepEqual(deps, []string{"./c"}) {
		t.Fatalf("unexpected deps: %v", deps)
	}
}
