// Package hop assembles and extracts the bundle that `bp hop` moves between
// machines: a single agent's conversation transcript and, optionally, its
// working-directory files, described by a manifest. The bundle is a gzip-
// compressed tar; the transport (internal/p2p) streams it over an encrypted,
// peer-authenticated libp2p connection. Nothing here opens a network
// connection or touches the agentbook — the caller does both — so the bundle
// logic stays pure and testable.
package hop

import (
	"fmt"
	"strings"
)

// Format is the bundle layout version. A reader refuses a bundle whose format
// it does not understand rather than guessing.
const Format = 1

// manifestName is the first entry in every bundle tar. It is read before any
// payload file so the receiver knows what is coming.
const manifestName = "manifest.json"

// Payload roots inside the tar. Every non-manifest entry lives under exactly
// one of these; the extractor rejects anything else, which is the path-
// traversal guard's first gate.
const (
	workRoot       = "work/"
	transcriptRoot = "transcript/"
)

// AgentSpec is the portable subset of an agentbook record needed to resume the
// agent on the destination. It deliberately omits machine-specific runtime
// fields (live PID binding, status timestamps, source-absolute launch paths):
// the destination rebuilds a clean launch from harness + thread + folder, the
// same way `bp attach` revives a stopped agent.
type AgentSpec struct {
	Name          string `json:"name"`
	Harness       string `json:"harness"`             // claude, codex, opencode, hermes
	ThreadID      string `json:"threadId,omitempty"`  // conversation/identity id to resume
	SessionID     string `json:"sessionId,omitempty"` // transcript session id (claude)
	Folder        string `json:"folder,omitempty"`    // SOURCE absolute folder, a hint only
	FolderBase    string `json:"folderBase,omitempty"`// basename, the default target subdir
	Model         string `json:"model,omitempty"`
	ClaudeAccount string `json:"claudeAccount,omitempty"`
	Nickname      string `json:"nickname,omitempty"`
	Role          string `json:"role,omitempty"`
	Class         string `json:"class,omitempty"`
	Color         string `json:"color,omitempty"`
}

// FileEntry records one payload file. Path is relative to the bundle root and
// always begins with a known payload root.
type FileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Mode uint32 `json:"mode"`
}

// Manifest is the bundle's table of contents, stored as the first tar entry.
type Manifest struct {
	Format       int         `json:"format"`
	CreatedAt    string      `json:"createdAt"`
	Agent        AgentSpec   `json:"agent"`
	IncludeFiles bool        `json:"includeFiles"`
	TotalBytes   int64       `json:"totalBytes"`
	Files        []FileEntry `json:"files"`
	// Source names where the bundle came from, for the operator's audit trail.
	// It is advisory text, never trusted for authorization (the transport
	// authenticates the peer independently).
	Source Origin `json:"source,omitempty"`
}

// Origin is advisory provenance shown to the operator on the receiving side.
type Origin struct {
	Host  string `json:"host,omitempty"`
	Agent string `json:"agent,omitempty"`
}

// Validate checks a manifest read off the wire before any file is written.
func (m Manifest) Validate() error {
	if m.Format != Format {
		return fmt.Errorf("unsupported bundle format %d (need %d)", m.Format, Format)
	}
	if !ValidName(m.Agent.Name) {
		return fmt.Errorf("invalid agent name %q", m.Agent.Name)
	}
	if m.Agent.Harness == "" {
		return fmt.Errorf("manifest missing harness")
	}
	if m.TotalBytes < 0 {
		return fmt.Errorf("negative total bytes")
	}
	var sum int64
	for _, f := range m.Files {
		if err := safePayloadPath(f.Path); err != nil {
			return err
		}
		if f.Size < 0 {
			return fmt.Errorf("file %q has negative size", f.Path)
		}
		sum += f.Size
	}
	if sum != m.TotalBytes {
		return fmt.Errorf("manifest total %d does not match file sum %d", m.TotalBytes, sum)
	}
	return nil
}

// hasParentComponent reports whether any slash-separated component of p is a
// real parent-directory traversal segment ("..") rather than merely a name that
// contains ".." as a substring. Filenames like "do..ts.txt" are valid and common
// in dependency trees (e.g. node_modules fixtures), so they must not be rejected;
// only a "/../" component can escape the extraction root.
func hasParentComponent(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// safePayloadPath rejects anything that could escape the extraction root or is
// not under a known payload root. It is intentionally strict: a surprising
// entry fails the bundle rather than being skipped silently.
func safePayloadPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return fmt.Errorf("unsafe bundle path %q", p)
	}
	if hasParentComponent(p) {
		return fmt.Errorf("bundle path %q contains a .. component", p)
	}
	for _, r := range p {
		if r == 0 {
			return fmt.Errorf("bundle path contains NUL")
		}
	}
	if !strings.HasPrefix(p, workRoot) && !strings.HasPrefix(p, transcriptRoot) {
		return fmt.Errorf("bundle path %q outside known roots", p)
	}
	return nil
}

// ValidName accepts the agent-name shape bp uses elsewhere: a nonempty token
// without path separators, whitespace or control characters. It mirrors
// identity.ValidName without importing it, keeping this package dependency-free.
func ValidName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		if r <= ' ' || r == '/' || r == '\\' || r == ':' || r == '@' {
			return false
		}
	}
	return name != "." && name != ".."
}
