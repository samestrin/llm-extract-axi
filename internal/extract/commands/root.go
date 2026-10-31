// Package commands holds the llm-extract command tree.
package commands

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/samestrin/llm-extract-axi/pkg/output"
)

// Version is stamped at build time by the Makefile with
// -X github.com/samestrin/llm-extract-axi/internal/extract/commands.Version.
// It lives here rather than on main because the cobra root reads it, so
// stamping main would leave `llm-extract --version` reporting this default.
var Version = "dev"

// GlobalAXIOutput selects TOON output. It reaches every command through
// output.SetDefaultAXI rather than a per-command flag, because each command
// builds its own formatter with output.New(json, min, w).
var GlobalAXIOutput bool

// RootCmd is the base command when called without any subcommands.
var RootCmd = &cobra.Command{
	Use:   "llm-extract",
	Short: "Fetch outside content and reduce it to what an agent needs",
	Long: `llm-extract pulls only what an agent needs from a large file or a web
page, without filling its context. It uses the network or a model only
when it has to, and says so.`,
	// NoArgs, because a root with no Run accepts any argument and prints help
	// with exit 0: a mistyped command would then look like success.
	Args:          cobra.NoArgs,
	SilenceErrors: true,
	SilenceUsage:  true,
	// Published unconditionally, including when the flag is false: assigning
	// only true would leave the previous in-process run's setting in place.
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		output.SetDefaultAXI(GlobalAXIOutput)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	RootCmd.PersistentFlags().BoolVar(&GlobalAXIOutput, "axi", false, "Output as TOON (AXI token-dense format)")
}

// Execute runs the root command against the process arguments and exits.
func Execute() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the command tree once and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	// RootCmd is a package singleton and pflag leaves a flag untouched when it
	// is absent, so a second in-process run would inherit the first one's
	// flags. The shipped binary runs once per process; tests do not.
	resetFlags(RootCmd)
	RootCmd.Version = Version
	RootCmd.SetArgs(args)
	RootCmd.SetOut(stdout)
	RootCmd.SetErr(stderr)
	if err := RootCmd.Execute(); err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

// resetFlags returns every flag on c and its subcommands to its default.
func resetFlags(c *cobra.Command) {
	reset := func(f *pflag.Flag) {
		if !f.Changed {
			return
		}
		// Set on a slice flag appends once the flag has been set, so slices
		// are replaced wholesale instead.
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			_ = sv.Replace(splitDefault(f.DefValue))
		} else {
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	}
	c.Flags().VisitAll(reset)
	c.PersistentFlags().VisitAll(reset)
	for _, sub := range c.Commands() {
		resetFlags(sub)
	}
}

// splitDefault turns a slice flag's DefValue, such as "[a,b]" or "[]", back
// into its elements.
func splitDefault(def string) []string {
	def = strings.TrimSuffix(strings.TrimPrefix(def, "["), "]")
	if def == "" {
		return nil
	}
	return strings.Split(def, ",")
}
