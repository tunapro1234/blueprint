package api

import (
	"regexp"
	"testing"

	"blueprint/internal/bpskill"
)

// The agent hint bp installs points agents at these MCP tools; every tool it
// names must exist, or an agent is sent looking for something bp lacks.
func TestHintNamesOnlyRealMCPTools(t *testing.T) {
	known := map[string]bool{}
	for _, tool := range mcpToolList {
		known[tool.Name] = true
	}
	named := regexp.MustCompile("`(bp_[a-z_]+)`").FindAllSubmatch(bpskill.Hint, -1)
	if len(named) == 0 {
		t.Fatal("the hint names no MCP tool")
	}
	for _, match := range named {
		if name := string(match[1]); !known[name] {
			t.Errorf("the hint names %s, which bp mcp does not serve", name)
		}
	}
}
