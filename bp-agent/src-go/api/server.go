package api

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/tunapro1234/base-agent/src-go/agent"
)

// AgentServer serves HTTP endpoints.
type AgentServer struct {
	Port  int
	Agent *agent.Agent
	mux   *http.ServeMux
	mu    sync.Mutex
}

// NewAgentServer creates a server.
func NewAgentServer(port int, agentInstance *agent.Agent) *AgentServer {
	if agentInstance == nil {
		agentInstance = agent.New("api-agent", agent.DefaultAgentConfig(), "")
	}
	return &AgentServer{Port: port, Agent: agentInstance, mux: http.NewServeMux()}
}

// Start starts the HTTP server.
func (s *AgentServer) Start() error {
	s.routes()
	addr := fmt.Sprintf(":%d", s.Port)
	return http.ListenAndServe(addr, s.mux)
}

// ExecuteWithOverrides runs the agent with request overrides.
func (s *AgentServer) ExecuteWithOverrides(ctx context.Context, req ExecuteRequest) agent.AgentResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	origCfg := s.Agent.Config
	origPrompt := s.Agent.SystemPrompt

	cfg := origCfg
	if req.Provider != "" {
		cfg.Provider = req.Provider
	}
	if req.Model != "" {
		cfg.Model = req.Model
	}
	if req.Temperature != nil {
		cfg.Temperature = *req.Temperature
	}
	s.Agent.Config = cfg
	if req.SystemPrompt != "" {
		s.Agent.SystemPrompt = req.SystemPrompt
	}

	result := s.Agent.Execute(ctx, req.Instruction)

	s.Agent.Config = origCfg
	s.Agent.SystemPrompt = origPrompt

	return result
}

func (s *AgentServer) routes() {
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/tasks", s.handleTasks)
	s.mux.HandleFunc("/execute", s.handleExecute)
}
