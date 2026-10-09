package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"blueprint/internal/release"
)

// A2A shapes (https://a2a-protocol.org, v1.0, with v0.3 accepted on input).
// A bp agent is an A2A agent; one message sent to it is one A2A task whose
// work is delivering that message. The task completes when the message
// reaches the agent; the agent's answer, if any, is a separate message back.

// A2A protocol versions. A request with no A2A-Version header is 0.3.
const (
	a2aV1  = "1.0"
	a2aV03 = "0.3"
)

// A2APart is one part of a message. v1.0 tells the kinds apart by which
// member is set; v0.3 also sent "kind". Only text and data parts carry
// meaning here; data parts are rendered as JSON text.
type A2APart struct {
	Kind      string          `json:"kind,omitempty"`
	Text      *string         `json:"text,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Raw       string          `json:"raw,omitempty"`
	URL       string          `json:"url,omitempty"`
	File      json.RawMessage `json:"file,omitempty"`
	MediaType string          `json:"mediaType,omitempty"`
	Filename  string          `json:"filename,omitempty"`
	Metadata  map[string]any  `json:"metadata,omitempty"`
}

// A2AMessage is the A2A Message object.
type A2AMessage struct {
	Kind      string         `json:"kind,omitempty"` // v0.3 only
	MessageID string         `json:"messageId"`
	ContextID string         `json:"contextId,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	Role      string         `json:"role"`
	Parts     []A2APart      `json:"parts"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// A2ATaskStatus is the A2A TaskStatus object.
type A2ATaskStatus struct {
	State     string      `json:"state"`
	Message   *A2AMessage `json:"message,omitempty"`
	Timestamp string      `json:"timestamp,omitempty"`
}

// A2ATask is the A2A Task object.
type A2ATask struct {
	Kind      string         `json:"kind,omitempty"` // v0.3 only
	ID        string         `json:"id"`
	ContextID string         `json:"contextId"`
	Status    A2ATaskStatus  `json:"status"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// a2aError is an A2A error with its JSON-RPC code, HTTP status and REST reason.
type a2aError struct {
	Code   int
	HTTP   int
	Reason string
	Msg    string
}

func (e *a2aError) Error() string { return e.Msg }

var (
	errTaskNotFound = func(id string) *a2aError {
		return &a2aError{-32001, 404, "TASK_NOT_FOUND", "task not found: " + id}
	}
	errNotCancelable = func(msg string) *a2aError {
		return &a2aError{-32002, 400, "TASK_NOT_CANCELABLE", msg}
	}
	errUnsupported = func(msg string) *a2aError {
		return &a2aError{-32004, 400, "UNSUPPORTED_OPERATION", msg}
	}
	errContentType = func(msg string) *a2aError {
		return &a2aError{-32005, 400, "CONTENT_TYPE_NOT_SUPPORTED", msg}
	}
	errVersion = func(v string) *a2aError {
		return &a2aError{-32009, 400, "VERSION_NOT_SUPPORTED", "A2A version not supported: " + v + " (supported: 1.0, 0.3)"}
	}
)

// a2aText joins a message's parts into the text bp delivers.
func a2aText(m A2AMessage) (string, error) {
	if len(m.Parts) == 0 {
		return "", invalid("message has no parts")
	}
	var pieces []string
	for _, part := range m.Parts {
		switch {
		case part.Text != nil:
			pieces = append(pieces, *part.Text)
		case len(part.Data) > 0:
			pieces = append(pieces, string(part.Data))
		default:
			return "", errContentType("only text and data parts can be delivered to an agent")
		}
	}
	return strings.Join(pieces, "\n"), nil
}

// a2aState maps a bp delivery state to an A2A task state.
func a2aState(state, version string) string {
	v1 := map[string]string{
		StateAccepted:   "TASK_STATE_SUBMITTED",
		StateUnverified: "TASK_STATE_WORKING",
		StateDelivered:  "TASK_STATE_COMPLETED",
		StateFailed:     "TASK_STATE_FAILED",
		StateCanceled:   "TASK_STATE_CANCELED",
	}[state]
	if v1 == "" {
		v1 = "TASK_STATE_UNSPECIFIED"
	}
	if version == a2aV1 {
		return v1
	}
	old := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(v1, "TASK_STATE_"), "_", "-"))
	if old == "unspecified" {
		old = "unknown"
	}
	return old
}

func a2aRole(version string) string {
	if version == a2aV1 {
		return "ROLE_AGENT"
	}
	return "agent"
}

// a2aTask renders a send result as an A2A task.
func a2aTask(result SendResult, version string, now time.Time) A2ATask {
	task := A2ATask{ID: result.ID, ContextID: result.ContextID,
		Status:   A2ATaskStatus{State: a2aState(result.State, version), Timestamp: now.UTC().Format("2006-01-02T15:04:05.000Z")},
		Metadata: map[string]any{"bp/to": result.To, "bp/route": result.Route, "bp/state": result.State}}
	if task.ContextID == "" {
		task.ContextID = result.ID
	}
	if result.Reason != "" {
		text := result.Reason
		task.Status.Message = &A2AMessage{MessageID: result.ID + "-status", Role: a2aRole(version),
			Parts: []A2APart{{Text: &text}}}
	}
	if version != a2aV1 {
		task.Kind = "task"
		if task.Status.Message != nil {
			task.Status.Message.Kind = "message"
			task.Status.Message.Parts[0].Kind = "text"
		}
	}
	return task
}

// AgentCard builds the A2A agent card for one bp agent, or for the bp hub
// when agent is "". baseURL is the server's own origin.
func AgentCard(baseURL, agent, description string) map[string]any {
	name, path := "bp", "/a2a"
	if description == "" {
		description = "bp: message the AI agents on this computer. Put the target agent in message.metadata[\"bp/to\"], or use the agent's own card under /a2a/agents/<name>."
	}
	if agent != "" {
		name, path = agent, "/a2a/agents/"+agent
	}
	rest := baseURL + path
	return map[string]any{
		"name":        name,
		"description": description,
		"version":     release.Version(),
		"supportedInterfaces": []map[string]any{
			{"url": rest, "protocolBinding": "HTTP+JSON", "protocolVersion": a2aV1},
			{"url": rest + "/rpc", "protocolBinding": "JSONRPC", "protocolVersion": a2aV1},
		},
		// v0.3 fields, for clients that predate supportedInterfaces.
		"url":                rest + "/rpc",
		"preferredTransport": "JSONRPC",
		"protocolVersion":    "0.3.0",
		"capabilities":       map[string]any{"streaming": false, "pushNotifications": false},
		"securitySchemes": map[string]any{"bearer": map[string]any{
			"httpAuthSecurityScheme": map[string]any{"scheme": "Bearer", "description": "token from bp api token"}}},
		"securityRequirements": []map[string]any{{"schemes": map[string]any{"bearer": map[string]any{"list": []string{}}}}},
		"defaultInputModes":    []string{"text/plain", "application/json"},
		"defaultOutputModes":   []string{"text/plain", "application/json"},
		"skills": []map[string]any{{
			"id": "message", "name": "Message",
			"description": fmt.Sprintf("Deliver a message to %s. Delivery waits until the agent is idle; the task completes when the message reaches it.", name),
			"tags":        []string{"messaging"},
		}},
	}
}
