// Package commands holds the llm-extract command tree.
package commands

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	goaxi "github.com/samestrin/go-axi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/samestrin/llm-extract-axi/internal/support/fetch"
	"github.com/samestrin/llm-extract-axi/pkg/llmapi"
	"github.com/samestrin/llm-extract-axi/pkg/output"
)

// Version is stamped at build time by the Makefile with
// -X github.com/samestrin/llm-extract-axi/internal/extract/commands.Version.
// It lives here rather than on main because the cobra root reads it, so
// stamping main would leave `llm-extract --version` reporting this default.
var Version = "dev"

// BareVersion returns v without a leading "v", so a `git describe` stamp such
// as v1.0.1 prints as 1.0.1.
func BareVersion(v string) string { return strings.TrimPrefix(v, "v") }

// IsVersionFlag reports whether arg is one of the version flags, -v, -V or
// --version.
func IsVersionFlag(arg string) bool {
	return arg == "-v" || arg == "-V" || arg == "--version"
}

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

// homeAbout is the home view's one-sentence description.
const homeAbout = "Fetch outside content and reduce it to what an agent needs."

// reachRow is one row of the home view's reach table under --axi.
type reachRow struct {
	Command string `json:"command"`
	Network string `json:"network"`
	Model   string `json:"model"`
}

// homeView is the bare-run payload under --axi: identity, the reach table and
// the configuration a run would use. The API key is reported as set or not and
// never carried, and the base URL has passed through redactURL.
type homeView struct {
	Bin           string     `json:"bin"`
	About         string     `json:"about"`
	Reach         []reachRow `json:"reach"`
	APIKeySet     bool       `json:"api_key_set"`
	Model         string     `json:"model"`
	BaseURL       string     `json:"base_url"`
	FetchMaxBytes int        `json:"fetch_max_bytes"`
}

// homeHelp is the help[] block of the home view under --axi.
var homeHelp = []string{
	"Fetch a page: llm-extract fetch <url>",
	"List a page's links: llm-extract extract-links --url <url>",
	"Pull what matters from a file or URL: llm-extract extract-relevant --path <path> --context <text>",
	"Flags for any command: llm-extract <command> --help",
}

// buildHomeView reads the process's own path, HOME and environment: no
// network, no model.
func buildHomeView() homeView {
	view := homeView{About: homeAbout}
	if exe, err := executable(); err != nil {
		view.Bin = fmt.Sprintf("unknown (%v)", err)
	} else {
		home, _ := os.UserHomeDir()
		view.Bin = tildeHome(exe, home)
	}
	for _, r := range reachTable {
		view.Reach = append(view.Reach, reachRow{r.command, r.network, r.model})
	}
	cfg := llmapi.GetAPIConfig()
	view.APIKeySet = cfg.APIKey != ""
	view.Model = cfg.Model
	view.BaseURL = redactURL(cfg.BaseURL)
	view.FetchMaxBytes = fetch.LoadConfig().MaxBodyBytes
	return view
}

// printHome writes the bare-run view: TOON with a help[] block under --axi,
// a table otherwise.
func printHome(w io.Writer) error {
	view := buildHomeView()
	if GlobalAXIOutput {
		// Assembled first and written once, so a failed help block never
		// leaves a payload on stdout that decodes without it.
		var doc bytes.Buffer
		if err := output.EncodeAXI(&doc, view); err != nil {
			return err
		}
		if err := goaxi.WriteHelp(&doc, homeHelp); err != nil {
			return err
		}
		_, err := w.Write(doc.Bytes())
		return err
	}
	keyState := "not set (extract-relevant and extract-links --context need one)"
	if view.APIKeySet {
		keyState = "set"
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "llm-extract\t%s\n", view.Bin)
	_, _ = fmt.Fprintln(tw, view.About)
	_, _ = fmt.Fprintln(tw)
	_, _ = fmt.Fprintln(tw, "command\tnetwork\tmodel")
	for _, r := range view.Reach {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Command, r.Network, r.Model)
	}
	_, _ = fmt.Fprintln(tw)
	_, _ = fmt.Fprintf(tw, "api key\t%s\n", keyState)
	_, _ = fmt.Fprintf(tw, "model\t%s\n", view.Model)
	_, _ = fmt.Fprintf(tw, "base url\t%s\n", view.BaseURL)
	_, _ = fmt.Fprintf(tw, "fetch max bytes\t%d\n", view.FetchMaxBytes)
	_, _ = fmt.Fprintln(tw)
	_, _ = fmt.Fprintln(tw, "Run 'llm-extract --help' for flags, or 'llm-extract <command> --help'.")
	return tw.Flush()
}

// redactURL returns the scheme, host and path of raw, dropping userinfo,
// query and fragment, where a password or token may sit. A value that does
// not parse as an absolute URL is not echoed, since the secret is still in it.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "(not a valid URL)"
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
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
	// A lone version flag is answered here, because cobra's own flag knows
	// only --version and would reject -V.
	if len(args) == 1 && IsVersionFlag(args[0]) {
		_, _ = fmt.Fprintln(stdout, BareVersion(Version))
		return 0
	}
	RootCmd.Version = BareVersion(Version)
	RootCmd.SetVersionTemplate("{{.Version}}\n")
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
