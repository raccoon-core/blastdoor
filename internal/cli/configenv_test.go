package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/config"
)

// writeConfigAt puts a config somewhere other than the working directory.
func writeConfigAt(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return p
}

// restoreConfigState puts the package-level config back after a test.
func restoreConfigState(t *testing.T) {
	t.Helper()
	prev, prevPath := loaded, configPath
	t.Cleanup(func() { loaded, configPath = prev, prevPath })
}

// A job that cannot change its working directory still has to be able to say
// where the config is.
func TestConfigFromEnvironment(t *testing.T) {
	elsewhere := writeConfigAt(t, "blastdoor.yml", "root: terraform\n")

	t.Chdir(t.TempDir())
	restoreConfigState(t)
	t.Setenv("BLASTDOOR_CONFIG", elsewhere)

	// The command is run for real: the root sets configPath from the
	// environment, and PersistentPreRunE loads it before anything else.
	// detect then fails for want of a git repository, which is not the
	// point — what it loaded is.
	_ = run(t, "detect")

	if cfg().Root != "terraform" {
		t.Errorf("root = %q, want the config named by the environment", cfg().Root)
	}
}

// The flag is more specific than the environment, and wins.
func TestConfigFlagBeatsEnvironment(t *testing.T) {
	fromEnv := writeConfigAt(t, "env.yml", "root: from-env\n")
	fromFlag := writeConfigAt(t, "flag.yml", "root: from-flag\n")

	t.Chdir(t.TempDir())
	restoreConfigState(t)
	t.Setenv("BLASTDOOR_CONFIG", fromEnv)

	_ = run(t, "detect", "--config", fromFlag)

	if cfg().Root != "from-flag" {
		t.Errorf("root = %q, want the flag to win", cfg().Root)
	}
}

// Naming a config that is not there fails, however it was named. Running on
// unconfigured would mean running with no guards and no ignore list.
func TestConfigFromEnvironmentMustExist(t *testing.T) {
	t.Chdir(t.TempDir())
	restoreConfigState(t)
	t.Setenv("BLASTDOOR_CONFIG", filepath.Join(t.TempDir(), "nope.yml"))

	configPath = os.Getenv("BLASTDOOR_CONFIG")
	if err := loadConfig(); err == nil {
		t.Fatal("want an error when BLASTDOOR_CONFIG names a file that is not there")
	}
}

// With the variable unset, discovery is unchanged.
func TestNoEnvironmentKeepsDiscovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("root: discovered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	restoreConfigState(t)
	t.Setenv("BLASTDOOR_CONFIG", "")

	configPath = ""
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg().Root != "discovered" {
		t.Errorf("root = %q, want the config in the working directory", cfg().Root)
	}
}
