package agent

import (
	"fmt"
	"os"
	"strings"

	"github.com/tunapro1234/base-agent/src-go/llm"
)

func loadKeysFromEnv(primary string, extrasPrefix string) []string {
	keys := []string{}
	if raw := os.Getenv(primary); raw != "" {
		for _, item := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				keys = append(keys, trimmed)
			}
		}
	}
	for i := 2; i <= 10; i++ {
		key := os.Getenv(fmt.Sprintf("%s_%d", extrasPrefix, i))
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func loadGeminiKeys() []string {
	return loadKeysFromEnv("GEMINI_API_KEY", "GEMINI_API_KEY")
}

func loadCodexKeys() []string {
	return loadKeysFromEnv("CODEX_API_KEY", "CODEX_API_KEY")
}

func loadOpusKeys() []string {
	return loadKeysFromEnv("OPUS_API_KEY", "OPUS_API_KEY")
}

func buildLLMRouter(cfg AgentConfig) (*llm.LLMRouter, error) {
	config := cfg
	defaults := DefaultAgentConfig()
	if config.Provider == "" {
		config.Provider = defaults.Provider
	}
	if config.Model == "" {
		config.Model = defaults.Model
	}
	if config.MaxIterations == 0 {
		config.MaxIterations = defaults.MaxIterations
	}
	if config.Temperature == 0 {
		config.Temperature = defaults.Temperature
	}

	router := llm.NewRouter(config.Provider)

	if keys := loadGeminiKeys(); len(keys) > 0 {
		model := config.Model
		if config.Provider != "gemini" {
			model = "gemini-3-flash-preview"
		}
		router.RegisterProvider("gemini", llm.NewGeminiAdapter(llm.GeminiConfig{
			APIKeys:     keys,
			BaseURL:     "https://generativelanguage.googleapis.com",
			Model:       model,
			Temperature: config.Temperature,
		}))
	} else if config.Provider == "gemini" {
		return nil, fmt.Errorf("gemini provider selected but no GEMINI_API_KEY found")
	}

	if keys := loadCodexKeys(); len(keys) > 0 {
		model := config.Model
		if config.Provider != "codex" {
			model = "gpt-5.2-codex"
		}
		router.RegisterProvider("codex", llm.NewCodexAdapter(llm.CodexConfig{
			APIKeys:         keys,
			Model:           model,
			ReasoningEffort: config.ReasoningEffort,
		}))
	} else if config.Provider == "codex" {
		return nil, fmt.Errorf("codex provider selected but no CODEX_API_KEY found")
	}

	if keys := loadOpusKeys(); len(keys) > 0 {
		router.RegisterProvider("opus", llm.NewOpusAdapter(llm.OpusConfig{
			APIKeys: keys,
		}))
	} else if config.Provider == "opus" {
		return nil, fmt.Errorf("opus provider selected but no OPUS_API_KEY found")
	}

	return router, nil
}
