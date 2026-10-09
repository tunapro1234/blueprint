// Command tmux exports Go reference results for the Rust bp-tmux crate into
// testdata/golden/tmux/*.json. Run from the repository root:
//
//	go run ./tools/goldenexport/tmux [-out testdata/golden/tmux]
//	go run ./tools/goldenexport/tmux -tables crates/bp-tmux/src/screen/unicode_tables.rs
//
// Only exported functions of internal/tmux are called. Every corpus is
// deterministic, so regenerating without a Go behavior change produces
// identical files.
package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
)

func main() {
	out := flag.String("out", "testdata/golden/tmux", "output directory")
	tables := flag.String("tables", "", "write the Rust Mn/Me table to this path instead")
	flag.Parse()
	if *tables != "" {
		writeTables(*tables)
		return
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		panic(err)
	}
	writeGoldens(*out)
}

func write(dir, name string, value any) {
	data, err := json.MarshalIndent(value, "", " ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), append(data, '\n'), 0o644); err != nil {
		panic(err)
	}
}
