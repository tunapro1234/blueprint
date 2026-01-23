package api

import (
	agent "github.com/tunapro1234/base-agent/src-go"
	"github.com/tunapro1234/base-agent/src-go/llm"
	"github.com/tunapro1234/base-agent/src-go/task"
	"github.com/tunapro1234/base-agent/src-go/tools"
)

const Version = "0.1.0"

type Agent = agent.Agent
type AgentConfig = agent.AgentConfig
type AgentResult = agent.AgentResult

type ToolRegistry = tools.ToolRegistry
type ToolSchema = tools.ToolSchema
type ToolResult = tools.ToolResult

type TaskStore = task.TaskStore
type Task = task.Task
type TaskStatus = task.TaskStatus

type LLMRouter = llm.LLMRouter
type ProviderAdapter = llm.ProviderAdapter
type CompletionRequest = llm.CompletionRequest
type LLMResponse = llm.LLMResponse
type Message = llm.Message
