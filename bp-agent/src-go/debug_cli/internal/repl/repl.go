package repl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tunapro1234/base-agent/src-go/debug_cli/internal/client"
	"github.com/tunapro1234/base-agent/src-go/debug_cli/internal/models"
	"github.com/tunapro1234/base-agent/src-go/debug_cli/internal/render"
)

// REPL provides an interactive CLI loop.
type REPL struct {
	Config  models.CLIConfig
	Client  *client.HTTPClient
	History []models.ChatMessage
	In      io.Reader
	Out     io.Writer
}

// New constructs a REPL instance.
func New(cfg models.CLIConfig, clientInstance *client.HTTPClient) *REPL {
	return &REPL{Config: cfg, Client: clientInstance, In: os.Stdin, Out: os.Stdout}
}

// Run starts the interactive loop.
func (r *REPL) Run() {
	if r.In == nil {
		r.In = os.Stdin
	}
	if r.Out == nil {
		r.Out = os.Stdout
	}
	if r.Client == nil {
		r.Client = client.NewHTTPClient(r.Config.BaseURL, r.Config.Token)
	}

	render.Banner(r.Out, r.Config)
	scanner := bufio.NewScanner(r.In)
	for {
		fmt.Fprint(r.Out, "> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			if r.handleCommand(line) {
				break
			}
			continue
		}
		r.send(line)
	}
	if err := scanner.Err(); err != nil {
		render.Error(r.Out, err)
	}
}

func (r *REPL) handleCommand(line string) bool {
	fields := strings.Fields(line)
	cmd := strings.TrimPrefix(fields[0], "/")
	args := strings.TrimSpace(strings.TrimPrefix(line, "/"+cmd))

	switch cmd {
	case "exit", "quit":
		return true
	case "help":
		render.Help(r.Out)
	case "system":
		if args == "" {
			render.Info(r.Out, fmt.Sprintf("system prompt: %s", r.Config.SystemPrompt))
			return false
		}
		r.Config.SystemPrompt = strings.TrimSpace(args)
		render.Info(r.Out, "system prompt updated")
	case "provider":
		if args == "" {
			render.Info(r.Out, fmt.Sprintf("provider: %s", r.Config.Provider))
			return false
		}
		r.Config.Provider = strings.TrimSpace(args)
		render.Info(r.Out, "provider updated")
	case "model":
		if args == "" {
			render.Info(r.Out, fmt.Sprintf("model: %s", r.Config.Model))
			return false
		}
		r.Config.Model = strings.TrimSpace(args)
		render.Info(r.Out, "model updated")
	case "temp":
		if args == "" {
			render.Info(r.Out, fmt.Sprintf("temperature: %.2f", r.Config.Temperature))
			return false
		}
		val, err := strconv.ParseFloat(strings.TrimSpace(args), 64)
		if err != nil {
			render.Error(r.Out, err)
			return false
		}
		r.Config.Temperature = val
		render.Info(r.Out, "temperature updated")
	case "debug":
		if args == "" {
			r.Config.Debug = !r.Config.Debug
			render.Info(r.Out, fmt.Sprintf("debug: %v", r.Config.Debug))
			return false
		}
		flag, err := parseOnOff(args)
		if err != nil {
			render.Error(r.Out, err)
			return false
		}
		r.Config.Debug = flag
		render.Info(r.Out, fmt.Sprintf("debug: %v", r.Config.Debug))
	case "tasks":
		limit := 10
		if args != "" {
			if val, err := strconv.Atoi(strings.TrimSpace(args)); err == nil {
				limit = val
			}
		}
		r.listTasks(limit)
	case "history":
		render.History(r.Out, r.History)
	case "reset":
		r.History = nil
		render.Info(r.Out, "history cleared")
	case "config":
		render.Config(r.Out, r.Config)
	case "base":
		if args == "" {
			render.Info(r.Out, fmt.Sprintf("base: %s", r.Config.BaseURL))
			return false
		}
		r.Config.BaseURL = strings.TrimSpace(args)
		r.Client = client.NewHTTPClient(r.Config.BaseURL, r.Config.Token)
		render.Info(r.Out, "base url updated")
	case "token":
		if args == "" {
			render.Info(r.Out, "token updated")
			return false
		}
		r.Config.Token = strings.TrimSpace(args)
		r.Client = client.NewHTTPClient(r.Config.BaseURL, r.Config.Token)
		render.Info(r.Out, "token updated")
	default:
		render.Info(r.Out, "unknown command, type /help")
	}
	return false
}

func (r *REPL) send(line string) {
	r.History = append(r.History, models.ChatMessage{Role: "user", Content: line})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	temp := r.Config.Temperature
	req := models.ExecuteRequest{
		Instruction:  line,
		SystemPrompt: r.Config.SystemPrompt,
		Provider:     r.Config.Provider,
		Model:        r.Config.Model,
		Temperature:  &temp,
		Debug:        r.Config.Debug,
	}
	resp, err := r.Client.Execute(ctx, req)
	if err != nil {
		render.Error(r.Out, err)
		return
	}
	if resp.Output != "" {
		r.History = append(r.History, models.ChatMessage{Role: "assistant", Content: resp.Output})
	}
	render.Response(r.Out, resp, r.Config.Debug)
}

func (r *REPL) listTasks(limit int) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tasks, err := r.Client.ListTasks(ctx, limit)
	if err != nil {
		render.Error(r.Out, err)
		return
	}
	render.Tasks(r.Out, tasks)
}

func parseOnOff(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "1", "yes":
		return true, nil
	case "off", "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid value: %s", value)
	}
}
