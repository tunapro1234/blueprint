package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
)

// ExecuteRequest is the POST /execute payload.
type ExecuteRequest struct {
	Instruction  string   `json:"instruction"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	Model        string   `json:"model,omitempty"`
	Temperature  *float64 `json:"temperature,omitempty"`
	Debug        bool     `json:"debug,omitempty"`
}

// ExecuteResponse is the POST /execute response.
type ExecuteResponse struct {
	Success bool            `json:"success"`
	Output  string          `json:"output"`
	TaskID  string          `json:"task_id,omitempty"`
	Trace   map[string]any  `json:"trace,omitempty"`
	Error   string          `json:"error,omitempty"`
}

func (s *AgentServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": Version})
}

func (s *AgentServer) handleTasks(w http.ResponseWriter, r *http.Request) {
	if s.Agent.Tasks == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "task store disabled"})
		return
	}
	limit := 10
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	tasks := s.Agent.Tasks.List(limit)
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *AgentServer) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	var req ExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if req.Instruction == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "instruction required"})
		return
	}
	result := s.ExecuteWithOverrides(context.Background(), req)
	resp := ExecuteResponse{
		Success: result.Success,
		Output:  result.Output,
		TaskID:  result.TaskID,
		Trace:   result.Trace,
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	data, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}
