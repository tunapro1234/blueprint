package commands

var COMMANDS = map[string]CommandHandler{
	"validate":  ValidateCommand,
	"status":    StatusCommand,
	"plan":      PlanCommand,
	"cancel":    CancelCommand,
	"deps":      DepsCommand,
	"init":      InitCommand,
	"log":       LogCommand,
	"diff":      DiffCommand,
	"show":      ShowCommand,
	"ss":        SnapshotCommand,
	"implement": ImplementCommand,
	"impl":      ImplementCommand,
	"upgrade":   UpgradeCommand,
}
