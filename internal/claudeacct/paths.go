// Package claudeacct stores several Claude Code logins and switches the live
// login between them.
//
// Claude Code keeps one login per config home: the OAuth tokens in
// <H>/.credentials.json and the account identity in the oauthAccount key of
// the global config file G. A switch rewrites the credentials file and splices
// oauthAccount into G under Claude Code's own lock protocol, so running
// Claude Code processes pick up the new login on their next request.
//
// Nothing in this package prints, logs or returns token values. Errors and
// views carry token status only ("valid, expires in 3h", "expired").
//
// The file layout, lock protocol and OAuth calls follow the behaviour of
// claude-swap (MIT License, Copyright (c) 2026 Onur Cetinkol), reimplemented
// in Go.
package claudeacct

import (
	"fmt"
	"os"
	"path/filepath"
)

// Env is the process environment the package reads. Tests inject a temporary
// home and CLAUDE_CONFIG_DIR; production uses OSEnv.
type Env struct {
	Getenv   func(string) string
	UserHome func() (string, error)
}

// OSEnv reads the real process environment.
func OSEnv() Env {
	return Env{Getenv: os.Getenv, UserHome: os.UserHomeDir}
}

// ClaudePaths locates Claude Code's live login.
type ClaudePaths struct {
	// ConfigHome is H: $CLAUDE_CONFIG_DIR, or ~/.claude.
	ConfigHome string
	// GlobalConfig is G: $CLAUDE_CONFIG_DIR/.claude.json, or ~/.claude.json.
	GlobalConfig string
}

// Paths resolves H and G the way Claude Code does. Inside an account
// profile (CLAUDE_CONFIG_DIR names one) it resolves the default home the
// profile was made from, so account commands run by a bound agent manage the
// switchable login and never the profile's own.
func (e Env) Paths() (ClaudePaths, error) {
	if dir, set := e.DefaultConfigDir(); set {
		dir = filepath.Clean(dir)
		return ClaudePaths{ConfigHome: dir, GlobalConfig: filepath.Join(dir, ".claude.json")}, nil
	}
	userHome := e.UserHome
	if userHome == nil {
		userHome = os.UserHomeDir
	}
	home, err := userHome()
	if err != nil || home == "" {
		return ClaudePaths{}, fmt.Errorf("find home directory for Claude Code config: %v", err)
	}
	return ClaudePaths{ConfigHome: filepath.Join(home, ".claude"), GlobalConfig: filepath.Join(home, ".claude.json")}, nil
}

// CredentialsFile is H/.credentials.json.
func (p ClaudePaths) CredentialsFile() string {
	return filepath.Join(p.ConfigHome, ".credentials.json")
}

// Lock directories, in the order Claude Code takes them.
func (p ClaudePaths) refreshLock() string { return filepath.Join(p.ConfigHome, ".oauth_refresh.lock") }
func (p ClaudePaths) legacyLock() string {
	return filepath.Join(filepath.Dir(p.ConfigHome), filepath.Base(p.ConfigHome)+".lock")
}
func (p ClaudePaths) configLock() string { return p.GlobalConfig + ".lock" }
