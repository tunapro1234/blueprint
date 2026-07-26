package tokens

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolverMungedAndWorktreeNames(t *testing.T) {
	root := t.TempDir()
	book := filepath.Join(root, "agentbook.json")
	writeLinesJSON(t, book, `{"agents":[{"name":"probot-studio","folder":"/srv/probot/studio"},{"name":"blog-agent","folder":"/srv/probot/studio/.worktrees/blog"},{"name":"op-main","folder":"/srv/outpost"}]}`)
	resolver := loadResolver([]string{book})

	claudePath := filepath.Join(root, "-srv-probot-studio--worktrees-blog", "session.jsonl")
	agent, owner := resolver.claudeAgent(claudePath, "")
	if agent != "probot-studio@blog" || owner != "probot-studio" {
		t.Fatalf("claude worktree=(%q,%q)", agent, owner)
	}
	agent, owner = resolver.codexAgent("/srv/probot/studio/.worktrees/blog/subdir")
	if agent != "probot-studio@blog" || owner != "probot-studio" {
		t.Fatalf("codex worktree=(%q,%q)", agent, owner)
	}
	agent, owner = resolver.codexAgent("/tmp/claude-0/-srv-outpost/id/scratchpad")
	if agent != "op-main" || owner != "op-main" {
		t.Fatalf("scratchpad=(%q,%q)", agent, owner)
	}
	agent, owner = resolver.codexAgent("/srv/probot/studio/packages/site")
	if agent != "probot-studio" || owner != "" {
		t.Fatalf("nested cwd=(%q,%q)", agent, owner)
	}
	if got := reverseMungedPath("-srv-probot-studio--worktrees-blog"); got != "/srv/probot/studio/.worktrees/blog" {
		t.Fatalf("reverseMungedPath=%q", got)
	}
	if agent, _ = resolver.codexAgent("/tmp/unresolved"); agent != "/tmp/unresolved" {
		t.Fatalf("unresolved path changed to %q", agent)
	}
	raw := resolver.normalizeRawAgent(RawRecord{Src: "claude", Agent: "-srv-probot-studio--worktrees-blog"})
	if raw.Agent != "probot-studio@blog" || raw.Owner != "probot-studio" {
		t.Fatalf("normalized raw=%+v", raw)
	}
}

func writeLinesJSON(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
