// Package ntfy sends push notifications to an ntfy topic.
package ntfy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	URL   string `json:"url"`
	Topic string `json:"topic"`
	Token string `json:"token,omitempty"`
}

var client = &http.Client{Timeout: 10 * time.Second}

func Send(ctx context.Context, config *Config, text string) error {
	if config == nil {
		return nil
	}
	target := strings.TrimRight(config.URL, "/") + "/" + url.PathEscape(config.Topic)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(text))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if config.Token != "" {
		request.Header.Set("Authorization", "Bearer "+config.Token)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned %s", response.Status)
	}
	return nil
}
