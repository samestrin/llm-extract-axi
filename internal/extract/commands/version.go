package commands

import "runtime/debug"

// ResolveVersion returns the version a binary should report: the ldflags
// stamp when the Makefile or release build set it, otherwise the module
// version `go install` recorded in the build info. It exists because a
// `go install ...@latest` binary carries no stamp and used to report "dev"
// instead of the release it was installed as.
func ResolveVersion(stamped string) string {
	bi, _ := debug.ReadBuildInfo()
	return resolveVersion(stamped, bi)
}

// resolveVersion is the pure decision, split out so tests can hand it any
// build info. An ldflags stamp always wins. "(devel)" is what a local
// `go build` records and is not a release, so it is skipped; with no usable
// build info the stamped default is kept rather than an empty version.
func resolveVersion(stamped string, bi *debug.BuildInfo) string {
	if stamped != "" && stamped != "dev" {
		return stamped
	}
	if bi != nil && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return stamped
}
