// Package commands holds the llm-extract command tree.
package commands

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	cmd, err := RootCmd.ExecuteC()
	if err == nil {
		return 0
	}
	code := output.ExitCodeFor(err)
	if !wantAXI(cmd, args, code) {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return code
	}
	// Under --axi the error is on stdout in TOON, the encoding a success would
	// have used, so a caller reading one stream learns that the command failed
	// and what to do next. A result that already carries its error is not
	// printed twice: two documents would not decode as one.
	var reported reportedError
	if !errors.As(err, &reported) {
		_ = output.EncodeAXI(stdout, axiErrorDoc(cmd, err, code))
	}
	return code
}

// errInvalidURL is the error for a URL that is not http or https.
var errInvalidURL = errors.New("invalid URL: must start with http:// or https://")

// reportedError is an error a command has already printed as part of its
// result. It still sets the exit code; under --axi it is not printed again.
type reportedError struct{ error }

func (e reportedError) Unwrap() error { return e.error }

// wantAXI reports whether this run's error is printed as TOON. A parsed flag
// is the answer when there is one. When parsing stopped at a malformed
// invocation, --axi may sit after the bad flag and never have been read, so
// the arguments are scanned for it instead.
func wantAXI(cmd *cobra.Command, args []string, code int) bool {
	if f := RootCmd.PersistentFlags().Lookup("axi"); f != nil && f.Changed {
		return GlobalAXIOutput
	}
	return code == int(goaxi.ExitUsage) && argsWantAXI(cmd, args)
}

// argsWantAXI scans args for --axi the way pflag would read it: the last
// occurrence wins, a value after = is a bool, nothing after "--" is a flag,
// and the argument after a flag that takes a value is that value, not a flag,
// so `--url --axi` leaves --axi off. cmd is the command the run reached; its
// flags, own and inherited, say which ones take a value.
func argsWantAXI(cmd *cobra.Command, args []string) bool {
	if cmd == nil {
		cmd = RootCmd
	}
	takesValue := func(f *pflag.Flag) bool { return f != nil && f.NoOptDefVal == "" }
	lookup := func(name string) *pflag.Flag {
		if f := cmd.Flags().Lookup(name); f != nil {
			return f
		}
		return cmd.InheritedFlags().Lookup(name)
	}
	shorthand := func(c string) *pflag.Flag {
		if f := cmd.Flags().ShorthandLookup(c); f != nil {
			return f
		}
		return cmd.InheritedFlags().ShorthandLookup(c)
	}
	want := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return want
		case a == "--axi":
			want = true
		case strings.HasPrefix(a, "--axi="):
			b, err := strconv.ParseBool(strings.TrimPrefix(a, "--axi="))
			want = err == nil && b
		case strings.HasPrefix(a, "--"):
			if !strings.Contains(a, "=") && takesValue(lookup(a[2:])) {
				i++
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			// A shorthand group such as -abc: the first letter that takes a
			// value takes the rest of the group, or the next argument when it
			// is the last letter.
			group := a[1:]
			for j := 0; j < len(group); j++ {
				f := shorthand(group[j : j+1])
				if f == nil {
					break
				}
				if takesValue(f) {
					if j == len(group)-1 {
						i++
					}
					break
				}
			}
		}
	}
	return want
}

// axiErrorDoc builds the TOON error for err: the {error, message} shape of
// Formatter.PrintError, plus what a caller needs to correct itself. A
// malformed invocation lists what cmd accepts; every error carries help[].
func axiErrorDoc(cmd *cobra.Command, err error, code int) map[string]any {
	doc := map[string]any{"error": true, "message": err.Error()}
	usage := "Run '" + cmd.CommandPath() + " --help' for usage"
	var help []string
	switch {
	case strings.Contains(err.Error(), "unknown command "):
		doc["commands"] = commandNames(cmd)
		help = []string{"Run '" + cmd.CommandPath() + " --help' to see the commands"}
	case code == int(goaxi.ExitUsage):
		doc["flags"] = flagNames(cmd)
		help = []string{usage}
	case errors.Is(err, llmapi.ErrAPIKeyRequired):
		help = []string{"Set OPENAI_API_KEY or ANTHROPIC_API_KEY, then run the command again", usage}
	case errors.Is(err, errInvalidURL):
		help = []string{"Pass a URL that starts with http:// or https://", usage}
	default:
		help = []string{usage}
	}
	doc["help"] = help
	return doc
}

// commandNames lists the commands a caller can run under cmd.
func commandNames(cmd *cobra.Command) []string {
	var names []string
	for _, c := range cmd.Commands() {
		if c.IsAvailableCommand() {
			names = append(names, c.Name())
		}
	}
	return names
}

// flagNames lists every flag cmd accepts, its own and those it inherits, as
// --name, sorted.
func flagNames(cmd *cobra.Command) []string {
	seen := map[string]bool{}
	var names []string
	add := func(f *pflag.Flag) {
		if f.Hidden || seen[f.Name] {
			return
		}
		seen[f.Name] = true
		names = append(names, "--"+f.Name)
	}
	cmd.LocalFlags().VisitAll(add)
	cmd.InheritedFlags().VisitAll(add)
	sort.Strings(names)
	return names
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
