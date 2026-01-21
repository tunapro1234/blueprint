package render

import (
	"fmt"
	"io"
	"sort"

	"github.com/tunapro1234/base-agent/src-go/debug_cli/internal/models"
)

// Banner shows startup info.
func Banner(out io.Writer, cfg models.CLIConfig) {
	fmt.Fprintln(out, "Base Agent Debug CLI")
	fmt.Fprintf(out, "API: %s\n", cfg.BaseURL)
	fmt.Fprintf(out, "Provider: %s  Model: %s  Temp: %.2f\n", cfg.Provider, cfg.Model, cfg.Temperature)
	fmt.Fprintln(out, "Type /help for commands.")
}

// Help prints command list.
func Help(out io.Writer) {
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  /help                 Show commands")
	fmt.Fprintln(out, "  /exit | /quit          Exit")
	fmt.Fprintln(out, "  /system <prompt>       Set system prompt")
	fmt.Fprintln(out, "  /provider <name>       Set provider")
	fmt.Fprintln(out, "  /model <name>          Set model")
	fmt.Fprintln(out, "  /temp <float>          Set temperature")
	fmt.Fprintln(out, "  /debug [on|off]        Toggle debug output")
	fmt.Fprintln(out, "  /tasks [limit]         List tasks")
	fmt.Fprintln(out, "  /history               Show chat history")
	fmt.Fprintln(out, "  /reset                 Clear chat history")
	fmt.Fprintln(out, "  /config                Show current config")
	fmt.Fprintln(out, "  /base <url>            Update base URL")
	fmt.Fprintln(out, "  /token <token>         Update bearer token")
}

// Response prints an execution response.
func Response(out io.Writer, resp models.ExecuteResponse, debug bool) {
	if !resp.Success && resp.Error != "" {
		fmt.Fprintf(out, "error: %s\n", resp.Error)
		return
	}
	fmt.Fprintf(out, "assistant> %s\n", resp.Output)
	if debug && resp.Trace != nil {
		fmt.Fprintln(out, "trace:")
		keys := make([]string, 0, len(resp.Trace))
		for key := range resp.Trace {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(out, "  %s: %v\n", key, resp.Trace[key])
		}
	}
}

// Tasks prints task list.
func Tasks(out io.Writer, tasks []models.TaskInfo) {
	if len(tasks) == 0 {
		fmt.Fprintln(out, "no tasks")
		return
	}
	for _, task := range tasks {
		fmt.Fprintf(out, "[%s] %s - %s\n", task.Status, task.ID, task.Instruction)
	}
}

// Config prints the current config.
func Config(out io.Writer, cfg models.CLIConfig) {
	fmt.Fprintln(out, "config:")
	fmt.Fprintf(out, "  base: %s\n", cfg.BaseURL)
	fmt.Fprintf(out, "  provider: %s\n", cfg.Provider)
	fmt.Fprintf(out, "  model: %s\n", cfg.Model)
	fmt.Fprintf(out, "  temp: %.2f\n", cfg.Temperature)
	fmt.Fprintf(out, "  debug: %v\n", cfg.Debug)
	if cfg.SystemPrompt != "" {
		fmt.Fprintf(out, "  system: %s\n", cfg.SystemPrompt)
	}
}

// History prints chat history.
func History(out io.Writer, history []models.ChatMessage) {
	if len(history) == 0 {
		fmt.Fprintln(out, "no history")
		return
	}
	for _, msg := range history {
		fmt.Fprintf(out, "%s> %s\n", msg.Role, msg.Content)
	}
}

// Info prints an informational line.
func Info(out io.Writer, msg string) {
	fmt.Fprintln(out, msg)
}

// Error prints an error line.
func Error(out io.Writer, err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(out, "error: %v\n", err)
}
