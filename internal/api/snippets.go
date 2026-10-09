package api

import (
	"fmt"
	"sort"
	"strings"
)

// ClientSnippet returns how to register bp's MCP server with one client. With
// an empty url it is the stdio server `bp mcp`; otherwise the local
// streamable HTTP endpoint, authenticated with the token from bp api token.
func ClientSnippet(client, url string) (string, error) {
	snippets := stdioSnippets
	if url != "" {
		snippets = httpSnippets
	}
	text, ok := snippets[strings.ToLower(client)]
	if !ok {
		names := make([]string, 0, len(snippets))
		for name := range snippets {
			names = append(names, name)
		}
		sort.Strings(names)
		return "", fmt.Errorf("unknown client %q; known: %s", client, strings.Join(names, ", "))
	}
	return strings.ReplaceAll(text, "URL", url), nil
}

// Snippets are checked against each client's documentation (2026-10); see
// docs/api.md for sources.
var stdioSnippets = map[string]string{
	"claude": `# Claude Code: run once (user scope = every project)
claude mcp add --scope user bp -- bp mcp

# or commit .mcp.json at the project root:
{"mcpServers": {"bp": {"type": "stdio", "command": "bp", "args": ["mcp"]}}}
`,
	"codex": `# Codex: run once
codex mcp add bp -- bp mcp

# or add to ~/.codex/config.toml:
[mcp_servers.bp]
command = "bp"
args = ["mcp"]
`,
	"gemini": `# Gemini CLI: run once (user scope)
gemini mcp add -s user bp bp mcp

# or add to ~/.gemini/settings.json:
{"mcpServers": {"bp": {"command": "bp", "args": ["mcp"]}}}
`,
	"antigravity": `# Antigravity: add to ~/.gemini/config/mcp_config.json
# (workspace: .agents/mcp_config.json)
{"mcpServers": {"bp": {"command": "bp", "args": ["mcp"]}}}
`,
	"grok": `# Grok CLI (xAI): run once
grok mcp add bp -- bp mcp

# or add to ~/.grok/config.toml:
[mcp_servers.bp]
command = "bp"
args = ["mcp"]
# Grok also reads Claude Code's and Cursor's MCP entries.
`,
	"opencode": `# OpenCode: add to ~/.config/opencode/opencode.json (or opencode.json in the project)
{"$schema": "https://opencode.ai/config.json",
 "mcp": {"bp": {"type": "local", "command": ["bp", "mcp"], "enabled": true}}}
`,
	"cursor": `# Cursor: add to ~/.cursor/mcp.json (or .cursor/mcp.json in the project)
{"mcpServers": {"bp": {"type": "stdio", "command": "bp", "args": ["mcp"]}}}
`,
	"hermes": `# Hermes Agent: run once
hermes mcp add bp --command bp --args mcp

# or add to ~/.hermes/config.yaml:
mcp_servers:
  bp:
    command: "bp"
    args: ["mcp"]
`,
}

var httpSnippets = map[string]string{
	"claude": `# Claude Code over local HTTP (export BP_TOKEN=$(bp api token) first)
claude mcp add --scope user --transport http bp URL --header "Authorization: Bearer $BP_TOKEN" --header "X-BP-Agent: <your-name>"
`,
	"codex": `# ~/.codex/config.toml (export BP_TOKEN=$(bp api token))
[mcp_servers.bp]
url = "URL"
bearer_token_env_var = "BP_TOKEN"
http_headers = { "X-BP-Agent" = "<your-name>" }
`,
	"gemini": `# ~/.gemini/settings.json
{"mcpServers": {"bp": {"httpUrl": "URL", "headers": {"Authorization": "Bearer <token from bp api token>", "X-BP-Agent": "<your-name>"}}}}
`,
	"antigravity": `# ~/.gemini/config/mcp_config.json
{"mcpServers": {"bp": {"serverUrl": "URL", "headers": {"Authorization": "Bearer <token from bp api token>", "X-BP-Agent": "<your-name>"}}}}
`,
	"grok": `# ~/.grok/config.toml (export BP_TOKEN=$(bp api token))
[mcp_servers.bp]
url = "URL"
headers = { "Authorization" = "Bearer ${BP_TOKEN}", "X-BP-Agent" = "<your-name>" }
`,
	"opencode": `# opencode.json
{"mcp": {"bp": {"type": "remote", "url": "URL", "headers": {"Authorization": "Bearer <token from bp api token>", "X-BP-Agent": "<your-name>"}}}}
`,
	"cursor": `# ~/.cursor/mcp.json (export BP_TOKEN=$(bp api token))
{"mcpServers": {"bp": {"url": "URL", "headers": {"Authorization": "Bearer ${env:BP_TOKEN}", "X-BP-Agent": "<your-name>"}}}}
`,
	"hermes": `# ~/.hermes/config.yaml (export BP_TOKEN=$(bp api token))
mcp_servers:
  bp:
    url: "URL"
    headers: { Authorization: "Bearer ${BP_TOKEN}", X-BP-Agent: "<your-name>" }
`,
}
