// Package release tests what a release ships: install.sh and the release
// workflow that builds the assets it downloads.
//
// The install tests run the real install.sh against a local server standing
// in for a GitHub release, because the property that matters is a side
// effect: what ends up in $HOME/.local/bin. An installer that prints
// "installed" over a binary that failed its checksum is the exact failure this
// repository exists to prevent, so every refusal case asserts the install
// directory, not just the exit code.
package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const installScript = "../../install.sh"

// The two binaries a release ships, in the order install.sh installs them.
// The CLI comes first: the MCP server runs it, so a server installed without
// it would be a server that fails every call.
var binaries = []string{"llm-extract", "llm-extract-mcp"}

// fakeBinary is a stand-in release binary, distinct per name so a test can
// tell which asset landed where.
func fakeBinary(name string) string {
	return "#!/bin/sh\necho '" + name + " fake'\n"
}

// asset is the asset name release.yml builds for this platform.
func asset(name string) string {
	return fmt.Sprintf("%s-%s-%s", name, runtime.GOOS, runtime.GOARCH)
}

func sha256Hex(b string) string {
	sum := sha256.Sum256([]byte(b))
	return hex.EncodeToString(sum[:])
}

// goodRelease is a complete, valid release for this platform, with a line for
// another platform in SHA256SUMS.txt so the script must pick its own.
func goodRelease() map[string]string {
	files := map[string]string{}
	var sums strings.Builder
	for _, b := range binaries {
		files[asset(b)] = fakeBinary(b)
		fmt.Fprintf(&sums, "%s  %s\n", sha256Hex(fakeBinary(b)), asset(b))
		fmt.Fprintf(&sums, "%s  %s-plan9-mips\n", sha256Hex("other"), b)
	}
	files["SHA256SUMS.txt"] = sums.String()
	return files
}

// fakeRelease serves files by name, and 404s anything not in the map.
func fakeRelease(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runInstaller runs install.sh with HOME pointed at home and returns the
// combined output and the exit status. extraEnv is appended last, so it wins.
//
// The script is read here and fed to "bash -s", the way the documented
// "curl ... | bash" runs it. Reading it through os.ReadFile also makes Go's
// test cache depend on install.sh, so an install.sh-only edit cannot report a
// cached PASS.
func runInstaller(t *testing.T, baseURL, home string, extraEnv ...string) (string, int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh does not target Windows")
	}
	script, err := os.ReadFile(installScript)
	if err != nil {
		t.Fatalf("reading %s: %v", installScript, err)
	}
	// Bounded, so a download that hangs fails the test instead of the run.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-s")
	cmd.Stdin = bytes.NewReader(script)
	// Killing bash leaves curl holding the output pipe; stop waiting on it.
	cmd.WaitDelay = 5 * time.Second
	var env []string
	for _, kv := range os.Environ() {
		// Drop anything that would redirect the install away from home.
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "INSTALL_DIR=") ||
			strings.HasPrefix(kv, "LLM_EXTRACT_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(append(env, "HOME="+home, "LLM_EXTRACT_BASE_URL="+baseURL), extraEnv...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("install.sh did not finish within 60s\noutput:\n%s", out.String())
	}
	if err == nil {
		return out.String(), 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), ee.ExitCode()
	}
	t.Fatalf("running install.sh: %v", err)
	return "", -1
}

func binDir(home string) string { return filepath.Join(home, ".local", "bin") }

// preinstall puts an old copy of both binaries in the install directory and
// returns their contents by name.
func preinstall(t *testing.T, home string) map[string]string {
	t.Helper()
	if err := os.MkdirAll(binDir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	old := map[string]string{}
	for _, b := range binaries {
		old[b] = "#!/bin/sh\necho 'old " + b + "'\n"
		if err := os.WriteFile(filepath.Join(binDir(home), b), []byte(old[b]), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return old
}

// assertDirHolds fails unless the install directory holds exactly want (name
// to contents): nothing changed, nothing added, no staging file left behind.
// A nil want means the directory must be empty or absent.
func assertDirHolds(t *testing.T, home string, want map[string]string, out string) {
	t.Helper()
	entries, err := os.ReadDir(binDir(home))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading %s: %v", binDir(home), err)
	}
	for _, e := range entries {
		w, ok := want[e.Name()]
		if !ok {
			t.Errorf("install directory holds unexpected %s\noutput:\n%s", e.Name(), out)
			continue
		}
		got, err := os.ReadFile(filepath.Join(binDir(home), e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != w {
			t.Errorf("%s was changed: got %q, want %q\noutput:\n%s", e.Name(), got, w, out)
		}
	}
	if len(entries) != len(want) {
		t.Errorf("install directory holds %d entries, want %d: %v\noutput:\n%s", len(entries), len(want), entries, out)
	}
}

// The happy path: both binaries land in $HOME/.local/bin as the exact bytes
// served, executable, with no root, no clone and nothing else left behind,
// and the CLI is installed before the server that depends on it.
func TestInstallInstallsBothBinaries(t *testing.T) {
	srv := fakeRelease(t, goodRelease())
	home := t.TempDir()

	out, code := runInstaller(t, srv.URL, home)
	if code != 0 {
		t.Fatalf("install.sh exited %d on a good release\noutput:\n%s", code, out)
	}
	want := map[string]string{}
	for _, b := range binaries {
		want[b] = fakeBinary(b)
		info, err := os.Stat(filepath.Join(binDir(home), b))
		if err != nil {
			t.Fatalf("%s not installed: %v\noutput:\n%s", b, err, out)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable: %v", b, info.Mode())
		}
	}
	assertDirHolds(t, home, want, out)

	cli := strings.Index(out, "Installed "+filepath.Join(binDir(home), "llm-extract")+"\n")
	mcp := strings.Index(out, "Installed "+filepath.Join(binDir(home), "llm-extract-mcp")+"\n")
	if cli < 0 || mcp < 0 || cli > mcp {
		t.Errorf("want llm-extract reported installed before llm-extract-mcp\noutput:\n%s", out)
	}
}

// INSTALL_DIR is the documented way to install somewhere other than
// $HOME/.local/bin; it must be honoured, not silently ignored.
func TestInstallHonoursInstallDir(t *testing.T) {
	srv := fakeRelease(t, goodRelease())
	home := t.TempDir()
	dir := filepath.Join(t.TempDir(), "elsewhere")

	out, code := runInstaller(t, srv.URL, home, "INSTALL_DIR="+dir)
	if code != 0 {
		t.Fatalf("install.sh exited %d\noutput:\n%s", code, out)
	}
	for _, b := range binaries {
		got, err := os.ReadFile(filepath.Join(dir, b))
		if err != nil || string(got) != fakeBinary(b) {
			t.Errorf("%s not installed in INSTALL_DIR: %q, %v", b, got, err)
		}
	}
	assertDirHolds(t, home, nil, out)
}

// refusal is one way a release can be unusable. Every one must install
// nothing, exit non-zero, and leave an existing install exactly as it was.
type refusal struct {
	name  string
	files map[string]string
	env   []string
	path  string // a directory prepended to PATH, for a fake uname
}

// checkRefusal runs one refusal twice: into an empty home, where nothing may
// appear, and over an existing install, which must be left untouched.
func checkRefusal(t *testing.T, r refusal) {
	t.Helper()
	srv := fakeRelease(t, r.files)
	env := r.env
	if r.path != "" {
		env = append(env, "PATH="+r.path+string(os.PathListSeparator)+os.Getenv("PATH"))
	}

	t.Run("fresh", func(t *testing.T) {
		home := t.TempDir()
		out, code := runInstaller(t, srv.URL, home, env...)
		if code == 0 {
			t.Errorf("install.sh exited 0\noutput:\n%s", out)
		}
		if !strings.Contains(out, "Nothing was installed.") {
			t.Errorf("refusal does not say nothing was installed\noutput:\n%s", out)
		}
		assertDirHolds(t, home, nil, out)
	})
	t.Run("existing", func(t *testing.T) {
		home := t.TempDir()
		old := preinstall(t, home)
		out, code := runInstaller(t, srv.URL, home, env...)
		if code == 0 {
			t.Errorf("install.sh exited 0\noutput:\n%s", out)
		}
		assertDirHolds(t, home, old, out)
	})
}

// withSums returns a good release whose SHA256SUMS.txt is replaced.
func withSums(sums string) map[string]string {
	f := goodRelease()
	f["SHA256SUMS.txt"] = sums
	return f
}

// without returns a good release with the named files missing (404).
func without(names ...string) map[string]string {
	f := goodRelease()
	for _, n := range names {
		delete(f, n)
	}
	return f
}

func goodLine(b string) string { return sha256Hex(fakeBinary(b)) + "  " + asset(b) + "\n" }

// The case the checksum exists for, for each binary. A mismatch on the server
// alone must also keep the CLI out: the CLI verified, but installing it would
// be a partial install the caller did not ask for.
func TestInstallRefusesChecksumMismatch(t *testing.T) {
	for _, r := range []refusal{
		{name: "cli mismatch", files: withSums(sha256Hex("tampered") + "  " + asset("llm-extract") + "\n" + goodLine("llm-extract-mcp"))},
		{name: "mcp mismatch", files: withSums(goodLine("llm-extract") + sha256Hex("tampered") + "  " + asset("llm-extract-mcp") + "\n")},
		{name: "no line for this platform", files: withSums(sha256Hex("x") + "  llm-extract-plan9-mips\n")},
		{name: "mcp line missing", files: withSums(goodLine("llm-extract"))},
		// A name that merely contains the asset must not count as its line.
		{name: "line only for a longer name", files: withSums(sha256Hex(fakeBinary("llm-extract")) + "  " + asset("llm-extract") + ".exe\n" + goodLine("llm-extract-mcp"))},
		// Two lines for one asset are ambiguous; picking either is a guess.
		{name: "duplicate line", files: withSums(goodLine("llm-extract") + sha256Hex("x") + "  " + asset("llm-extract") + "\n" + goodLine("llm-extract-mcp"))},
		{name: "empty checksum file", files: withSums("")},
	} {
		t.Run(r.name, func(t *testing.T) { checkRefusal(t, r) })
	}
}

// A download that fails outright, for each of the three files, and a server
// error rather than a 404.
func TestInstallRefusesFailedDownload(t *testing.T) {
	for _, r := range []refusal{
		{name: "cli 404", files: without(asset("llm-extract"))},
		{name: "mcp 404", files: without(asset("llm-extract-mcp"))},
		{name: "checksums 404", files: without("SHA256SUMS.txt")},
		{name: "nothing served", files: map[string]string{}},
	} {
		t.Run(r.name, func(t *testing.T) { checkRefusal(t, r) })
	}

	t.Run("server error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		home := t.TempDir()
		old := preinstall(t, home)
		out, code := runInstaller(t, srv.URL, home)
		if code == 0 {
			t.Errorf("install.sh exited 0 on a 500\noutput:\n%s", out)
		}
		assertDirHolds(t, home, old, out)
	})
}

// A download that connects and then stalls is the failed download a user is
// most likely to hit. Without a timeout "curl ... | bash" hangs on it instead
// of refusing. LLM_EXTRACT_DOWNLOAD_TIMEOUT shortens the limit so the test
// does not wait out the default. The stall is on the server binary, after the
// CLI has downloaded, so a script that installs as it goes would be caught.
func TestInstallRefusesStalledDownload(t *testing.T) {
	files := goodRelease()
	stalled := asset("llm-extract-mcp")
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == stalled {
			w.Header().Set("Content-Length", "1000000")
			_, _ = w.Write([]byte(files[name]))
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		body, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs first, so Close does not wait on the handler

	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%v", existing), func(t *testing.T) {
			home := t.TempDir()
			var old map[string]string
			if existing {
				old = preinstall(t, home)
			}
			start := time.Now()
			out, code := runInstaller(t, srv.URL, home, "LLM_EXTRACT_DOWNLOAD_TIMEOUT=2")
			if code == 0 {
				t.Errorf("install.sh exited 0 on a stalled download\noutput:\n%s", out)
			}
			if elapsed := time.Since(start); elapsed > 30*time.Second {
				t.Errorf("install.sh took %v to give up on a stalled download", elapsed)
			}
			assertDirHolds(t, home, old, out)
		})
	}
}

// A malformed timeout must be refused, not handed to curl, where an empty or
// zero value would mean no limit at all.
func TestInstallRefusesBadDownloadTimeout(t *testing.T) {
	for _, v := range []string{"0", "abc", "-5", "1.5"} {
		t.Run(fmt.Sprintf("%q", v), func(t *testing.T) {
			checkRefusal(t, refusal{files: goodRelease(), env: []string{"LLM_EXTRACT_DOWNLOAD_TIMEOUT=" + v}})
		})
	}
}

// fakeUname writes a uname that reports the given kernel and machine, and
// returns its directory for PATH.
func fakeUname(t *testing.T, kernel, machine string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n-s) echo '" + kernel + "' ;;\n-m) echo '" + machine + "' ;;\n*) echo '" + kernel + "' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "uname"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A platform the release does not build for must be refused up front, with
// the release page named, rather than downloading a 404 or the wrong binary.
// Windows is refused too: the release has .exe assets, but this is a shell
// script and does not install them.
func TestInstallRefusesUnsupportedPlatform(t *testing.T) {
	for _, r := range []refusal{
		{name: "unsupported OS", path: fakeUname(t, "FreeBSD", "x86_64")},
		{name: "windows shell", path: fakeUname(t, "MINGW64_NT-10.0", "x86_64")},
		{name: "unsupported arch", path: fakeUname(t, "Linux", "mips")},
		{name: "32-bit arm", path: fakeUname(t, "Linux", "armv7l")},
	} {
		t.Run(r.name, func(t *testing.T) {
			r.files = goodRelease()
			checkRefusal(t, r)
		})
	}
}

// A release URL is built from the version, so a pinned version must reach the
// URL and "latest" must use GitHub's latest-release redirect. The base-URL
// override is how the tests reach a local server; this checks what is fetched
// without it, by putting a fake curl on PATH that records the URL and fails.
func TestInstallBuildsReleaseURLFromVersion(t *testing.T) {
	for _, tc := range []struct{ version, want string }{
		{"", "https://github.com/samestrin/llm-extract-axi/releases/latest/download/"},
		{"latest", "https://github.com/samestrin/llm-extract-axi/releases/latest/download/"},
		{"v1.2.3", "https://github.com/samestrin/llm-extract-axi/releases/download/v1.2.3/"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "urls")
			curl := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in http*) echo \"$a\" >> '" + log + "' ;; esac; done\nexit 22\n"
			if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(curl), 0o755); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			// An empty base URL means "not overridden".
			out, code := runInstaller(t, "", home,
				"LLM_EXTRACT_VERSION="+tc.version,
				"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			if code == 0 {
				t.Errorf("install.sh exited 0 with every download failing\noutput:\n%s", out)
			}
			urls, err := os.ReadFile(log)
			if err != nil {
				t.Fatalf("curl was never called: %v\noutput:\n%s", err, out)
			}
			first := strings.SplitN(string(urls), "\n", 2)[0]
			// SHA256SUMS.txt is fetched first, so it is the one URL logged.
			if want := tc.want + "SHA256SUMS.txt"; first != want {
				t.Errorf("first download is %q, want %q", first, want)
			}
			assertDirHolds(t, home, nil, out)
		})
	}
}

// The CLI is renamed into place before the server. If the server's rename
// then fails, the old CLI must be put back (or, on a fresh install, the new
// one removed): a new CLI beside an old server, or a CLI with no server, is a
// mixed install the caller did not ask for. A fake mv that fails only for the
// server's destination forces that path.
func TestInstallRollsBackWhenSecondRenameFails(t *testing.T) {
	realMv, err := exec.LookPath("mv")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mv := "#!/bin/sh\nfor last; do :; done\ncase \"$last\" in */llm-extract-mcp) echo 'fake mv: refused' >&2; exit 1 ;; esac\nexec '" + realMv + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "mv"), []byte(mv), 0o755); err != nil {
		t.Fatal(err)
	}
	checkRefusal(t, refusal{files: goodRelease(), path: dir})
}
