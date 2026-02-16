package main

import (
	"fmt"
	"os"

	tui "bp-tui/src/tui/blueprint/history/tui-metrics-bar-9c5f"
)

func main() {
	if err := tui.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
