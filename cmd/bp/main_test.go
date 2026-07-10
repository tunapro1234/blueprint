package main

import (
	"reflect"
	"testing"

	"blueprint/internal/book"
)

func TestAnnouncementTargetsFollowHierarchy(t *testing.T) {
	fleet := book.Fleet{
		Root:  "server-main",
		Order: []string{"server-main", "alpha", "alpha-child", "alpha-grandchild", "beta", "orphan", "lab-scratch", "closed-agent"},
		Agents: map[string]book.Agent{
			"server-main":      {Name: "server-main"},
			"alpha":            {Name: "alpha"},
			"alpha-child":      {Name: "alpha-child"},
			"alpha-grandchild": {Name: "alpha-grandchild"},
			"beta":             {Name: "beta"},
			"orphan":           {Name: "orphan"},
			"lab-scratch":      {Name: "lab-scratch"},
			"closed-agent":     {Name: "closed-agent"},
		},
		Parents: map[string]string{
			"server-main":      "",
			"alpha":            "server-main",
			"alpha-child":      "alpha",
			"alpha-grandchild": "alpha-child",
			"beta":             "server-main",
			"orphan":           "",
			"lab-scratch":      "alpha",
			"closed-agent":     "alpha",
		},
	}
	states := map[string]book.State{}
	for _, name := range fleet.Order {
		states[name] = book.State{Alive: true}
	}
	delete(states, "closed-agent")

	if got, want := announcementTargets(fleet, states, "alpha"), []string{"alpha-child", "alpha-grandchild"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("alpha targets=%v, want %v", got, want)
	}
	if got, want := announcementTargets(fleet, states, "server-main"), []string{"alpha", "alpha-child", "alpha-grandchild", "beta", "orphan"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root targets=%v, want %v", got, want)
	}
}

func TestTranslatePolicyOutput(t *testing.T) {
	input := "usage: 7g %42, fable %10, 5s %8, reset 12.5s, E %25, fresh (1s)\noverride kaldirildi\n"
	want := "usage: 7d %42, fable %10, 5h %8, reset 12.5h, E %25, fresh (1s)\noverride cleared\n"
	if got := translatePolicyOutput(input); got != want {
		t.Fatalf("translated output=%q, want %q", got, want)
	}
}
