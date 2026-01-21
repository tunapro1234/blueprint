package agent

import (
	"context"
	"fmt"

	"github.com/tunapro1234/base-agent/src-go/llm"
	"github.com/tunapro1234/base-agent/src-go/task"
	"github.com/tunapro1234/base-agent/src-go/tools"
)

// Agent executes instructions using an LLM router and tools.
type Agent struct {
	Name         string
	Config       AgentConfig
	SystemPrompt string
	Router       *llm.LLMRouter
	Tools        *tools.ToolRegistry
	Tasks        *task.TaskStore
}

// New creates a new Agent.
func New(name string, cfg AgentConfig, systemPrompt string) *Agent {
	if name == "" {
		name = "agent"
	}
	if systemPrompt == "" {
		systemPrompt = "You are a helpful assistant."
	}
	defaults := DefaultAgentConfig()
	if cfg.Provider == "" {
		cfg.Provider = defaults.Provider
	}
	if cfg.Model == "" {
		cfg.Model = defaults.Model
	}
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = defaults.MaxIterations
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = defaults.Temperature
	}
	router, err := buildLLMRouter(cfg)
	if err != nil {
		panic(err)
	}
	var store *task.TaskStore
	if cfg.EnableTaskStore {
		store = task.NewTaskStore(false, "")
	}
	return &Agent{
		Name:         name,
		Config:       cfg,
		SystemPrompt: systemPrompt,
		Router:       router,
		Tools:        tools.NewToolRegistry(),
		Tasks:        store,
	}
}

// AddTool registers a tool.
func (a *Agent) AddTool(name string, handler tools.ToolHandler, schema tools.ToolSchema) error {
	return a.Tools.Register(name, handler, schema)
}

// Execute runs an instruction.
func (a *Agent) Execute(ctx context.Context, instruction string) AgentResult {
	var taskID string
	if a.Tasks != nil {
		t := a.Tasks.Create(instruction)
		taskID = t.ID
	}

	messages := []llm.Message{
		{Role: "system", Content: a.SystemPrompt},
		{Role: "user", Content: instruction},
	}

	var toolSchemas []tools.ToolSchema
	if a.Tools.Count() > 0 {
		toolSchemas = a.Tools.GetSchemas()
	}

	for i := 0; i < a.Config.MaxIterations; i++ {
		temp := a.Config.Temperature
		request := llm.CompletionRequest{
			Messages:    messages,
			Tools:       toolSchemas,
			Temperature: &temp,
			Model:       a.Config.Model,
			Provider:    a.Config.Provider,
		}
		response, err := a.Router.Complete(ctx, request)
		if err != nil {
			if a.Tasks != nil {
				_, _ = a.Tasks.Update(taskID, task.TaskFailed, "", err.Error())
			}
			return AgentResult{Success: false, Output: "", TaskID: taskID}
		}
		if len(response.ToolCalls) == 0 {
			if a.Tasks != nil {
				_, _ = a.Tasks.Update(taskID, task.TaskCompleted, response.Content, "")
			}
			return AgentResult{Success: true, Output: response.Content, TaskID: taskID}
		}

		messages = append(messages, llm.Message{Role: "assistant", Content: response.Content})
		for _, call := range response.ToolCalls {
			result := a.Tools.Execute(call.Name, call.Args)
			if !result.Success {
				messages = append(messages, llm.Message{Role: "user", Content: fmt.Sprintf("Tool %s error: %s", call.Name, result.Error)})
				continue
			}
			messages = append(messages, llm.Message{Role: "user", Content: fmt.Sprintf("Tool %s result: %s", call.Name, result.Output)})
		}
	}

	if a.Tasks != nil {
		_, _ = a.Tasks.Update(taskID, task.TaskFailed, "", "max iterations reached")
	}
	return AgentResult{Success: false, Output: "", TaskID: taskID}
}
