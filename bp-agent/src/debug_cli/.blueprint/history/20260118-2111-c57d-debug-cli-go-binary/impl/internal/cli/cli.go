package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"debug_cli/internal/client"
	"debug_cli/internal/models"
	"debug_cli/internal/repl"
)

func Run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 1
	}
	cmd := args[0]
	switch cmd {
	case "health":
		fs, cfg := newFlagSet("health")
		if err := fs.Parse(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		c := client.New(*cfg)
		payload, err := c.Health()
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		printJSON(payload)
		return 0
	case "run":
		fs, cfg := newFlagSet("run")
		instruction := fs.String("instruction", "", "Instruction text")
		if err := fs.Parse(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		text := strings.TrimSpace(*instruction)
		if text == "" {
			if fs.NArg() > 0 {
				text = strings.TrimSpace(strings.Join(fs.Args(), " "))
			} else {
				text = readStdin()
			}
		}
		if text == "" {
			fmt.Fprintln(os.Stderr, "no instruction provided")
			return 1
		}
		req := models.ExecuteRequest{
			Instruction:  text,
			SystemPrompt: emptyToNil(cfg.SystemPrompt),
			Provider:     emptyToNil(cfg.Provider),
			Model:        emptyToNil(cfg.Model),
			Temperature:  floatPtrIfSet(cfg.Temperature),
			Debug:        boolPtrIfSet(cfg.Debug),
		}
		c := client.New(*cfg)
		resp, err := c.Execute(req)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		fmt.Fprintln(os.Stdout, resp.Output)
		if cfg.Debug && resp.Trace != nil {
			printTrace(resp.Trace)
		}
		return 0
	case "repl":
		fs, cfg := newFlagSet("repl")
		if err := fs.Parse(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		c := client.New(*cfg)
		if err := repl.Run(c, *cfg); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		return 0
	case "tasks":
		fs, cfg := newFlagSet("tasks")
		limit := fs.Int("limit", 10, "Max tasks")
		if err := fs.Parse(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		c := client.New(*cfg)
		payload, err := c.Tasks(*limit)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		printJSON(payload)
		return 0
	default:
		printUsage()
		return 1
	}
}

func newFlagSet(name string) (*flag.FlagSet, *models.DebugConfig) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := &models.DebugConfig{}
	fs.StringVar(&cfg.BaseURL, "url", "http://localhost:8080", "Base URL")
	fs.StringVar(&cfg.Token, "token", "", "Auth token")
	fs.StringVar(&cfg.Provider, "provider", "", "Provider")
	fs.StringVar(&cfg.Model, "model", "", "Model")
	fs.Float64Var(&cfg.Temperature, "temp", 0, "Temperature")
	fs.StringVar(&cfg.SystemPrompt, "system", "", "System prompt")
	fs.BoolVar(&cfg.Debug, "debug", false, "Include trace output")
	return fs, cfg
}

func printJSON(payload map[string]any) {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stdout, payload)
		return
	}
	fmt.Fprintln(os.Stdout, string(data))
}

func printTrace(trace map[string]any) {
	data, err := json.MarshalIndent(trace, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stdout, trace)
		return
	}
	fmt.Fprintln(os.Stdout, string(data))
}

func readStdin() string {
	bytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(bytes))
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  debug-cli health [--url URL]")
	fmt.Println("  debug-cli run --instruction \"Hello\" [--provider id]")
	fmt.Println("  debug-cli repl [--provider id]")
	fmt.Println("  debug-cli tasks [--limit N]")
}

func emptyToNil(value string) string {
	return strings.TrimSpace(value)
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
