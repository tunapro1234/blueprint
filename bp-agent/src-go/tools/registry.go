package tools

import (
	"fmt"
	"sync"
)

// ToolRegistry manages tool registration and execution.
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]ToolEntry
}

// NewToolRegistry creates a new ToolRegistry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: map[string]ToolEntry{}}
}

// Register adds a new tool to the registry.
func (r *ToolRegistry) Register(name string, handler ToolHandler, schema ToolSchema) error {
	if name == "" || handler == nil {
		return fmt.Errorf("invalid tool")
	}
	if schema.Name == "" {
		schema.Name = name
	}
	if schema.Name != name {
		return fmt.Errorf("schema name mismatch")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; ok {
		return fmt.Errorf("tool already registered")
	}
	r.tools[name] = ToolEntry{Name: name, Handler: handler, Schema: schema}
	return nil
}

// Execute runs a tool by name with args.
func (r *ToolRegistry) Execute(name string, args map[string]any) ToolResult {
	r.mu.RLock()
	entry, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return ToolResult{Success: false, Error: "tool not found"}
	}
	out, err := entry.Handler(args)
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}
	}
	return ToolResult{Success: true, Output: out}
}

// GetSchemas returns the registered tool schemas.
func (r *ToolRegistry) GetSchemas() []ToolSchema {
	r.mu.RLock()
	defer r.mu.RUnlock()
	schemas := make([]ToolSchema, 0, len(r.tools))
	for _, entry := range r.tools {
		schemas = append(schemas, entry.Schema)
	}
	return schemas
}

// Has returns true if a tool is registered.
func (r *ToolRegistry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

// Count returns the number of registered tools.
func (r *ToolRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}
