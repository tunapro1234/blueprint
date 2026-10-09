package modules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var guardEntry = map[string]any{
	"matcher": "Read|Bash",
	"hooks":   []any{map[string]any{"type": "command", "command": "bp guard hook claude", "timeout": 5}},
}

func TestJSONEntryRoundTripIsByteIdentical(t *testing.T) {
	cases := map[string]string{
		"user hooks": `{
  "model": "opus",
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "mine"}]}
    ],
    "Stop": []
  },
  "env": {"A": "1"}
}
`,
		"no hooks key":             "{\n\t\"model\": \"opus\",\n\t\"env\": {}\n}\n",
		"hooks without PreToolUse": `{"hooks": {"Stop": [{"hooks": []}]}}`,
		"empty array":              `{"hooks": {"PreToolUse": [ ]}}`,
		"empty object":             "{}\n",
	}
	for name, original := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			must(t, os.WriteFile(path, []byte(original), 0640))
			change, err := AddJSONEntry(path, "/hooks/PreToolUse", guardEntry)
			must(t, err)
			data, _ := os.ReadFile(path)
			var doc struct {
				Hooks struct{ PreToolUse []json.RawMessage }
			}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatalf("invalid JSON after add: %v\n%s", err, data)
			}
			if last := doc.Hooks.PreToolUse[len(doc.Hooks.PreToolUse)-1]; !strings.Contains(string(last), "bp guard hook claude") {
				t.Fatalf("entry not appended:\n%s", data)
			}
			// Adding again is a no-op and records nothing.
			again, err := AddJSONEntry(path, "/hooks/PreToolUse", guardEntry)
			must(t, err)
			if again != nil {
				t.Fatalf("second add returned a change: %+v", again)
			}
			if reread, _ := os.ReadFile(path); string(reread) != string(data) {
				t.Fatalf("second add changed the file:\n%s", reread)
			}
			if _, err := undoJSONEntry(*change); err != nil {
				t.Fatal(err)
			}
			if after, _ := os.ReadFile(path); string(after) != original {
				t.Fatalf("not byte-identical:\nwant %q\ngot  %q", original, after)
			}
			if info, _ := os.Stat(path); info.Mode().Perm() != 0640 {
				t.Fatalf("mode = %v", info.Mode())
			}
		})
	}
}

func TestJSONEntryCreatesAndRemovesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	change, err := AddJSONEntry(path, "/hooks/PreToolUse", guardEntry)
	must(t, err)
	if change.Created != "/" {
		t.Fatalf("created = %q", change.Created)
	}
	_, err = undoJSONEntry(*change)
	must(t, err)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file bp created was left behind")
	}
}

func TestJSONEntryUndoKeepsUserChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	must(t, os.WriteFile(path, []byte(`{"hooks": {"PreToolUse": [{"matcher": "Bash"}]}}`), 0600))
	change, err := AddJSONEntry(path, "/hooks/PreToolUse", guardEntry)
	must(t, err)

	// The user appends an entry of their own after ours: undo removes only ours.
	data, _ := os.ReadFile(path)
	withUser := strings.Replace(string(data), "}]}}", `}, {"matcher": "Write"}]}}`, 1)
	if withUser == string(data) {
		t.Fatalf("fixture edit failed: %s", data)
	}
	must(t, os.WriteFile(path, []byte(withUser), 0600))
	_, err = undoJSONEntry(*change)
	must(t, err)
	if after, _ := os.ReadFile(path); string(after) != `{"hooks": {"PreToolUse": [{"matcher": "Bash"}, {"matcher": "Write"}]}}` {
		t.Fatalf("after undo: %s", after)
	}

	// The user edits our entry: undo leaves it and reports it.
	change, err = AddJSONEntry(path, "/hooks/PreToolUse", guardEntry)
	must(t, err)
	data, _ = os.ReadFile(path)
	edited := strings.Replace(string(data), `"timeout":5`, `"timeout":30`, 1)
	must(t, os.WriteFile(path, []byte(edited), 0600))
	if _, err := undoJSONEntry(*change); err != errModified {
		t.Fatalf("err = %v", err)
	}
	if after, _ := os.ReadFile(path); string(after) != edited {
		t.Fatal("edited entry changed")
	}
}

func TestJSONEntryFollowsSymlinkAndJournals(t *testing.T) {
	f := newFixture(t, "modules: {}\n")
	dir := t.TempDir()
	target := filepath.Join(dir, "real.json")
	link := filepath.Join(dir, "settings.json")
	original := "{\n  \"theme\": \"dark\"\n}\n"
	must(t, os.WriteFile(target, []byte(original), 0600))
	must(t, os.Symlink(target, link))
	change, err := AddJSONEntry(link, "/hooks/PreToolUse", guardEntry)
	must(t, err)
	journal, err := LoadJournal(f.cfg, "guard-hooks")
	must(t, err)
	journal.Record(*change)
	journal.Record(*change)
	if len(journal.Changes) != 1 {
		t.Fatalf("duplicate change recorded: %+v", journal.Changes)
	}
	must(t, journal.Save(f.cfg))
	journal, err = LoadJournal(f.cfg, "guard-hooks")
	must(t, err)
	done, kept := journal.Undo(f.env)
	if len(done) != 1 || len(kept) != 0 {
		t.Fatalf("done=%v kept=%v", done, kept)
	}
	if info, _ := os.Lstat(link); info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced by a file")
	}
	if after, _ := os.ReadFile(target); string(after) != original {
		t.Fatalf("not byte-identical: %q", after)
	}
}

func TestJSONEntryAlreadyPresentIsNotRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	mine, _ := json.Marshal(guardEntry)
	original := `{"hooks": {"PreToolUse": [` + string(mine) + `]}}`
	must(t, os.WriteFile(path, []byte(original), 0600))
	change, err := AddJSONEntry(path, "/hooks/PreToolUse", guardEntry)
	must(t, err)
	if change != nil {
		t.Fatalf("the user's own identical hook was claimed by bp: %+v", change)
	}
	if after, _ := os.ReadFile(path); string(after) != original {
		t.Fatal("file changed")
	}
}

func TestJSONEntryNumbersAndDuplicateKeys(t *testing.T) {
	if jsonEqual([]byte(`{"n": 9007199254740993}`), []byte(`{"n": 9007199254740992}`)) {
		t.Fatal("large integers compared through float64")
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	// Parsers use the last of duplicate keys; so must bp.
	must(t, os.WriteFile(path, []byte(`{"hooks": {"PreToolUse": [1]}, "hooks": {"PreToolUse": []}}`), 0600))
	_, err := AddJSONEntry(path, "/hooks/PreToolUse", guardEntry)
	must(t, err)
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), `{"hooks": {"PreToolUse": [1]}, "hooks": {"PreToolUse": [{`) {
		t.Fatalf("edited the wrong duplicate: %s", data)
	}
}

func TestJSONEntryRefusesDanglingSymlinkAndConcurrentWrite(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "settings.json")
	must(t, os.Symlink(filepath.Join(dir, "missing", "real.json"), link))
	if _, err := AddJSONEntry(link, "/hooks/PreToolUse", guardEntry); err == nil {
		t.Fatal("dangling symlink replaced")
	}
	if info, _ := os.Lstat(link); info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink gone")
	}
}
