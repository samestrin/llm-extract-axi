package commands

import (
	"strings"
	"testing"
)

// Defends AXI principle 10 (EST-8204 item 3): every command's --help carries
// an Examples section with at least two runnable lines, as extract-links and
// extract-relevant already did. fetch and setup had none, so an agent reading
// their help had to assemble an invocation from the flag list alone.
func TestHelpHasExamples(t *testing.T) {
	for _, name := range []string{"fetch", "setup", "extract-links", "extract-relevant"} {
		t.Run(name, func(t *testing.T) {
			code, stdout, _ := runCLI(t, name, "--help")
			if code != 0 {
				t.Fatalf("exit %d", code)
			}
			_, ex, ok := strings.Cut(stdout, "Examples:\n")
			if !ok {
				t.Fatalf("%s --help has no Examples section:\n%s", name, stdout)
			}
			n := 0
			for _, line := range strings.Split(ex, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "llm-extract "+name) {
					n++
				}
			}
			if n < 2 {
				t.Errorf("%s --help has %d example invocations, want 2 or more:\n%s", name, n, ex)
			}
		})
	}
}

// Defends the skill's account of failure (EST-8204 items 4-6). A failed run
// can still carry results beside error (extract-links --context with no key
// prints the heuristic links and exits 2), so "never an empty result" alone
// misleads; a non-text body comes back empty, and the only way to get it is
// --output; and the skill is published alone, so it must say how to get the
// binary it calls. TestCommittedSkillMatchesSetupSkill keeps SKILL.md in step.
func TestSetupSkillCoversFailuresBinaryBodiesAndInstall(t *testing.T) {
	for _, want := range []string{
		"may still carry results beside",
		"Exit 2",
		"not UTF-8 text",
		"--output <file>",
		"not on PATH",
		"install.sh",
	} {
		if !strings.Contains(setupSkill, want) {
			t.Errorf("setupSkill does not mention %q", want)
		}
	}
}
