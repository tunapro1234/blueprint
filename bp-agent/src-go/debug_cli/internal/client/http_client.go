package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/tunapro1234/base-agent/src-go/debug_cli/internal/models"
)

// HTTPClient talks to the Base Agent API.
type HTTPClient struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

// NewHTTPClient constructs a client.
func NewHTTPClient(baseURL, token string) *HTTPClient {
	return &HTTPClient{
		BaseURL: baseURL,
		Token:   token,
		Client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Execute calls POST /execute.
func (c *HTTPClient) Execute(ctx context.Context, req models.ExecuteRequest) (models.ExecuteResponse, error) {
	endpoint, err := c.resolve("/execute")
	if err != nil {
		return models.ExecuteResponse{}, err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return models.ExecuteResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return models.ExecuteResponse{}, err
	}
	c.applyHeaders(httpReq)

	resp, err := c.Client.Do(httpReq)
	if err != nil {
		return models.ExecuteResponse{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return models.ExecuteResponse{}, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	var out models.ExecuteResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return models.ExecuteResponse{}, err
	}
	return out, nil
}

// ListTasks calls GET /tasks.
func (c *HTTPClient) ListTasks(ctx context.Context, limit int) ([]models.TaskInfo, error) {
	endpoint, err := c.resolve("/tasks")
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(httpReq)

	resp, err := c.Client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	var payload struct {
		Tasks []models.TaskInfo `json:"tasks"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload.Tasks, nil
}

func (c *HTTPClient) resolve(path string) (string, error) {
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", err
	}
	rel, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(rel).String(), nil
}

func (c *HTTPClient) applyHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}
