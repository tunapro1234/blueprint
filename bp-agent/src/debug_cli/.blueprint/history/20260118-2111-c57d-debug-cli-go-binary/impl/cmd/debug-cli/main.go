package main

import (
	"os"

	"debug_cli/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
