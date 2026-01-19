package repl

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"debug_cli/internal/client"
	"debug_cli/internal/models"
)

type Turn struct {
	User      string
	Assistant string
}

type Command struct {
	Name  string
	Value string
}

func Run(c *client.DebugClient, cfg models.DebugConfig) error {
	return RunWithIO(c, cfg, os.Stdin, os.Stdout)
}

func RunWithIO(c *client.DebugClient, cfg models.DebugConfig, in io.Reader, out io.Writer) error {
	state := &sessionState{
		client:  c,
		config:  cfg,
		history: []Turn{},
		writer:  out,
	}

	fmt.Fprintln(out, "Debug CLI (type /exit to quit)")
	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, "you> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			shouldExit, err := state.handleCommand(line)
			if err != nil {
				fmt.Fprintln(out, err.Error())
			}
			if shouldExit {
				break
			}
			continue
		}

		instruction := renderTranscript(state.history, line)
		req := models.ExecuteRequest{
			Instruction:  instruction,
			SystemPrompt: emptyToNil(state.config.SystemPrompt),
			Provider:     emptyToNil(state.config.Provider),
			Model:        emptyToNil(state.config.Model),
			Temperature:  floatPtrIfSet(state.config.Temperature),
			Debug:        boolPtrIfSet(state.config.Debug),
		}
		resp, err := state.client.Execute(req)
		if err != nil {
			fmt.Fprintln(out, err.Error())
			continue
		}
		fmt.Fprintf(out, "assistant> %s\n", resp.Output)
		if state.config.Debug && resp.Trace != nil {
			printTrace(out, resp.Trace)
		}
		state.history = append(state.history, Turn{User: line, Assistant: resp.Output})
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

type sessionState struct {
	client  *client.DebugClient
	config  models.DebugConfig
	history []Turn
	writer  io.Writer
}

func (s *sessionState) handleCommand(line string) (bool, error) {
	cmd, ok := ParseSlashCommand(line)
	if !ok {
		return false, nil
	}

	switch cmd.Name {
	case "exit", "quit":
		return true, nil
	case "system":
		s.config.SystemPrompt = strings.TrimSpace(cmd.Value)
	case "provider":
		s.config.Provider = strings.TrimSpace(cmd.Value)
	case "model":
		s.config.Model = strings.TrimSpace(cmd.Value)
	case "temp":
		if strings.TrimSpace(cmd.Value) == "" {
			s.config.Temperature = 0
			return false, nil
		}
		val, err := strconv.ParseFloat(strings.TrimSpace(cmd.Value), 64)
		if err != nil {
			return false, fmt.Errorf("invalid temperature")
		}
		s.config.Temperature = val
	case "debug":
		val, err := parseBool(cmd.Value)
		if err != nil {
			return false, err
		}
		s.config.Debug = val
	case "help":
		printHelp(s.writer)
	default:
		fmt.Fprintln(s.writer, "Unknown command. Use /help for options.")
	}
	return false, nil
}

func ParseSlashCommand(line string) (Command, bool) {
	if !strings.HasPrefix(line, "/") {
		return Command{}, false
	}
	trimmed := strings.TrimPrefix(line, "/")
	if trimmed == "" {
		return Command{}, false
	}
	parts := strings.SplitN(trimmed, " ", 2)
	cmd := Command{Name: strings.TrimSpace(parts[0])}
	if len(parts) > 1 {
		cmd.Value = parts[1]
	}
	return cmd, true
}

func renderTranscript(history []Turn, newMessage string) string {
	lines := make([]string, 0, len(history)*2+1)
	for _, turn := range history {
		lines = append(lines, fmt.Sprintf("user: %s", turn.User))
		lines = append(lines, fmt.Sprintf("assistant: %s", turn.Assistant))
	}
	lines = append(lines, fmt.Sprintf("user: %s", newMessage))
	return strings.Join(lines, "\n")
}

func printTrace(out io.Writer, trace map[string]any) {
	if provider, ok := trace["provider"]; ok && provider != nil {
		if model, mok := trace["model"]; mok && model != nil {
			fmt.Fprintf(out, "trace: provider=%v model=%v\n", provider, model)
		} else {
			fmt.Fprintf(out, "trace: provider=%v\n", provider)
		}
	}
	if toolCalls, ok := trace["tool_calls"]; ok && toolCalls != nil {
		fmt.Fprintln(out, "tool_calls:")
		fmt.Fprintln(out, prettyJSON(toolCalls))
	}
	if toolResults, ok := trace["tool_results"]; ok && toolResults != nil {
		fmt.Fprintln(out, "tool_results:")
		fmt.Fprintln(out, prettyJSON(toolResults))
	}
}

func prettyJSON(value any) string {
	data, err := jsonMarshalIndent(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

func jsonMarshalIndent(value any) ([]byte, error) {
	return json.MarshalIndent(value, "", "  ")
}

func parseBool(value string) (bool, error) {
	lower := strings.ToLower(strings.TrimSpace(value))
	switch lower {
	case "on", "true", "1", "yes":
		return true, nil
	case "off", "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean: %s", value)
	}
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, "/system <text>  - set system prompt")
	fmt.Fprintln(out, "/provider <id>  - set provider")
	fmt.Fprintln(out, "/model <id>     - set model")
	fmt.Fprintln(out, "/temp <float>   - set temperature")
	fmt.Fprintln(out, "/debug on|off   - toggle trace output")
	fmt.Fprintln(out, "/exit           - quit")
}

func emptyToNil(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return value
}

func floatPtrIfSet(value float64) *float64 {
	if value == 0 {
		return nil
	}
	v := value
	return &v
}

func boolPtrIfSet(value bool) *bool {
	if !value {
		return nil
	}
	v := value
	return &v
}
