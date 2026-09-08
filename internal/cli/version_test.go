package cli

import (
	"runtime/debug"
	"testing"
)

func settings(pairs ...string) []debug.BuildSetting {
	out := make([]debug.BuildSetting, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, debug.BuildSetting{Key: pairs[i], Value: pairs[i+1]})
	}
	return out
}

// A stamped release says what it was released as, and nothing else: the
// commit is already the tag's.
func TestReleaseVersionIsLeftAlone(t *testing.T) {
	got := resolveVersion("1.2.3", settings("vcs.revision", "470da522ecbd3dda4c3866f698b289b56a564811"))

	if got != "1.2.3" {
		t.Errorf("resolveVersion = %q, want 1.2.3", got)
	}
}

// "dev" names no build. The commit does, and a note is read long after the
// binary that wrote it is gone.
func TestDevVersionNamesTheCommit(t *testing.T) {
	got := resolveVersion("dev", settings("vcs.revision", "470da522ecbd3dda4c3866f698b289b56a564811"))

	if got != "dev (470da52)" {
		t.Errorf("resolveVersion = %q, want dev (470da52)", got)
	}
}

// The commit alone would claim the binary matches code someone can read.
func TestDevVersionSaysWhenTheTreeWasModified(t *testing.T) {
	got := resolveVersion("dev", settings(
		"vcs.revision", "470da522ecbd3dda4c3866f698b289b56a564811",
		"vcs.modified", "true",
	))

	if got != "dev (470da52, modified)" {
		t.Errorf("resolveVersion = %q, want dev (470da52, modified)", got)
	}
}

// Built away from its repository — the image build does that — there is
// nothing to name and nothing is invented.
func TestDevVersionWithoutVCSStaysBare(t *testing.T) {
	if got := resolveVersion("dev", settings("GOARCH", "amd64")); got != "dev" {
		t.Errorf("resolveVersion = %q, want dev", got)
	}
}
