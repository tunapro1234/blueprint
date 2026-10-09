package modules

import (
	"os"
	"strings"
	"testing"
)

func TestCompactionHooksOffByDefault(t *testing.T) {
	f := newFixture(t, "")
	if Detect(f.cfg, f.env)[CompactionHooks] {
		t.Fatal("compaction-hooks detected on an install that never asked for it")
	}
}

func TestCompactionHooksConflicts(t *testing.T) {
	f := newFixture(t, "modules: {}\n")
	if got := compactionHooksConflicts(f.env); len(got) != 1 || !strings.Contains(got[0], "sessions is off") {
		t.Fatalf("conflicts without sessions = %v", got)
	}
	f.env.Config.Modules = map[string]bool{Sessions: true}
	if got := compactionHooksConflicts(f.env); len(got) != 0 {
		t.Fatalf("clean setup conflicts: %v", got)
	}
}

func TestEnableDisableCompactionHooksWritesAndRemovesArtifacts(t *testing.T) {
	f := newFixture(t, "modules: {}\n")
	f.env.Config.Modules = map[string]bool{Sessions: true}
	must(t, os.MkdirAll(f.home, 0700))

	result, err := Enable(f.env, CompactionHooks, Options{})
	must(t, err)
	if !result.Changed {
		t.Fatalf("enable did not change anything: %+v", result)
	}

	pluginPath := OpenCodePluginPath(f.home)
	plugin := read(t, pluginPath)
	if !strings.Contains(plugin, `"session.compacted"`) {
		t.Fatalf("plugin does not watch session.compacted:\n%s", plugin)
	}
	if !strings.Contains(plugin, "_hook") || !strings.Contains(plugin, "opencode") {
		t.Fatalf("plugin does not call bp _hook opencode:\n%s", plugin)
	}
	if !strings.Contains(plugin, "promptAsync") {
		t.Fatalf("plugin does not inject via session.promptAsync:\n%s", plugin)
	}

	scriptPath := HermesHookScriptPath(f.home)
	script := read(t, scriptPath)
	if !strings.Contains(script, "_hook hermes") {
		t.Fatalf("hermes script does not call bp _hook hermes:\n%s", script)
	}
	info, err := os.Stat(scriptPath)
	must(t, err)
	if info.Mode().Perm()&0100 == 0 {
		t.Fatalf("hermes script is not executable: %v", info.Mode())
	}

	configPath := HermesConfigPath(f.home)
	config := decodeYAMLFile(t, configPath)
	hooks, ok := config["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hermes config.yaml has no hooks key: %+v", config)
	}
	preLLM, ok := hooks["pre_llm_call"].([]any)
	if !ok || len(preLLM) != 1 {
		t.Fatalf("hermes config.yaml missing the hook entry: %+v", hooks)
	}
	entry := preLLM[0].(map[string]any)
	if entry["command"] != scriptPath {
		t.Fatalf("hook entry command = %v, want %v", entry["command"], scriptPath)
	}

	// Enabling twice adds nothing new.
	_, err = Enable(f.env, CompactionHooks, Options{})
	must(t, err)
	if got := decodeYAMLFile(t, configPath)["hooks"].(map[string]any)["pre_llm_call"].([]any); len(got) != 1 {
		t.Fatalf("second enable duplicated the hook entry: %+v", got)
	}

	disableResult, err := Disable(f.env, CompactionHooks, Options{})
	must(t, err)
	if !disableResult.Changed {
		t.Fatalf("disable reported no change: %+v", disableResult)
	}
	if _, err := os.Stat(pluginPath); !os.IsNotExist(err) {
		t.Fatal("opencode plugin left behind")
	}
	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Fatal("hermes script left behind")
	}
	// bp created ~/.hermes/config.yaml from nothing (it did not exist before
	// Enable); once the hook entry is gone, nothing of the user's is left in
	// it, so disable removes the whole file, the same as any other config
	// bp both created and fully undid.
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("disable left an empty ~/.hermes/config.yaml behind: %v", read(t, configPath))
	}
}

func TestCompactionHooksPreservesExistingHermesConfig(t *testing.T) {
	f := newFixture(t, "modules: {}\n")
	f.env.Config.Modules = map[string]bool{Sessions: true}
	must(t, os.MkdirAll(f.home, 0700))
	configPath := HermesConfigPath(f.home)
	must(t, os.MkdirAll(f.home+"/.hermes", 0700))
	original := "model: anthropic/claude-sonnet-4.6\nhooks:\n  pre_tool_call:\n    - command: /users/own-hook.sh\n"
	must(t, os.WriteFile(configPath, []byte(original), 0640))

	_, err := Enable(f.env, CompactionHooks, Options{})
	must(t, err)
	config := decodeYAMLFile(t, configPath)
	if config["model"] != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("enable lost the user's model key: %+v", config)
	}
	hooks := config["hooks"].(map[string]any)
	if preTool := hooks["pre_tool_call"].([]any); len(preTool) != 1 {
		t.Fatalf("enable touched the user's own hook: %+v", preTool)
	}

	_, err = Disable(f.env, CompactionHooks, Options{})
	must(t, err)
	config = decodeYAMLFile(t, configPath)
	if config["model"] != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("disable lost the user's model key: %+v", config)
	}
	hooks = config["hooks"].(map[string]any)
	if preTool := hooks["pre_tool_call"].([]any); len(preTool) != 1 {
		t.Fatalf("disable touched the user's own hook: %+v", preTool)
	}
	if preLLM, ok := hooks["pre_llm_call"].([]any); ok && len(preLLM) != 0 {
		t.Fatalf("disable left bp's entry behind: %+v", preLLM)
	}
}

func TestCompactionHooksDisableKeepsUserEditedPlugin(t *testing.T) {
	f := newFixture(t, "modules: {}\n")
	f.env.Config.Modules = map[string]bool{Sessions: true}
	must(t, os.MkdirAll(f.home, 0700))

	_, err := Enable(f.env, CompactionHooks, Options{})
	must(t, err)
	pluginPath := OpenCodePluginPath(f.home)
	must(t, os.WriteFile(pluginPath, []byte(opencodePluginMarker+"\n// the user edited this\n"), 0600))

	result, err := Disable(f.env, CompactionHooks, Options{})
	must(t, err)
	if len(result.Kept) == 0 {
		t.Fatalf("disable did not report the edited plugin as kept: %+v", result)
	}
	if _, err := os.Stat(pluginPath); err != nil {
		t.Fatal("disable removed the user's edited plugin")
	}
}
