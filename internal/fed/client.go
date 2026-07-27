package fed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"blueprint/internal/msgq"
)

type Client struct {
	Hub        string
	Token      string
	HTTPClient *http.Client
	Now        func() time.Time
	Log        io.Writer
}

func NewClient(hub, token string) *Client {
	return &Client{
		Hub:        strings.TrimRight(hub, "/"),
		Token:      token,
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
		Now:        time.Now,
		Log:        os.Stderr,
	}
}

func (c *Client) request(ctx context.Context, method, endpoint string, body io.Reader, result any) error {
	if err := CheckHubURL(c.Hub); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, c.Hub+endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.HTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &payload)
		if payload.Error == "" {
			payload.Error = strings.TrimSpace(string(data))
		}
		return fmt.Errorf("%s: %s", response.Status, payload.Error)
	}
	if result != nil {
		if err := json.Unmarshal(data, result); err != nil {
			return fmt.Errorf("decode hub response: %w", err)
		}
	}
	return nil
}

func (c *Client) Ping(ctx context.Context) (string, time.Duration, error) {
	started := time.Now()
	var response struct {
		OK   bool   `json:"ok"`
		Peer string `json:"peer"`
	}
	if err := c.request(ctx, http.MethodGet, "/v1/ping", nil, &response); err != nil {
		return "", 0, err
	}
	if !response.OK || response.Peer == "" {
		return "", 0, fmt.Errorf("invalid ping response")
	}
	return response.Peer, time.Since(started), nil
}

func (c *Client) Send(ctx context.Context, targetPeer, to, from, text string) (string, error) {
	hubPeer, _, err := c.Ping(ctx)
	if err != nil {
		return "", fmt.Errorf("reach hub: %w", err)
	}
	if targetPeer != hubPeer {
		return "", fmt.Errorf("unknown peer: %s (this client only reaches '%s' via the hub)", targetPeer, hubPeer)
	}
	payload, err := json.Marshal(map[string]string{"to": to, "from": from, "msg": text})
	if err != nil {
		return "", err
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := c.request(ctx, http.MethodPost, "/v1/send", bytes.NewReader(payload), &response); err != nil {
		return "", fmt.Errorf("hub send failed: %w", err)
	}
	return response.ID, nil
}

func (c *Client) Poll(ctx context.Context) ([]Message, error) {
	var response struct {
		Messages []Message `json:"messages"`
	}
	if err := c.request(ctx, http.MethodGet, "/v1/poll", nil, &response); err != nil {
		return nil, err
	}
	if response.Messages == nil {
		response.Messages = []Message{}
	}
	return response.Messages, nil
}

func (c *Client) Ack(ctx context.Context, ids []string) error {
	payload, err := json.Marshal(map[string][]string{"ids": ids})
	if err != nil {
		return err
	}
	return c.request(ctx, http.MethodPost, "/v1/ack", bytes.NewReader(payload), nil)
}

func LastPollPath(stateDir string) string {
	return filepath.Join(stateDir, "fed", "last-poll")
}

func SeenPath(stateDir string) string {
	return filepath.Join(stateDir, "fed", "seen.json")
}

func (c *Client) PollAndEnqueue(ctx context.Context, stateDir string, queue *msgq.Queue) (int, error) {
	seen, err := loadSeen(SeenPath(stateDir))
	if err != nil {
		return 0, err
	}
	messages, err := c.Poll(ctx)
	if err != nil {
		return 0, err
	}
	known := make(map[string]bool, len(seen.IDs))
	for _, id := range seen.IDs {
		known[id] = true
	}
	enqueued := 0
	changed := false
	ackIDs := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.ID != "" {
			ackIDs = append(ackIDs, message.ID)
			if known[message.ID] {
				continue
			}
		}
		if _, err := queue.Enqueue(message.To, message.From, "["+message.From+"] "+message.Msg); err != nil {
			return 0, err
		}
		enqueued++
		if message.ID != "" {
			known[message.ID] = true
			seen.IDs = append(seen.IDs, message.ID)
			changed = true
		}
	}
	if changed {
		if len(seen.IDs) > MaxPeerMessages {
			seen.IDs = append([]string(nil), seen.IDs[len(seen.IDs)-MaxPeerMessages:]...)
		}
		if err := writeSeen(SeenPath(stateDir), seen); err != nil {
			return enqueued, err
		}
	}
	if len(ackIDs) > 0 {
		if err := c.Ack(ctx, ackIDs); err != nil && c.Log != nil {
			fmt.Fprintf(c.Log, "fed: ack failed: %v\n", err)
		}
	}
	if err := writeLastPoll(LastPollPath(stateDir), c.Now().UTC()); err != nil {
		return enqueued, err
	}
	return enqueued, nil
}

type seenState struct {
	IDs []string `json:"ids"`
}

func loadSeen(path string) (seenState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return seenState{IDs: []string{}}, nil
	}
	if err != nil {
		return seenState{}, err
	}
	var seen seenState
	if err := json.Unmarshal(data, &seen); err != nil {
		return seenState{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(seen.IDs) > MaxPeerMessages {
		seen.IDs = append([]string(nil), seen.IDs[len(seen.IDs)-MaxPeerMessages:]...)
	}
	return seen, nil
}

func writeSeen(path string, seen seenState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".seen-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := json.NewEncoder(tmp).Encode(seen); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

func writeLastPoll(path string, now time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".last-poll-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := fmt.Fprintln(tmp, now.Format(time.RFC3339Nano)); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func CheckHubURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("invalid hub URL")
	}
	if parsed.Scheme == "http" {
		host := strings.ToLower(parsed.Hostname())
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("refusing to send bearer token over plaintext HTTP")
		}
	}
	return nil
}
