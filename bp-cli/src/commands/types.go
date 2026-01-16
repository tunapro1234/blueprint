package commands

import bp "blueprint"

type CommandHandler func(ctx CommandContext) CommandResult

type CommandContext struct {
	Path      string
	Blueprint *bp.Blueprint
	Tree      *bp.BlueprintTree
	Args      map[string]any
}

type CommandResult struct {
	ExitCode int
	Output   string
	Data     any
	Errors   []string
}

type DiffResult struct {
	HasChanges  bool
	LeftID      string
	RightID     string
	UnifiedDiff string
}

type StatusInfo struct {
	State        string
	ChangedFiles []string
	ChangedDeps  []string
	StaleReason  string
}

type SnapshotInfo struct {
	ID       string
	Path     string
	ImplHash string
	DepsHash string
}

type WorkSession struct {
	ID       string
	Created  string
	Status   string
	Packages []WorkPackage
}

type WorkPackage struct {
	Path          string
	WorkPath      string
	Reason        string
	BlueprintHash string
}
