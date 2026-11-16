package commands

import (
	"runtime/debug"
	"testing"
)

// A `go install github.com/samestrin/llm-extract-axi/...@latest` binary has no
// ldflags stamp, and `llm-extract --version` printed "dev" instead of the
// installed release. These tests defend the fallback that reads the module
// version from the build info, and the rule that an ldflags stamp always wins.

func buildInfoWithVersion(v string) *debug.BuildInfo {
	return &debug.BuildInfo{Main: debug.Module{Version: v}}
}

// Defends the go install bug: with no ldflags stamp ("dev"), the module
// version recorded by `go install` is the reported version.
func TestResolveVersionFallsBackToBuildInfo(t *testing.T) {
	if got := resolveVersion("dev", buildInfoWithVersion("v1.1.0")); got != "v1.1.0" {
		t.Errorf("resolveVersion(dev, v1.1.0 build info) = %q, want v1.1.0", got)
	}
}

// Defends ldflags precedence: a Makefile or release-build stamp must win over
// whatever the build info says, so a stamped binary never reports a different
// version than the one it was built as.
func TestResolveVersionLdflagsWin(t *testing.T) {
	if got := resolveVersion("1.1.0", buildInfoWithVersion("v9.9.9")); got != "1.1.0" {
		t.Errorf("resolveVersion(1.1.0, v9.9.9 build info) = %q, want 1.1.0", got)
	}
	if got := resolveVersion("v1.2.3-4-gabcdef", buildInfoWithVersion("v9.9.9")); got != "v1.2.3-4-gabcdef" {
		t.Errorf("resolveVersion stamped git describe = %q, want it unchanged", got)
	}
}

// Defends against reporting "(devel)": a local `go build` records Main.Version
// "(devel)", which is not a release, so the fallback must skip it and keep the
// default rather than print a pseudo-version.
func TestResolveVersionSkipsDevel(t *testing.T) {
	if got := resolveVersion("dev", buildInfoWithVersion("(devel)")); got != "dev" {
		t.Errorf("resolveVersion(dev, (devel) build info) = %q, want dev", got)
	}
}

// Defends the no-information paths: no build info at all, or build info with
// an empty Main.Version, must keep the stamped default instead of printing an
// empty version line.
func TestResolveVersionWithoutUsableBuildInfoKeepsStamp(t *testing.T) {
	if got := resolveVersion("dev", nil); got != "dev" {
		t.Errorf("resolveVersion(dev, nil) = %q, want dev", got)
	}
	if got := resolveVersion("dev", buildInfoWithVersion("")); got != "dev" {
		t.Errorf("resolveVersion(dev, empty Main.Version) = %q, want dev", got)
	}
	if got := resolveVersion("dev", &debug.BuildInfo{}); got != "dev" {
		t.Errorf("resolveVersion(dev, zero BuildInfo) = %q, want dev", got)
	}
}

// Defends the wrapper the MCP server calls: an ldflags stamp passes through
// ResolveVersion unchanged no matter what build info the process carries.
func TestResolveVersionWrapperKeepsStamp(t *testing.T) {
	if got := ResolveVersion("1.2.3"); got != "1.2.3" {
		t.Errorf("ResolveVersion(1.2.3) = %q, want 1.2.3", got)
	}
}

// Defends the printed form: the go install module version carries a leading
// "v" (v1.1.0), and --version prints the bare form, as it does for a stamp.
func TestResolveVersionFallbackPrintsBare(t *testing.T) {
	v := resolveVersion("dev", buildInfoWithVersion("v1.1.0"))
	if got := BareVersion(v); got != "1.1.0" {
		t.Errorf("BareVersion(fallback) = %q, want 1.1.0", got)
	}
}
