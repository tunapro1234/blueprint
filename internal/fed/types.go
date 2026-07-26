// Package fed implements blueprint's hub-and-client message federation.
package fed

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxMessageBytes = 16 * 1024
	MaxNameRunes    = 64
	DefaultRate     = 300
)

type Message struct {
	ID   string  `json:"id"`
	To   string  `json:"to"`
	From string  `json:"from"`
	Msg  string  `json:"msg"`
	TS   float64 `json:"ts"`
}

type Peer struct {
	Token  string   `json:"token"`
	Expose []string `json:"expose"`
}

// ParseAddress separates a local target from agent@peer federation syntax.
func ParseAddress(value string) (agent, peer string, federated bool, err error) {
	if !strings.Contains(value, "@") {
		return value, "", false, nil
	}
	if strings.Count(value, "@") != 1 {
		return "", "", true, fmt.Errorf("invalid federated address: %s", value)
	}
	agent, peer, _ = strings.Cut(value, "@")
	if err := validateName(agent); err != nil {
		return "", "", true, fmt.Errorf("invalid federated address %q: agent: %w", value, err)
	}
	if err := validateName(peer); err != nil {
		return "", "", true, fmt.Errorf("invalid federated address %q: peer: %w", value, err)
	}
	return agent, peer, true, nil
}

func validateName(value string) error {
	if value == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("name must be valid UTF-8")
	}
	if utf8.RuneCountInString(value) > MaxNameRunes {
		return fmt.Errorf("name exceeds %d characters", MaxNameRunes)
	}
	for _, char := range value {
		if unicode.IsControl(char) || char == '/' || char == '\\' {
			return fmt.Errorf("name contains an invalid character")
		}
	}
	return nil
}

func sanitize(value string) string {
	return strings.Map(func(char rune) rune {
		if unicode.IsControl(char) && char != '\n' {
			return -1
		}
		return char
	}, value)
}
