package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"debug_cli/internal/models"
)

type DebugClient struct {
	cfg  models.DebugConfig
	http *http.Client
}

func New(cfg models.DebugConfig) *DebugClient {
	return &DebugClient{
		cfg:  cfg,
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *DebugClient) Health() (map[string]any, error) {
	return c.doJSON("GET", "/health", nil)
}

func (c *DebugClient) Execute(req models.ExecuteRequest) (models.ExecuteResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return models.ExecuteResponse{}, err
	}
	resp, err := c.doRaw("POST", "/execute", payload)
	if err != nil {
		return models.ExecuteResponse{}, err
	}
	var out models.ExecuteResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		return models.ExecuteResponse{}, err
	}
	return out, nil
}

func (c *DebugClient) Tasks(limit int) (map[string]any, error) {
	endpoint := fmt.Sprintf("/tasks?limit=%d", limit)
	return c.doJSON("GET", endpoint, nil)
}

func (c *DebugClient) doJSON(method, path string, payload []byte) (map[string]any, error) {
	body, err := c.doRaw(method, path, payload)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return map[string]any{}, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (c *DebugClient) doRaw(method, path string, payload []byte) ([]byte, error) {
	base := c.cfg.BaseURL
	if base == "" {
		base = "http://localhost:8080"
	}
	base = strings.TrimRight(base, "/")
	endpoint := base + path
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}
