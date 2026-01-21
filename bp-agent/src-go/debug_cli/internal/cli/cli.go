package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/tunapro1234/base-agent/src-go/debug_cli/internal/models"
)

const (
	envBaseURL   = "BASE_AGENT_URL"
	envToken     = "BASE_AGENT_TOKEN"
	envProvider  = "BASE_AGENT_PROVIDER"
	envModel     = "BASE_AGENT_MODEL"
	envSystem    = "BASE_AGENT_SYSTEM_PROMPT"
	envTemp      = "BASE_AGENT_TEMPERATURE"
	envDebug     = "BASE_AGENT_DEBUG"
	defaultURL   = "http://localhost:8080"
	defaultModel = "gemini-3-pro-preview"
	defaultProv  = "gemini"
)

// ParseConfig parses CLI flags and environment variables into a config.
func ParseConfig(args []string) (models.CLIConfig, error) {
	cfg := models.CLIConfig{
		BaseURL:      envOr(envBaseURL, defaultURL),
		Provider:     envOr(envProvider, defaultProv),
		Model:        envOr(envModel, defaultModel),
		SystemPrompt: envOr(envSystem, ""),
		Temperature:  envFloat(envTemp, 0.3),
		Debug:        envBool(envDebug, false),
		Token:        envOr(envToken, ""),
	}

	fs := flag.NewFlagSet("debug-cli", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.StringVar(&cfg.BaseURL, "base", cfg.BaseURL, "Base API URL")
	fs.StringVar(&cfg.Provider, "provider", cfg.Provider, "Provider: gemini, codex, opus")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "Model name")
	fs.StringVar(&cfg.SystemPrompt, "system", cfg.SystemPrompt, "System prompt override")
	fs.Float64Var(&cfg.Temperature, "temp", cfg.Temperature, "Sampling temperature")
	fs.BoolVar(&cfg.Debug, "debug", cfg.Debug, "Enable debug output")
	fs.StringVar(&cfg.Token, "token", cfg.Token, "Bearer token")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(os.Stdout)
			return cfg, err
		}
		usage(os.Stderr)
		return cfg, err
	}

	return cfg, nil
}

func usage(out io.Writer) {
	fmt.Fprintln(out, "debug-cli flags:")
	fmt.Fprintln(out, "  -base URL          Base API URL (default http://localhost:8080)")
	fmt.Fprintln(out, "  -provider NAME     Provider: gemini, codex, opus")
	fmt.Fprintln(out, "  -model NAME        Model name")
	fmt.Fprintln(out, "  -system PROMPT     System prompt override")
	fmt.Fprintln(out, "  -temp FLOAT        Sampling temperature")
	fmt.Fprintln(out, "  -debug             Enable debug output")
	fmt.Fprintln(out, "  -token TOKEN       Bearer token")
}

func envOr(key, fallback string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	val, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return val
}

func envFloat(key string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	val, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return val
}
