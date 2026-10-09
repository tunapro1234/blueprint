package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"

	"blueprint/internal/guard"
)

// APIConfig controls bp's HTTP API (internal/api). Nothing listens unless it
// is enabled: the daemon then serves the per-user unix socket, and TCP only
// when Listen names a loopback address.
type APIConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Listen is an optional 127.0.0.1:PORT (or [::1]:PORT) for TCP clients.
	Listen  string         `json:"listen,omitempty" yaml:"listen,omitempty"`
	Gateway *GatewayConfig `json:"gateway,omitempty" yaml:"gateway,omitempty"`
}

// GatewayConfig is the remote MCP gateway for web chat apps. It is off by
// default and listens on loopback only; a TLS reverse proxy publishes it.
// Each client sees only what its expose list names.
type GatewayConfig struct {
	Enabled bool   `json:"enabled" yaml:"enabled"`
	Listen  string `json:"listen" yaml:"listen"`
	// PublicURL is the MCP endpoint as clients reach it (https://host/mcp).
	// OAuth metadata and token audiences are derived from it.
	PublicURL string                   `json:"publicUrl,omitempty" yaml:"publicUrl,omitempty"`
	Clients   map[string]GatewayClient `json:"clients,omitempty" yaml:"clients,omitempty"`
}

// GatewayClient is one remote client (a ChatGPT or Claude.ai connector). It
// appears to local agents as the inbox agent of the same name.
type GatewayClient struct {
	Agents         []string `json:"agents,omitempty" yaml:"agents,omitempty"`
	Rooms          []string `json:"rooms,omitempty" yaml:"rooms,omitempty"`
	Boards         []string `json:"boards,omitempty" yaml:"boards,omitempty"`
	ReadOnlyBoards []string `json:"readOnlyBoards,omitempty" yaml:"readOnlyBoards,omitempty"`
	// RatePerHour and Burst bound sends and room posts; MaxBytes bounds one
	// text. Zero uses guard's defaults (120 per hour, burst 20, 16 KiB).
	RatePerHour int `json:"ratePerHour,omitempty" yaml:"ratePerHour,omitempty"`
	Burst       int `json:"burst,omitempty" yaml:"burst,omitempty"`
	MaxBytes    int `json:"maxBytes,omitempty" yaml:"maxBytes,omitempty"`
	// Redact applies to local text the client reads (secrets always; emails
	// and extra patterns on request).
	Redact guard.RedactPolicy `json:"redact,omitempty" yaml:"redact,omitempty"`
}

var apiName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func validateAPI(value *APIConfig) error {
	if value == nil {
		return nil
	}
	if value.Listen != "" {
		if err := loopbackAddress(value.Listen); err != nil {
			return fmt.Errorf("listen: %w", err)
		}
	}
	gateway := value.Gateway
	if gateway == nil {
		return nil
	}
	if gateway.Enabled && gateway.Listen == "" {
		return fmt.Errorf("gateway.listen is required when the gateway is enabled")
	}
	if gateway.Listen != "" {
		if err := loopbackAddress(gateway.Listen); err != nil {
			return fmt.Errorf("gateway.listen: %w (publish it through a TLS reverse proxy)", err)
		}
	}
	if gateway.PublicURL != "" {
		u, err := url.Parse(gateway.PublicURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopbackAddress(u.Host) == nil)) {
			return fmt.Errorf("gateway.publicUrl must be an https URL")
		}
	}
	for name, client := range gateway.Clients {
		if !apiName.MatchString(name) {
			return fmt.Errorf("gateway client %q: invalid name", name)
		}
		for _, list := range [][]string{client.Agents, client.Rooms, client.Boards, client.ReadOnlyBoards} {
			for _, item := range list {
				if !apiName.MatchString(item) {
					return fmt.Errorf("gateway client %s: invalid name %q", name, item)
				}
			}
		}
		// guard skips a pattern that does not compile; say so here instead
		// of redacting less than the owner asked for.
		for _, expr := range client.Redact.Patterns {
			if _, err := regexp.Compile(expr); err != nil {
				return fmt.Errorf("gateway client %s: redact pattern %q: %v", name, expr, err)
			}
		}
		if client.RatePerHour < 0 || client.Burst < 0 || client.MaxBytes < 0 {
			return fmt.Errorf("gateway client %s: ratePerHour, burst and maxBytes must not be negative", name)
		}
	}
	return nil
}

func loopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return fmt.Errorf("%q is not host:port", address)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%q is not a loopback address", address)
	}
	return nil
}
