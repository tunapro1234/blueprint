package main

import (
	"fmt"

	"blueprint/internal/book"
)

// reparent changes where an existing agent sits in the tree (#23).
func (a *app) reparent(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: bp reparent <agent> <new-parent>")
	}
	previous, err := book.SetParent(a.config.Agentbooks, args[0], args[1])
	if err != nil {
		return err
	}
	if previous == args[1] {
		fmt.Fprintf(a.out, "%s: parent already %s, no change\n", args[0], args[1])
		return nil
	}
	fmt.Fprintf(a.out, "%s: parent %s -> %s\n", args[0], previous, args[1])
	return nil
}
