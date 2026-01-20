package commands

var COMMANDS = map[string]CommandHandler{
	"validate":  ValidateCommand,
	"status":    StatusCommand,
	"plan":      PlanCommand,
	"cancel":    CancelCommand,
	"ss":        SsCommand,
	"new-lang":  NewLangCommand,
	"deps":      DepsCommand,
	"map":       MapCommand,
	"init":      InitCommand,
	"log":       LogCommand,
	"diff":      DiffCommand,
	"show":      ShowCommand,
	"implement": ImplementCommand,
	"impl":      ImplementCommand,
	"upgrade":   UpgradeCommand,
}
