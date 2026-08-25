package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/config"
)

// withConfig writes a config in a temp working directory and loads it the way
// the root command does.
func withConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(body), 0o600); err != nil {
			t.Fatalf("writing config: %v", err)
		}
	}
	t.Chdir(dir)

	configPath = ""
	prev := loaded
	t.Cleanup(func() { loaded = prev })
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
}

// run executes the root command with args, returning its error.
func run(t *testing.T, args ...string) error {
	t.Helper()
	cmd := NewRootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	return cmd.Execute()
}

// The config supplies a setting the flag did not.
func TestConfigSuppliesRoot(t *testing.T) {
	withConfig(t, "root: terraform\n")

	cmd := newDetectCmd()
	got := pickString(cmd, "root", ".", cfg().Root)
	if got != "terraform" {
		t.Errorf("root = %q, want %q", got, "terraform")
	}
}

// A flag left at its default must not out-rank the config just because the
// default is non-empty. --root defaults to ".", which would otherwise win
// every time and make the file useless.
func TestUntouchedFlagDoesNotBeatConfig(t *testing.T) {
	withConfig(t, "root: terraform\n")

	cmd := newDetectCmd()
	if err := cmd.Flags().Parse(nil); err != nil {
		t.Fatal(err)
	}
	if got := pickString(cmd, "root", ".", cfg().Root); got != "terraform" {
		t.Errorf("root = %q, want the config to win", got)
	}
}

// A flag that was given wins.
func TestGivenFlagBeatsConfig(t *testing.T) {
	withConfig(t, "root: terraform\n")

	cmd := newDetectCmd()
	if err := cmd.Flags().Parse([]string{"--root", "infra"}); err != nil {
		t.Fatal(err)
	}
	if got := pickString(cmd, "root", "infra", cfg().Root); got != "infra" {
		t.Errorf("root = %q, want %q", got, "infra")
	}
}

// A list is replaced whole, never merged: half a guard list from the pipeline
// and half from the repository would leave nobody able to say what is guarded.
func TestGuardListIsReplacedNotMerged(t *testing.T) {
	withConfig(t, "guard:\n  - policy\n  - .gitlab-ci.yml\n")

	cmd := newEvalCmd()
	if err := cmd.Flags().Parse([]string{"--guard-path", "policy"}); err != nil {
		t.Fatal(err)
	}

	got := guardPathsFor(cmd, []string{"policy"})
	want := []string{"policy", config.FileName}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("guards = %v, want %v: the flag list replaces the config's", got, want)
	}
}

// Self-guarding is not a setting either source states, so neither can replace
// it: the config's own path is guarded even when the config names no guards.
func TestConfigIsGuardedEvenWhenItNamesNone(t *testing.T) {
	withConfig(t, "root: terraform\n")

	cmd := newEvalCmd()
	if err := cmd.Flags().Parse(nil); err != nil {
		t.Fatal(err)
	}

	got := guardPathsFor(cmd, nil)
	if !reflect.DeepEqual(got, []string{config.FileName}) {
		t.Errorf("guards = %v, want the config itself to be guarded", got)
	}
}

// With no config there is nothing to self-guard, and the flags stand alone.
func TestNoConfigLeavesGuardsAlone(t *testing.T) {
	withConfig(t, "")

	cmd := newEvalCmd()
	if err := cmd.Flags().Parse([]string{"--guard-path", "policy"}); err != nil {
		t.Fatal(err)
	}

	got := guardPathsFor(cmd, []string{"policy"})
	if !reflect.DeepEqual(got, []string{"policy"}) {
		t.Errorf("guards = %v, want just the flag", got)
	}
}

// A bool the config sets and the flag did not.
func TestConfigTurnsOnRequireCoverage(t *testing.T) {
	withConfig(t, "require_coverage: true\n")

	cmd := newEvalCmd()
	if err := cmd.Flags().Parse(nil); err != nil {
		t.Fatal(err)
	}
	if !pickBool(cmd, "require-coverage", false, cfg().RequireCoverage) {
		t.Error("require_coverage should come from the config")
	}
}

// A config false must beat a flag default of true, which is why the config's
// bools are pointers.
func TestConfigFalseBeatsATrueDefault(t *testing.T) {
	withConfig(t, "squash: false\n")

	cmd := newGateCmd()
	if err := cmd.Flags().Parse(nil); err != nil {
		t.Fatal(err)
	}
	if pickBool(cmd, "squash", true, cfg().Squash) {
		t.Error("squash: false in the config should win over the flag default")
	}
}

// The pattern must reach the matcher exactly as written — the shell rewriting
// it is the bug this whole mechanism exists to prevent.
func TestIgnorePatternSurvivesFromConfigToMatcher(t *testing.T) {
	withConfig(t, "ignore:\n  - \"**/README.md\"\n")

	cmd := newEvalCmd()
	if err := cmd.Flags().Parse(nil); err != nil {
		t.Fatal(err)
	}

	ignores := pickList(cmd, "ignore-path", nil, cfg().Ignore)
	if !reflect.DeepEqual(ignores, []string{"**/README.md"}) {
		t.Fatalf("ignore = %v", ignores)
	}
	if !matchesGuard("terraform/components/kafka/README.md", ignores) {
		t.Error("a nested README should match the pattern from the config")
	}
}

// A file blastdoor cannot fully understand stops the command. Running on
// means running with no guards and no ignore list.
func TestUnknownKeyFailsTheCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("ingore: [a]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	configPath = ""
	prev := loaded
	t.Cleanup(func() { loaded = prev })

	err := run(t, "detect")
	if err == nil {
		t.Fatal("want the command to fail on an unknown key")
	}
	if !strings.Contains(err.Error(), "ingore") {
		t.Errorf("error should name the key, got: %v", err)
	}
}

// Asking for a config that is not there fails rather than running unconfigured.
func TestMissingNamedConfigFailsTheCommand(t *testing.T) {
	t.Chdir(t.TempDir())
	prev := loaded
	prevPath := configPath
	t.Cleanup(func() { loaded, configPath = prev, prevPath })

	if err := run(t, "detect", "--config", "nope.yml"); err == nil {
		t.Fatal("want the command to fail when --config does not exist")
	}
}
