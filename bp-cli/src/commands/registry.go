package commands

var COMMANDS = map[string]CommandHandler{
	"validate":  ValidateCommand,
	"status":    StatusCommand,
	"deps":      DepsCommand,
	"init":      InitCommand,
	"log":       LogCommand,
	"diff":      DiffCommand,
	"show":      ShowCommand,
	"ss":        SnapshotCommand,
	"implement": ImplementCommand,
	"upgrade":   UpgradeCommand,
}
