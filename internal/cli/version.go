package cli

import (
	"fmt"
	"runtime/debug"
)

// Version is stamped at build time with -ldflags.
var Version = "dev"

// BuildVersion is what the tool calls itself: the stamped version, or for an
// unstamped build the commit Go embedded. "dev" on its own names no build, so
// a note saying it cannot be traced back to the code that judged the change.
func BuildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Version
	}
	return resolveVersion(Version, info.Settings)
}

// resolveVersion is BuildVersion's decision, separated from reading the build
// info so it can be tested without one.
func resolveVersion(version string, settings []debug.BuildSetting) string {
	if version != "dev" {
		return version
	}

	var revision string
	var dirty bool
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}

	// No revision means the source was built away from its repository — the
	// image build does that — and there is nothing to name.
	if revision == "" {
		return version
	}
	// A dirty tree says so: the commit alone would claim the binary matches
	// code someone can go and read, and it does not.
	if dirty {
		return fmt.Sprintf("dev (%.7s, modified)", revision)
	}
	return fmt.Sprintf("dev (%.7s)", revision)
}
