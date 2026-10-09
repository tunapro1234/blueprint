package modules

import (
	"os"
	"path/filepath"
	"testing"
)

// A dry run must not touch any user file. applyCompactionHooks wrote the
// Hermes hooks.pre_llm_call entry even with a dry-run journal: the entry was
// never journaled, pointed at a script the dry run did not write, and a later
// real enable found it "already there" (nil change), so disable never removed
// it.
func TestCompactionHooksDryRunWritesNothing(t *testing.T) {
	f := newFixture(t, "modules: {}\n")
	f.env.Config.Modules = map[string]bool{Sessions: true}
	must(t, os.MkdirAll(f.home, 0700))

	result, err := Enable(f.env, CompactionHooks, Options{DryRun: true})
	must(t, err)
	if len(result.Actions) == 0 {
		t.Fatalf("dry run described nothing: %+v", result)
	}
	for _, path := range []string{HermesConfigPath(f.home), HermesHookScriptPath(f.home), OpenCodePluginPath(f.home)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("dry run wrote %s (err=%v)", path, err)
		}
	}

	must(t, func() error { _, err := Enable(f.env, CompactionHooks, Options{}); return err }())
	must(t, func() error { _, err := Disable(f.env, CompactionHooks, Options{}); return err }())
	if _, err := os.Lstat(HermesConfigPath(f.home)); !os.IsNotExist(err) {
		t.Fatalf("enable+disable after a dry run left %s: %s", HermesConfigPath(f.home), read(t, HermesConfigPath(f.home)))
	}
}

// yaml.Unmarshal reads only the first document; re-encoding it dropped every
// document after "---". Both add and undo must refuse a multi-document file.
func TestYAMLEntryRefusesMultiDocument(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	original := "a: 1\n---\nb: 2\n"
	must(t, os.WriteFile(path, []byte(original), 0600))
	if change, err := AddYAMLEntry(path, "hooks.pre_llm_call", map[string]any{"command": "x"}); err == nil {
		t.Fatalf("multi-document file accepted: %+v", change)
	}
	if got := read(t, path); got != original {
		t.Fatalf("file changed:\n%s", got)
	}
	change := Change{Kind: KindYAMLEntry, Path: path, Option: "hooks.pre_llm_call", Value: `{"command":"x"}`}
	if _, err := undoYAMLEntry(change); err != errModified {
		t.Fatalf("undo on multi-document file = %v, want errModified", err)
	}
	// A comment-only file is still an empty document, not an error.
	must(t, os.WriteFile(path, []byte("# just a comment\n"), 0600))
	if _, err := AddYAMLEntry(path, "hooks.pre_llm_call", map[string]any{"command": "x"}); err != nil {
		t.Fatalf("comment-only file: %v", err)
	}
}

// Re-enabling replaced the hook script and plugin unconditionally, so a
// user's edit was lost; a file at that path bp never wrote was replaced and
// then deleted by undo.
func TestCompactionHooksKeepsUserFiles(t *testing.T) {
	f := newFixture(t, "modules: {}\n")
	f.env.Config.Modules = map[string]bool{Sessions: true}
	must(t, os.MkdirAll(f.home, 0700))
	_, err := Enable(f.env, CompactionHooks, Options{})
	must(t, err)

	// Same bytes, or bp's own last-recorded bytes: re-enable is fine.
	_, err = Enable(f.env, CompactionHooks, Options{})
	must(t, err)

	script := HermesHookScriptPath(f.home)
	edited := read(t, script) + "echo user-edit\n"
	must(t, os.WriteFile(script, []byte(edited), 0700))
	if _, err := Enable(f.env, CompactionHooks, Options{}); err == nil {
		t.Fatal("re-enable overwrote a user-edited hook script")
	}
	if read(t, script) != edited {
		t.Fatal("user edit lost")
	}

	g := newFixture(t, "modules: {}\n")
	g.env.Config.Modules = map[string]bool{Sessions: true}
	plugin := OpenCodePluginPath(g.home)
	must(t, os.MkdirAll(filepath.Dir(plugin), 0700))
	must(t, os.WriteFile(plugin, []byte("// the user's own plugin\n"), 0600))
	if _, err := Enable(g.env, CompactionHooks, Options{}); err == nil {
		t.Fatal("enable replaced a file bp never wrote")
	}
	if read(t, plugin) != "// the user's own plugin\n" {
		t.Fatal("user's plugin replaced")
	}
}
