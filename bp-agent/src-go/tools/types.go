package tools

// ToolSchema defines the shape of tool metadata exposed to LLMs.
type ToolSchema struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolResult captures the outcome of a tool execution.
type ToolResult struct {
	Success bool
	Output  string
	Error   string
}

// ToolHandler executes a tool with the given arguments.
type ToolHandler func(args map[string]any) (string, error)

// ToolEntry stores tool metadata and handler.
type ToolEntry struct {
	Name    string
	Handler ToolHandler
	Schema  ToolSchema
}
