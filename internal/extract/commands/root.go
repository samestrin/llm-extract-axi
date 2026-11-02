// Package commands holds the llm-extract command tree.
package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

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
	// A bare run shows content, not help: where the binary is, what it does,
	// and which commands reach outside. --help still prints help.
	RunE: func(cmd *cobra.Command, args []string) error {
		return printHome(cmd.OutOrStdout())
	},
}

// reach is one row of the home view's network and model table. Keep it in
// step with the README section "Which commands reach outside".
type reach struct{ command, network, model string }

var reachTable = []reach{
	{"fetch", "always (fetches the URL)", "never"},
	{"extract-links", "always (fetches the URL)", "only with --context (LLM link ranking)"},
	{"extract-relevant", "only when --path is a URL", "always (--context is required)"},
	{"setup", "never", "never"},
}

// executable locates the running binary; a variable so tests can stub it.
var executable = os.Executable

// printHome writes the bare-run view. It reads only the process's own path
// and HOME: no network, no model.
func printHome(w io.Writer) error {
	var path string
	if exe, err := executable(); err != nil {
		path = fmt.Sprintf("unknown (%v)", err)
	} else {
		home, _ := os.UserHomeDir()
		path = tildeHome(exe, home)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "llm-extract\t%s\n", path)
	_, _ = fmt.Fprintln(tw, "Fetch outside content and reduce it to what an agent needs.")
	_, _ = fmt.Fprintln(tw)
	_, _ = fmt.Fprintln(tw, "command\tnetwork\tmodel")
	for _, r := range reachTable {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", r.command, r.network, r.model)
	}
	_, _ = fmt.Fprintln(tw)
	_, _ = fmt.Fprintln(tw, "Run 'llm-extract --help' for flags, or 'llm-extract <command> --help'.")
	return tw.Flush()
}

// tildeHome replaces a leading home directory in path with ~. A directory
// that only shares home's prefix, such as /home/sam2 for /home/sam, is left
// alone.
func tildeHome(path, home string) string {
	if home == "" {
		return path
	}
	home = filepath.Clean(home)
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return path
}

func init() {
	RootCmd.PersistentFlags().BoolVar(&GlobalAXIOutput, "axi", false, "Output as TOON (AXI token-dense format)")
}

// Execute runs the root command against the process arguments and exits.
func Execute() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the command tree once and returns the process exit code: 2 for
// a malformed invocation such as an unknown flag or command, 1 for a command
// that ran and failed. output.ExitCodeFor draws that line.
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
		return output.ExitCodeFor(err)
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
