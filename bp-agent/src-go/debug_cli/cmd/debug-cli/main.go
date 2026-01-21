package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/tunapro1234/base-agent/src-go/debug_cli/internal/cli"
	"github.com/tunapro1234/base-agent/src-go/debug_cli/internal/client"
	"github.com/tunapro1234/base-agent/src-go/debug_cli/internal/repl"
)

func main() {
	cfg, err := cli.ParseConfig(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	httpClient := client.NewHTTPClient(cfg.BaseURL, cfg.Token)
	console := repl.New(cfg, httpClient)
	console.Run()
}
