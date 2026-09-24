package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenPassesRecordedArgsAfterBinaryWithoutDuplicates(t *testing.T) {
	id := "019a0d02-a847-76d1-ba01-8b67fbe755c1"
	h := &launchHarness{capture: "› Ask Codex to do anything\n"}
	opts := OpenOptions{Codex: true, NoSandbox: true, Resume: true, ResumeID: id, NoPrompt: true, Args: []string{"--yolo", "-m", "gpt-x"}}
	if err := h.client().Open(context.Background(), "claude-w", "/work", opts, nil); err != nil {
		t.Fatal(err)
	}
	var launch string
	for _, call := range h.calls {
		if strings.HasPrefix(call, "send-keys") && strings.Contains(call, "codex") {
			launch = call
		}
	}
	want := "CODEX_BWRAPPED=1 command codex '-m' 'gpt-x' --dangerously-bypass-approvals-and-sandbox resume '" + id + "'"
	if !strings.Contains(launch, want) {
		t.Fatalf("launch=%q want %q", launch, want)
	}
	h = &launchHarness{capture: "bypass permissions\n"}
	if err := h.client().Open(context.Background(), "claude-main", "/work", OpenOptions{NoPrompt: true, Args: []string{"--dangerously-skip-permissions", "--model", "opus"}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, call := range h.calls {
		if strings.HasPrefix(call, "send-keys") && strings.Contains(call, " claude ") {
			launch = call
		}
	}
	if strings.Count(launch, "--dangerously-skip-permissions") != 1 || !strings.Contains(launch, "PREFIX=claude-main command claude '--model' 'opus' --dangerously-skip-permissions") {
		t.Fatalf("claude launch=%q", launch)
	}
}

func TestRemoteCodexResumeDropsPermissionOverridesButKeepsOtherFlags(t *testing.T) {
	id := "019a0d02-a847-76d1-ba01-8b67fbe755c1"
	args := []string{
		"--yolo", "--dangerously-bypass-approvals-and-sandbox", "--full-auto",
		"-s", "workspace-write", "-s=read-only", "--sandbox", "read-only",
		"-a", "on-request", "-a=never", "--ask-for-approval=never", "--ask-for-approval", "untrusted",
		"-c", "sandbox_mode=workspace-write", "-c=sandbox_workspace_write.writable_roots=[\"/tmp\"]",
		"--config=approval_policy=never", "--config", "permissions/permission_profile.default=untrusted",
		"--add-dir", "/private/a", "--add-dir=/private/b",
		"-m", "gpt-6-sol", "--profile", "worker", "-c", "model=gpt-6-sol", "--config=model_reasoning_effort=medium", "--search",
	}
	h := &launchHarness{capture: "› Ask Codex to do anything\n"}
	opts := OpenOptions{Codex: true, NoSandbox: true, Resume: true, ResumeID: id, Remote: "unix://", NoPrompt: true, Args: args}
	if err := h.client().Open(context.Background(), "agent", "/work", opts, nil); err != nil {
		t.Fatal(err)
	}
	var launch string
	for _, call := range h.calls {
		if strings.HasPrefix(call, "send-keys") && strings.Contains(call, "command codex") {
			launch = call
			break
		}
	}
	for _, want := range []string{"CODEX_BWRAPPED=1 command codex", "--remote 'unix://'", "resume '" + id + "'", "'-c' 'model=gpt-6-sol'", "'--config=model_reasoning_effort=medium'", "'--search'"} {
		if !strings.Contains(launch, want) {
			t.Errorf("remote resume launch %q missing %q", launch, want)
		}
	}
	for _, forbidden := range []string{
		"--dangerously-bypass-approvals-and-sandbox", "--yolo", "--full-auto",
		"--sandbox", "workspace-write", "read-only", "--ask-for-approval", "on-request", "never", "untrusted", "approval_policy",
		"sandbox_mode", "sandbox_workspace_write", "permission_profile", "--add-dir", "/private/a", "/private/b",
	} {
		if strings.Contains(launch, forbidden) {
			t.Errorf("remote resume launch retained permission override %q: %s", forbidden, launch)
		}
	}
}

func TestRemoteFreshAndLocalCodexResumeKeepBypassBehavior(t *testing.T) {
	id := "019a0d02-a847-76d1-ba01-8b67fbe755c1"
	for _, test := range []struct {
		name   string
		opts   OpenOptions
		resume bool
	}{
		{name: "fresh remote", opts: OpenOptions{Codex: true, NoSandbox: true, Remote: "unix://", NoPrompt: true, Args: []string{"--full-auto", "-c", "sandbox_mode=workspace-write"}}},
		{name: "local resume", opts: OpenOptions{Codex: true, NoSandbox: true, Resume: true, ResumeID: id, NoPrompt: true, Args: []string{"--full-auto", "-c", "sandbox_mode=workspace-write"}}, resume: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &launchHarness{capture: "› Ask Codex to do anything\n"}
			if err := h.client().Open(context.Background(), "agent", "/work", test.opts, nil); err != nil {
				t.Fatal(err)
			}
			var launch string
			for _, call := range h.calls {
				if strings.HasPrefix(call, "send-keys") && strings.Contains(call, "command codex") {
					launch = call
					break
				}
			}
			if !strings.Contains(launch, "CODEX_BWRAPPED=1 command codex") || !strings.Contains(launch, "--dangerously-bypass-approvals-and-sandbox") {
				t.Fatalf("launch lost bypass behavior: %s", launch)
			}
			if !strings.Contains(launch, "--full-auto") || !strings.Contains(launch, "sandbox_mode=workspace-write") {
				t.Fatalf("launch changed recorded arguments: %s", launch)
			}
			if test.resume && !strings.Contains(launch, "resume '"+id+"'") {
				t.Fatalf("local resume lost its thread id: %s", launch)
			}
			if !test.resume && !strings.Contains(launch, "--remote 'unix://'") {
				t.Fatalf("fresh remote launch lost its endpoint: %s", launch)
			}
		})
	}
}

func TestDirectLaunchBypassesInteractiveShellAliases(t *testing.T) {
	h := &launchHarness{capture: "bypass permissions\n"}
	if err := h.client().Open(context.Background(), "agent", "/work", OpenOptions{
		NoPrompt: true,
		Args:     []string{"--model", "opus"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	var launch string
	for _, call := range h.calls {
		if strings.HasPrefix(call, "send-keys -t =agent: ") && strings.HasSuffix(call, " Enter") && strings.Contains(call, "claude") {
			launch = strings.TrimSuffix(strings.TrimPrefix(call, "send-keys -t =agent: "), " Enter")
			break
		}
	}
	if launch == "" || !strings.Contains(launch, "PREFIX=agent command claude") {
		t.Fatalf("direct Claude launch does not bypass interactive aliases: %q", launch)
	}

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(root, "args")
	envPath := filepath.Join(root, "prefix")
	fake := filepath.Join(binDir, "claude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE_ARGS\"\nprintf '%s\\n' \"$CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX\" > \"$CAPTURE_ENV\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fake, filepath.Join(binDir, "fake-claude")); err != nil {
		t.Fatal(err)
	}
	rc := "claude() { fake-claude --function-added \"$@\"; }\nalias claude='fake-claude --alias-added'\n"
	for _, file := range []string{".bashrc", ".zshrc"} {
		if err := os.WriteFile(filepath.Join(home, file), []byte(rc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "bash", args: []string{"--noprofile", "-i", "-c"}},
		{name: "zsh", args: []string{"-i", "-c"}},
	} {
		shell, err := exec.LookPath(test.name)
		if err != nil {
			t.Logf("%s unavailable; skipping shell alias check", test.name)
			continue
		}
		cmd := exec.Command(shell, append(test.args, launch)...)
		cmd.Env = []string{
			"HOME=" + home,
			"ZDOTDIR=" + home,
			"PATH=" + path,
			"CAPTURE_ARGS=" + argsPath,
			"CAPTURE_ENV=" + envPath,
			"TERM=dumb",
		}
		probe := exec.Command(shell, append(test.args, "CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=agent claude --probe")...)
		probe.Env = cmd.Env
		if output, err := probe.CombinedOutput(); err != nil {
			t.Fatalf("%s alias control failed: %v\n%s", test.name, err, output)
		}
		data, err := os.ReadFile(argsPath)
		if err != nil {
			t.Fatalf("%s alias control did not call the fake harness: %v", test.name, err)
		}
		probeArgs := strings.Split(strings.TrimSpace(string(data)), "\n")
		if strings.Join(probeArgs, "\x00") != "--alias-added\x00--probe" {
			t.Fatalf("%s control did not prove alias expansion is active: got %q", test.name, probeArgs)
		}
		if err := os.Remove(argsPath); err != nil {
			t.Fatal(err)
		}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s launch failed: %v\n%s", test.name, err, output)
		}
		data, err = os.ReadFile(argsPath)
		if err != nil {
			t.Fatalf("%s did not call the fake harness: %v", test.name, err)
		}
		got := strings.Split(strings.TrimSpace(string(data)), "\n")
		want := []string{"--model", "opus", "--dangerously-skip-permissions"}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("%s alias/function changed argv: got %q want %q", test.name, got, want)
		}
		if data, err := os.ReadFile(envPath); err != nil || strings.TrimSpace(string(data)) != "agent" {
			t.Fatalf("%s launch lost the session-name environment assignment: %q, %v", test.name, data, err)
		}
		if err := os.Remove(argsPath); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaudeResumeDoesNotRepeatRecordedPermissionFlag(t *testing.T) {
	id := "019a0d02-a847-76d1-ba01-8b67fbe755c1"
	claudeHome := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	transcriptDir := filepath.Join(claudeHome, "projects", mungeProjectPath("/work"))
	if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &launchHarness{capture: "bypass permissions\n"}
	opts := OpenOptions{
		Resume: true, ResumeID: id, NoPrompt: true,
		Args: []string{"--dangerously-skip-permissions", "--model", "opus"},
	}
	if err := h.client().Open(context.Background(), "claude-worker", "/work", opts, nil); err != nil {
		t.Fatal(err)
	}
	var launch string
	for _, call := range h.calls {
		if strings.HasPrefix(call, "send-keys") && strings.Contains(call, "--resume") {
			launch = call
		}
	}
	if strings.Count(launch, "--dangerously-skip-permissions") != 1 || !strings.Contains(launch, "--resume '"+id+"'") || !strings.Contains(launch, "'--model' 'opus'") {
		t.Fatalf("resumed Claude launch=%q", launch)
	}
}
