package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompatibilityWrappersPreserveExistingAliasAndFunction(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		t.Run(shell, func(t *testing.T) {
			dir := t.TempDir()
			integration := filepath.Join(dir, "shell.sh")
			if err := os.WriteFile(integration, []byte(compatibilityWrappers), 0o600); err != nil {
				t.Fatal(err)
			}
			script := "alias lush='existing-alias'\nfunction rush { echo existing-function; }\n. " + quoteShell(integration) + "\nalias lush\ntypeset -f rush\n"
			output, err := exec.Command(path, "-c", script).CombinedOutput()
			if err != nil {
				t.Fatalf("source wrappers: %v: %s", err, output)
			}
			got := string(output)
			if !strings.Contains(got, "existing-alias") || !strings.Contains(got, "existing-function") || strings.Contains(got, "command bp shell") {
				t.Fatalf("existing definitions were replaced: %s", got)
			}
		})
	}
}

func TestCompatibilityWrappersDelegateToBpCommands(t *testing.T) {
	if !strings.Contains(compatibilityWrappers, "_bp_install_compat lush attach") || !strings.Contains(compatibilityWrappers, "_bp_install_compat rush shell") {
		t.Fatal("lush/rush are not thin bp attach/shell wrappers")
	}
}
