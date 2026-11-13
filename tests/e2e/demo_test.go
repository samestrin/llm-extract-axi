package e2e

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The demo is recorded by vhs, which is not installed on the build hosts (it
// needs ttyd and ffmpeg as well). These tests defend the tape from the outside:
// they read docs/demo/llm-extract.tape, replay every command in it against
// ./build/llm-extract, and check the output and the timings. vhs never runs.
const (
	demoTape = "../../docs/demo/llm-extract.tape"
	demoGIF  = "docs/demo/llm-extract.gif"
	// demoMaxDuration is the session length the release promises (AC2).
	demoMaxDuration = 30 * time.Second
	// demoStartupAllowance covers what the tape spends outside its own Sleep
	// lines: start-up, the hidden `go build` of the fixture server, and the
	// three commands themselves.
	demoStartupAllowance = 3 * time.Second
	// demoReplayLimit is a backstop so a broken tape cannot hang the suite.
	demoReplayLimit = 90 * time.Second
)

// vhs defaults, used when the tape does not set them.
const (
	vhsDefaultTyping   = 50 * time.Millisecond
	vhsDefaultPlayback = 1.0
)

var (
	tapeTypeLine  = regexp.MustCompile(`^Type "(.*)" Enter$`)
	tapeSleepLine = regexp.MustCompile(`^Sleep (\S+)$`)
	tapeSetLine   = regexp.MustCompile(`^Set (\S+) (\S+)$`)
	// A version the binary prints from -X, e.g. 1.0.1-19-g68a20d6. It does not
	// start a line when the replay's `clear` writes its terminal escapes first.
	versionLine = regexp.MustCompile(`(?:^|[^0-9.])[0-9]+\.[0-9]+\.[0-9]+`)
)

// tapeCommand is one Type line and whether it sits in a Hide section.
type tapeCommand struct {
	text   string
	hidden bool
}

// tapeSession is everything the checks need to know about the tape.
type tapeSession struct {
	output   string
	commands []tapeCommand
	// typed is the number of characters typed by Type lines, each Enter included.
	typed    int
	sleeps   time.Duration
	typing   time.Duration
	playback float64
}

// parseDemoTape reads the tape. It fails on a Type line it cannot replay
// rather than skipping it: a check that silently ignores a command would pass
// while the demo shows a command that no longer works, and it also keeps the
// tape's one-line `Type "..." Enter` style from drifting away from this reader.
func parseDemoTape(t *testing.T) tapeSession {
	t.Helper()
	raw, err := os.ReadFile(demoTape)
	if err != nil {
		t.Fatalf("cannot read the demo tape: %v", err)
	}
	s := tapeSession{typing: vhsDefaultTyping, playback: vhsDefaultPlayback}
	hidden := false
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "Output "):
			s.output = strings.TrimSpace(strings.TrimPrefix(line, "Output "))
		case line == "Hide":
			hidden = true
		case line == "Show":
			hidden = false
		}
		if m := tapeSetLine.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "TypingSpeed":
				d, err := time.ParseDuration(m[2])
				if err != nil {
					t.Fatalf("%s:%d: bad TypingSpeed %q: %v", demoTape, i+1, m[2], err)
				}
				s.typing = d
			case "PlaybackSpeed":
				f, err := strconv.ParseFloat(m[2], 64)
				if err != nil || f <= 0 {
					t.Fatalf("%s:%d: bad PlaybackSpeed %q", demoTape, i+1, m[2])
				}
				s.playback = f
			}
			continue
		}
		if m := tapeSleepLine.FindStringSubmatch(line); m != nil {
			d, err := time.ParseDuration(m[1])
			if err != nil {
				t.Fatalf("%s:%d: bad Sleep %q: %v", demoTape, i+1, m[1], err)
			}
			s.sleeps += d
			continue
		}
		if strings.HasPrefix(line, "Type ") {
			m := tapeTypeLine.FindStringSubmatch(line)
			if m == nil {
				t.Fatalf("%s:%d: this check can only replay a one-line `Type \"...\" Enter` with no double quote in the text: %s", demoTape, i+1, line)
			}
			s.commands = append(s.commands, tapeCommand{text: m[1], hidden: hidden})
			s.typed += len(m[1]) + 1 // the Enter key is typed too
		}
	}
	if len(s.commands) == 0 {
		t.Fatalf("%s holds no command to replay", demoTape)
	}
	return s
}

// visible returns the commands the recording shows, in order.
func (s tapeSession) visible() []string {
	var out []string
	for _, c := range s.commands {
		if !c.hidden {
			out = append(out, c.text)
		}
	}
	return out
}

// estimatedDuration is the session length worked out from the tape itself:
// vhs types each character at TypingSpeed, sleeps for each Sleep, and divides
// the whole session by PlaybackSpeed. It is an estimate, not a measurement;
// demoStartupAllowance is deliberately generous so a tape that only just fits
// fails here instead of in the operator's render.
func (s tapeSession) estimatedDuration() time.Duration {
	typed := time.Duration(float64(s.typed) * float64(s.typing) / s.playback)
	slept := time.Duration(float64(s.sleeps) / s.playback)
	return typed + slept + demoStartupAllowance
}

// Defends the demo the release promises: one GIF at the committed path, with
// no hosted recording link, showing exactly the commands the epic names. The
// path check is what keeps `## Demo` in the README pointing at a file in this
// repository instead of at vhs.charm.sh.
func TestDemoTapeRecordsTheCommittedGIF(t *testing.T) {
	s := parseDemoTape(t)
	if s.output != demoGIF {
		t.Errorf("the tape writes %q, want %q: the GIF is a file in the repository", s.output, demoGIF)
	}

	visible := s.visible()
	if len(visible) != 3 {
		t.Fatalf("the demo shows %d commands, want 3 (--version, extract-links --axi, extract-links --fields):\n%s", len(visible), strings.Join(visible, "\n"))
	}
	for i, want := range []string{
		"llm-extract --version",
		"llm-extract extract-links --axi",
		"llm-extract extract-links --fields href,score",
	} {
		if !strings.HasPrefix(visible[i], want) {
			t.Errorf("shown command %d is %q, want it to start with %q", i+1, visible[i], want)
		}
		if !strings.Contains(visible[i], "http://127.0.0.1:") && want != "llm-extract --version" {
			t.Errorf("shown command %d does not name the loopback fixture: %q", i+1, visible[i])
		}
	}

	for _, name := range []string{"README.md", "llm-extract.tape"} {
		body, err := os.ReadFile("../../docs/demo/" + name)
		if err != nil {
			t.Fatalf("cannot read docs/demo/%s: %v", name, err)
		}
		if strings.Contains(string(body), "vhs.charm.sh") {
			t.Errorf("docs/demo/%s links a hosted recording; the GIF is committed in the repository", name)
		}
	}
}

// Defends AC2's "under 30 seconds". vhs cannot run here, so the tape's own
// timings are summed: a new Sleep, a slow TypingSpeed or a fourth command that
// pushes the session over the limit fails here before the operator renders it.
func TestDemoTapeIsUnderThirtySeconds(t *testing.T) {
	s := parseDemoTape(t)
	got := s.estimatedDuration()
	t.Logf("estimated session: %s (typing %s, sleeps %s, playback %g, %d characters)",
		got, s.typedCommandTime(), s.sleeps, s.playback, s.typed)
	if got > demoMaxDuration {
		t.Errorf("estimated session is %s, want under %s", got, demoMaxDuration)
	}
}

// typedCommandTime is the time spent typing the Type lines, for the log.
func (s tapeSession) typedCommandTime() time.Duration {
	return time.Duration(float64(s.typed) * float64(s.typing) / s.playback)
}

// Defends every command in the tape, the hidden setup included: the tape's own
// Type lines are replayed in one bash session against ./build/llm-extract, so a
// command that stops working (a renamed flag, a fixture that moved, a server
// that does not start) fails the suite instead of the operator's recording.
// The assertions are on the payload, not just the exit status: a pipeline that
// fails still exits 0 through head, so the output is what proves it ran.
func TestDemoTapeCommandsRunAgainstTheBinary(t *testing.T) {
	binary(t, "llm-extract") // fails, not skips, when ./build is missing
	s := parseDemoTape(t)
	if _, err := exec.LookPath("bash"); err != nil {
		t.Fatalf("the demo is a bash session and this check replays it; bash is not on PATH: %v", err)
	}

	var script strings.Builder
	script.WriteString("set -eu\n")
	// Name the command that broke, and take the hidden fixture server down
	// whether the session succeeds or fails, so the port is free afterwards.
	script.WriteString(`trap 'echo "TAPE COMMAND FAILED: $BASH_COMMAND" >&2' ERR` + "\n")
	script.WriteString(`trap 'if [ -n "${SERVER:-}" ]; then kill "$SERVER" 2>/dev/null || true; fi' EXIT` + "\n")
	for _, c := range s.commands {
		script.WriteString(c.text + "\n")
	}

	ctx, cancel := context.WithTimeout(context.Background(), demoReplayLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", script.String())
	// vhs runs the tape in a terminal and sets TERM; `clear` needs it here too.
	cmd.Env = append(os.Environ(), "TERM=xterm")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("replaying the tape took longer than %s:\n%s", demoReplayLimit, out)
	}
	if err != nil {
		t.Fatalf("replaying the tape failed (%v):\n%s", err, out)
	}

	for _, want := range []string{
		"on http://127.0.0.1:8765/docs-page.html", // the hidden fixture server really bound
		"links[50]:",            // the --axi document on the fixture
		"Total Links: 50",       // the plain --fields run
		"docs/ops/requirements", // a link the fixture really has
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("replayed output does not contain %q:\n%s", want, out)
		}
	}
	if !versionLine.Match(out) {
		t.Errorf("replayed output holds no version line:\n%s", out)
	}
}
