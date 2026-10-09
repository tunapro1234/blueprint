package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"blueprint/internal/harness"
)

// Every harness that is an MCP client says how to add bp's server, and that
// command names a client bp api config knows.
func TestHarnessCatalogPointsAtRealConfigSnippets(t *testing.T) {
	for _, d := range harness.Catalog() {
		if !d.MCP.Client {
			continue
		}
		client, ok := strings.CutPrefix(d.MCP.BPSetup, "bp api config ")
		if !ok {
			t.Errorf("%s: bp_setup = %q, want \"bp api config <client>\"", d.Kind, d.MCP.BPSetup)
			continue
		}
		for _, url := range []string{"", "http://127.0.0.1:1/mcp"} {
			if _, err := ClientSnippet(client, url); err != nil {
				t.Errorf("%s: %v", d.Kind, err)
			}
		}
	}
}

// docs/mcp.md is what the install hint's MCP tools point at: it must name
// every tool bp mcp serves, and only those, and every client it lists.
func TestMCPDocCoversEveryToolAndClient(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "mcp.md"))
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, tool := range mcpToolList {
		known[tool.Name] = true
		if !strings.Contains(string(doc), "`"+tool.Name+"`") {
			t.Errorf("docs/mcp.md does not document %s", tool.Name)
		}
	}
	for _, match := range regexp.MustCompile("`(bp_[a-z_]+)`").FindAllSubmatch(doc, -1) {
		if name := string(match[1]); !known[name] {
			t.Errorf("docs/mcp.md names %s, which bp mcp does not serve", name)
		}
	}
	for client := range stdioSnippets {
		if !strings.Contains(string(doc), "`bp api config "+client+"`") {
			t.Errorf("docs/mcp.md does not list bp api config %s", client)
		}
	}
}
