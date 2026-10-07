package claudeacct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultTokenURL is Claude Code's OAuth token endpoint.
	DefaultTokenURL = "https://platform.claude.com/v1/oauth/token"
	// DefaultUsageURL is the OAuth usage endpoint Claude Code's /usage reads.
	DefaultUsageURL = "https://api.anthropic.com/api/oauth/usage"
	// ClientID is Claude Code's public OAuth client id.
	ClientID       = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	oauthBeta      = "oauth-2025-04-20"
	userAgent      = "bp-claude-accounts"
	maxBody        = 1 << 20
	defaultTimeout = 15 * time.Second
)

// Client talks to the token and usage endpoints. Both URLs are injectable so
// tests run against httptest servers.
type Client struct {
	HTTP     *http.Client
	TokenURL string
	UsageURL string
	Now      func() time.Time
}

func (c *Client) httpClient() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: defaultTimeout}
}

func (c *Client) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// RefreshError classifies a failed refresh. Kind is one of invalid_grant,
// no_refresh_token (both permanent: the login is dead), invalid_client
// (systemic: the client id was rejected, no account is at fault),
// http-<code>, timeout, network or bad-response (transient).
type RefreshError struct {
	Kind   string
	Status int
}

func (e *RefreshError) Error() string {
	switch e.Kind {
	case "invalid_grant":
		return "token refresh rejected (invalid_grant): this login is dead; log in again and re-add it"
	case "no_refresh_token":
		return "stored login has no refresh token; log in again and re-add it"
	case "invalid_client":
		return "token refresh rejected (invalid_client): the OAuth client was refused; no account was changed"
	}
	return "token refresh failed (" + e.Kind + ")"
}

// Dead reports whether the login can never be refreshed again.
func (e *RefreshError) Dead() bool {
	return e.Kind == "invalid_grant" || e.Kind == "no_refresh_token"
}

// Refresh exchanges the refresh token. The result carries the rotated
// refresh token when the server sent one; callers must persist it before
// doing anything else.
func (c *Client) Refresh(ctx context.Context, current Tokens) (Tokens, error) {
	if current.RefreshToken == "" {
		return Tokens{}, &RefreshError{Kind: "no_refresh_token"}
	}
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": current.RefreshToken,
		"client_id":     ClientID,
	})
	url := DefaultTokenURL
	if c != nil && c.TokenURL != "" {
		url = c.TokenURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Tokens{}, &RefreshError{Kind: "network"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Tokens{}, &RefreshError{Kind: transportKind(err)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Tokens{}, &RefreshError{Kind: transportKind(err)}
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error json.RawMessage `json:"error"`
		}
		var code string
		if json.Unmarshal(data, &failure) == nil {
			_ = json.Unmarshal(failure.Error, &code)
		}
		switch resp.StatusCode {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
			if code == "invalid_grant" || code == "invalid_client" {
				return Tokens{}, &RefreshError{Kind: code, Status: resp.StatusCode}
			}
		}
		return Tokens{}, &RefreshError{Kind: "http-" + strconv.Itoa(resp.StatusCode), Status: resp.StatusCode}
	}
	var answer struct {
		AccessToken  string   `json:"access_token"`
		RefreshToken string   `json:"refresh_token"`
		ExpiresIn    *float64 `json:"expires_in"`
		Scope        string   `json:"scope"`
	}
	if err := json.Unmarshal(data, &answer); err != nil || answer.AccessToken == "" || answer.ExpiresIn == nil || *answer.ExpiresIn <= 0 {
		return Tokens{}, &RefreshError{Kind: "bad-response", Status: resp.StatusCode}
	}
	next := Tokens{
		AccessToken:  answer.AccessToken,
		RefreshToken: current.RefreshToken,
		ExpiresAt:    c.now().UnixMilli() + int64(math.Round(*answer.ExpiresIn*1000)),
		Scopes:       current.Scopes,
	}
	if answer.RefreshToken != "" {
		next.RefreshToken = answer.RefreshToken
	}
	if scopes := strings.Fields(answer.Scope); len(scopes) > 0 {
		next.Scopes = scopes
	}
	return next, nil
}

// UsageError classifies a failed usage fetch: http-<code>, timeout, network
// or bad-response, with the server's Retry-After when it sent one.
type UsageError struct {
	Kind       string
	RetryAfter time.Duration
	HasRetry   bool
}

func (e *UsageError) Error() string {
	if e.HasRetry {
		return fmt.Sprintf("usage fetch failed (%s, retry after %s)", e.Kind, e.RetryAfter.Round(time.Second))
	}
	return "usage fetch failed (" + e.Kind + ")"
}

// FetchUsage reads the usage endpoint with an access token.
func (c *Client) FetchUsage(ctx context.Context, accessToken string) (*Usage, error) {
	url := DefaultUsageURL
	if c != nil && c.UsageURL != "" {
		url = c.UsageURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &UsageError{Kind: "network"}
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", oauthBeta)
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, &UsageError{Kind: transportKind(err)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, &UsageError{Kind: transportKind(err)}
	}
	if resp.StatusCode != http.StatusOK {
		failure := &UsageError{Kind: "http-" + strconv.Itoa(resp.StatusCode)}
		if raw := strings.TrimSpace(resp.Header.Get("Retry-After")); raw != "" {
			if seconds, err := strconv.ParseFloat(raw, 64); err == nil && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
				failure.RetryAfter = time.Duration(math.Max(0, seconds) * float64(time.Second))
				failure.HasRetry = true
			}
		}
		return nil, failure
	}
	usage, err := ParseUsage(data)
	if err != nil {
		return nil, &UsageError{Kind: "bad-response"}
	}
	return usage, nil
}

func transportKind(err error) string {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "timeout"
	}
	return "network"
}
