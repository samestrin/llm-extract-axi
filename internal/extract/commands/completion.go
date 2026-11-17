package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/samestrin/llm-extract-axi/pkg/output"
)

// initCompletionCmd replaces cobra's default completion command with a fresh
// one whose bare form is a usage error. Cobra's parent has no Run, so a bare
// `completion` or an unknown shell such as `completion bogus` printed help and
// exited 0: a mistyped shell looked like success.
//
// It is rebuilt on every run because cobra captures the root's writer when it
// builds the command, so a second in-process run would print the script to the
// first run's stdout.
func initCompletionCmd(root *cobra.Command) {
	for _, c := range root.Commands() {
		if c.Name() == "completion" {
			root.RemoveCommand(c)
		}
	}
	root.InitDefaultCompletionCmd()
	for _, c := range root.Commands() {
		if c.Name() == "completion" {
			c.Args = cobra.ArbitraryArgs
			c.RunE = runCompletionWithoutShell
		}
	}
}

// runCompletionWithoutShell is reached only when no shell subcommand matched.
// An unknown shell is worded as cobra words an unknown command, so the error
// document lists the shells under commands[].
func runCompletionWithoutShell(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return output.UsageError(errors.New("completion needs a shell: " + strings.Join(commandNames(cmd), ", ")))
	}
	return output.UsageError(fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath()))
}
