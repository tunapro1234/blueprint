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
	DepUpdates   []DepUpgrade
	Dependents   []bp.DepRef
	Implementing bool
	ImplHidden   bool
	WorkingClean bool
	BlueprintChanged bool
	APIChanged       bool
	SpecChanged      bool
}

type PlanStep struct {
	Path   string
	State  string
	Reason string
}

type PlanResult struct {
	Steps []PlanStep
}

type SnapshotInfo struct {
	ID          string
	Path        string
	ContentHash string
	APIHash     string
	SpecHash    string
	ImplHash    string
}

type ImplementResult struct {
	Ready       bool
	Deps        []bp.DepState
	MissingDeps []string
}

type DepUpgrade struct {
	Path       string
	Current    string
	Latest     string
	APIChanged bool
}

type UpgradeResult struct {
	Upgraded   []string
	APIChanged []string
	Skipped    []string
}
