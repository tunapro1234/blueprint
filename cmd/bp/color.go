package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"blueprint/internal/book"
	"blueprint/internal/config"
)

// color exports the same accent used by the bar for external window managers.
func (a *app) color(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: bp color <agent> [--json|auto|red|orange|yellow|green|cyan|blue|purple|pink|gray|white|0–255]")
	}
	resolved, err := a.resolveNativeTarget(args[0])
	if err != nil {
		return err
	}
	args = append([]string(nil), args...)
	args[0] = resolved
	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return err
	}
	if _, ok := fleet.Agents[args[0]]; !ok {
		return fmt.Errorf("unknown agent: %s", args[0])
	}
	if len(args) == 2 && args[1] != "--json" {
		value := strings.ToLower(args[1])
		if value == "auto" {
			value = ""
		} else {
			var err error
			value, err = config.ColorIndex(value)
			if err != nil {
				return err
			}
		}
		if err := book.SetColorOverride(a.config.Agentbooks, args[0], value); err != nil {
			return err
		}
	}
	index, err := strconv.Atoi(a.barAccent(args[0]))
	if err != nil || index < 0 || index > 255 {
		return fmt.Errorf("invalid accent for %s", args[0])
	}
	hex := xtermColor(index)
	if len(args) == 2 && args[1] == "--json" {
		return json.NewEncoder(a.out).Encode(struct {
			Agent string `json:"agent"`
			Index int    `json:"index"`
			Hex   string `json:"hex"`
		}{args[0], index, hex})
	}
	fmt.Fprintln(a.out, hex)
	return nil
}

// The first 16 terminal colors are palette-dependent; use xterm defaults.
func xtermColor(index int) string {
	if index < 16 {
		return []string{"#000000", "#800000", "#008000", "#808000", "#000080", "#800080", "#008080", "#c0c0c0", "#808080", "#ff0000", "#00ff00", "#ffff00", "#0000ff", "#ff00ff", "#00ffff", "#ffffff"}[index]
	}
	if index >= 232 {
		v := 8 + 10*(index-232)
		return fmt.Sprintf("#%02x%02x%02x", v, v, v)
	}
	levels := []int{0, 95, 135, 175, 215, 255}
	n := index - 16
	return fmt.Sprintf("#%02x%02x%02x", levels[n/36], levels[(n/6)%6], levels[n%6])
}
