package release

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The release is built by scripts/release-build.sh, which release.yml calls,
// so the build a tag produces can be run here rather than first seen when a
// release is cut. The workflow itself is checked as text: there is no YAML
// parser in go.mod, so readReleaseWorkflow splits it by indentation and fails
// loudly if that layout stops matching.

const (
	releaseWorkflowPath = "../../.github/workflows/release.yml"
	releaseBuildScript  = "../../scripts/release-build.sh"
)

type releaseWorkflow struct {
	sections map[string]string // top-level key -> its block
	jobs     map[string]string // job name -> its block
}

func readReleaseWorkflow(t *testing.T) releaseWorkflow {
	t.Helper()
	body, err := os.ReadFile(releaseWorkflowPath)
	if err != nil {
		t.Fatalf("reading release.yml: %v", err)
	}
	w := releaseWorkflow{sections: map[string]string{}, jobs: map[string]string{}}
	var section, job string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			section = strings.TrimSuffix(strings.Fields(line)[0], ":")
			job = ""
		}
		w.sections[section] += line + "\n"
		if section == "jobs" && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") {
			job = strings.TrimSuffix(strings.TrimSpace(line), ":")
		}
		if job != "" {
			w.jobs[job] += line + "\n"
		}
	}
	for _, s := range []string{"on", "permissions", "jobs"} {
		if w.sections[s] == "" {
			t.Fatalf("release.yml has no top-level %q section; the split is wrong and these tests would pass vacuously", s)
		}
	}
	if len(w.jobs) == 0 {
		t.Fatal("release.yml has no jobs; the split is wrong and these tests would pass vacuously")
	}
	return w
}

// A pushed v* tag is what cuts a release. A manual run, and a pull request
// that changes the workflow, prove the build without publishing.
func TestReleaseWorkflowTriggers(t *testing.T) {
	w := readReleaseWorkflow(t)
	for _, want := range []string{"tags: ['v*']", "workflow_dispatch", "paths: ['.github/workflows/release.yml', 'scripts/release-build.sh']"} {
		if !strings.Contains(w.sections["on"], want) {
			t.Errorf("release.yml triggers do not include %q:\n%s", want, w.sections["on"])
		}
	}
}

// Exactly one job may publish, it is the only one with write access, it runs
// only on a pushed v* tag, and it publishes everything the build produced,
// SHA256SUMS.txt included. Gating on the ref alone is not enough: a manual run
// started from a tag also has a refs/tags/v ref, and must publish nothing.
func TestReleaseWorkflowPublishesOnlyOnATagPush(t *testing.T) {
	w := readReleaseWorkflow(t)
	if strings.Contains(w.sections["permissions"], "write") {
		t.Errorf("release.yml grants write at the top level, so every job could publish:\n%s", w.sections["permissions"])
	}
	var publishers []string
	for name, block := range w.jobs {
		publishes := strings.Contains(block, "action-gh-release")
		if publishes {
			publishers = append(publishers, name)
		}
		if strings.Contains(block, "contents: write") && !publishes {
			t.Errorf("job %q has contents: write but does not publish; only the publish job needs it", name)
		}
	}
	if len(publishers) != 1 {
		t.Fatalf("want exactly one publishing job, got %v", publishers)
	}
	pub := w.jobs[publishers[0]]
	for _, want := range []string{
		"if: github.event_name == 'push' && startsWith(github.ref, 'refs/tags/v')",
		"contents: write",
		"needs: build",
		"files: dist/*",
		"fail_on_unmatched_files: true",
	} {
		if !strings.Contains(pub, want) {
			t.Errorf("publish job is missing %q:\n%s", want, pub)
		}
	}
}

// The build job is what a manual run proves, so it runs on every trigger and
// does everything a release does short of publishing: build the binaries the
// end-to-end tests need, run the suite, then build the release from the tag.
func TestReleaseWorkflowBuildJob(t *testing.T) {
	w := readReleaseWorkflow(t)
	build := w.jobs["build"]
	if build == "" {
		t.Fatal("release.yml has no build job")
	}
	if strings.Contains(build, "if:") {
		t.Errorf("the build job is conditional; it must run on every trigger:\n%s", build)
	}
	// Order matters: tests/e2e fails without ./build, and a release must not
	// be built from a commit whose tests fail.
	steps := []string{"make build", "go test -race ./...", "scripts/release-build.sh \"$VERSION\" dist", "actions/upload-artifact"}
	last := -1
	for _, s := range steps {
		i := strings.Index(build, s)
		if i < 0 {
			t.Errorf("build job is missing %q", s)
			continue
		}
		if i < last {
			t.Errorf("build job runs %q out of order; want %v", s, steps)
		}
		last = i
	}
	// A tag stamps its own name; anything else must not claim to be a release.
	for _, want := range []string{`VERSION="$GITHUB_REF_NAME"`, `VERSION="dev-${GITHUB_SHA::12}"`, `"$GITHUB_REF_TYPE" = "tag"`} {
		if !strings.Contains(build, want) {
			t.Errorf("build job does not set the version with %q", want)
		}
	}
}

// release-build.sh must name all five platforms, so a platform cannot quietly
// drop out of a release while the host-only run below still passes.
func TestReleaseBuildNamesFivePlatforms(t *testing.T) {
	body, err := os.ReadFile(releaseBuildScript)
	if err != nil {
		t.Fatal(err)
	}
	const want = `PLATFORMS="${RELEASE_PLATFORMS:-darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64}"`
	if !strings.Contains(string(body), want) {
		t.Errorf("release-build.sh does not default to the five platforms; want the line\n%s", want)
	}
}

// runReleaseBuild runs the release build for the given platforms into a temp
// directory and returns the directory, the output and whether it succeeded.
func runReleaseBuild(t *testing.T, version, platforms string) (string, string, bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("release-build.sh runs on the Linux release runner")
	}
	out := filepath.Join(t.TempDir(), "dist")
	script, err := filepath.Abs(releaseBuildScript)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, version, out)
	cmd.Env = append(os.Environ(), "RELEASE_PLATFORMS="+platforms)
	b, err := cmd.CombinedOutput()
	return out, string(b), err == nil
}

// The real build, for the host and one cross target: each binary carries the
// tag as its version (commands.Version for the CLI, not main, or it would say
// "dev"), SHA256SUMS.txt has exactly one line per binary, and every line
// verifies.
func TestReleaseBuildStampsVersionAndWritesChecksums(t *testing.T) {
	host := runtime.GOOS + "/" + runtime.GOARCH
	cross := "windows/amd64"
	dist, out, ok := runReleaseBuild(t, "v9.8.7", host+" "+cross)
	if !ok {
		t.Fatalf("release-build.sh failed:\n%s", out)
	}

	suffix := "-" + runtime.GOOS + "-" + runtime.GOARCH
	got, err := exec.Command(filepath.Join(dist, "llm-extract"+suffix), "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("built llm-extract --version: %v\n%s", err, got)
	}
	if strings.TrimSpace(string(got)) != "llm-extract version v9.8.7" {
		t.Errorf("built CLI reports %q, want the tag v9.8.7", strings.TrimSpace(string(got)))
	}
	// The server has no --version; the stamped string is in the binary.
	mcp, err := os.ReadFile(filepath.Join(dist, "llm-extract-mcp"+suffix))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mcp), "v9.8.7") {
		t.Error("built llm-extract-mcp does not carry the tag as its version")
	}

	want := map[string]bool{}
	for _, b := range binaries {
		want[b+suffix] = true
		want[b+"-windows-amd64.exe"] = true
	}
	entries, err := os.ReadDir(dist)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "SHA256SUMS.txt" && !want[e.Name()] {
			t.Errorf("unexpected file in dist: %s", e.Name())
		}
	}
	if len(entries) != len(want)+1 {
		t.Errorf("dist holds %d files, want %d binaries and SHA256SUMS.txt", len(entries), len(want))
	}

	f, err := os.Open(filepath.Join(dist, "SHA256SUMS.txt"))
	if err != nil {
		t.Fatalf("no SHA256SUMS.txt: %v", err)
	}
	defer func() { _ = f.Close() }()
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			t.Errorf("malformed checksum line %q", sc.Text())
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if seen[name] || !want[name] {
			t.Errorf("checksum line for %q is duplicated or unexpected", name)
		}
		seen[name] = true
		body, err := os.ReadFile(filepath.Join(dist, name))
		if err != nil {
			t.Fatal(err)
		}
		if sha256Hex(string(body)) != fields[0] {
			t.Errorf("checksum for %s does not match the file", name)
		}
	}
	if len(seen) != len(want) {
		t.Errorf("SHA256SUMS.txt covers %d binaries, want %d", len(seen), len(want))
	}
}

// A release with no version, or a platform that does not build, must fail
// rather than publish a "dev" binary or a short release.
func TestReleaseBuildRefuses(t *testing.T) {
	if _, out, ok := runReleaseBuild(t, "", runtime.GOOS+"/"+runtime.GOARCH); ok {
		t.Errorf("release-build.sh succeeded with no version:\n%s", out)
	}
	if _, out, ok := runReleaseBuild(t, "dev", runtime.GOOS+"/"+runtime.GOARCH); ok {
		t.Errorf("release-build.sh succeeded stamping \"dev\", the unstamped default:\n%s", out)
	}
	if _, out, ok := runReleaseBuild(t, "v1.0.0", "plan9/nosuch"); ok {
		t.Errorf("release-build.sh succeeded on a platform Go cannot build:\n%s", out)
	}
}
