package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"blueprint/commands"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: bp <command> [path] [flags]")
		os.Exit(1)
	}
	cmdName := os.Args[1]
	handler, ok := commands.COMMANDS[cmdName]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmdName)
		os.Exit(1)
	}
	ctx, err := parseContext(cmdName, os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	result := handler(ctx)
	if result.Output != "" {
		fmt.Println(result.Output)
	}
	os.Exit(result.ExitCode)
}

func parseContext(cmd string, args []string) (commands.CommandContext, error) {
	ctx := commands.CommandContext{Args: map[string]any{}}
	switch cmd {
	case "validate":
		fs := flag.NewFlagSet("validate", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		recursive := fs.Bool("recursive", false, "")
		fs.BoolVar(recursive, "r", false, "")
		if err := fs.Parse(args); err != nil {
			return ctx, err
		}
		ctx.Args["recursive"] = *recursive
		ctx.Path = firstArgOrDefault(fs.Args(), ".")
	case "status":
		fs := flag.NewFlagSet("status", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		recursive := fs.Bool("recursive", false, "")
		fs.BoolVar(recursive, "r", false, "")
		if err := fs.Parse(args); err != nil {
			return ctx, err
		}
		ctx.Args["recursive"] = *recursive
		ctx.Path = firstArgOrDefault(fs.Args(), ".")
	case "deps":
		ctx.Path = firstArgOrDefault(args, ".")
	case "init":
		ctx.Path = firstArgOrDefault(args, ".")
	case "log":
		fs := flag.NewFlagSet("log", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		count := fs.Int("count", 10, "")
		fs.IntVar(count, "n", 10, "")
		if err := fs.Parse(args); err != nil {
			return ctx, err
		}
		ctx.Args["count"] = *count
		ctx.Path = firstArgOrDefault(fs.Args(), ".")
	case "diff":
		fs := flag.NewFlagSet("diff", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		if err := fs.Parse(args); err != nil {
			return ctx, err
		}
		pos := fs.Args()
		path, id1, id2 := resolvePathAndIDs(pos)
		ctx.Path = path
		if id1 != "" {
			ctx.Args["id1"] = id1
		}
		if id2 != "" {
			ctx.Args["id2"] = id2
		}
	case "show":
		fs := flag.NewFlagSet("show", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		if err := fs.Parse(args); err != nil {
			return ctx, err
		}
		pos := fs.Args()
		path, id := resolvePathAndID(pos)
		ctx.Path = path
		if id != "" {
			ctx.Args["id"] = id
		}
	case "ss":
		fs := flag.NewFlagSet("ss", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		message := fs.String("message", "", "")
		fs.StringVar(message, "m", "", "")
		skipTests := fs.Bool("skip-tests", false, "")
		if err := fs.Parse(args); err != nil {
			return ctx, err
		}
		ctx.Args["message"] = *message
		ctx.Args["skip_tests"] = *skipTests
		ctx.Path = firstArgOrDefault(fs.Args(), ".")
	default:
		return ctx, errors.New("Unknown command")
	}
	return ctx, nil
}

func firstArgOrDefault(args []string, def string) string {
	if len(args) == 0 {
		return def
	}
	return args[0]
}

func resolvePathAndIDs(args []string) (string, string, string) {
	if len(args) == 0 {
		return ".", "", ""
	}
	if pathExists(args[0]) {
		path := args[0]
		id1 := ""
		id2 := ""
		if len(args) > 1 {
			id1 = args[1]
		}
		if len(args) > 2 {
			id2 = args[2]
		}
		return path, id1, id2
	}
	if len(args) == 1 {
		return ".", args[0], ""
	}
	return ".", args[0], args[1]
}

func resolvePathAndID(args []string) (string, string) {
	if len(args) == 0 {
		return ".", ""
	}
	if pathExists(args[0]) {
		path := args[0]
		id := ""
		if len(args) > 1 {
			id = args[1]
		}
		return path, id
	}
	return ".", args[0]
}

func pathExists(path string) bool {
	if path == "" {
		return false
	}
	if !filepath.IsAbs(path) {
		_, err := os.Stat(path)
		return err == nil
	}
	_, err := os.Stat(path)
	return err == nil
}
