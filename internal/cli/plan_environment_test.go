package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The flag is recorded per unit rather than once per run, for the same reason
// engine.txt is: eval reads it from another job, and when plans are split
// across a parallel matrix their artifacts are merged. One file per unit
// merges; one file per run collides.
func TestWriteEnvironmentFileRecordsThePerUnitFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, ".blastdoor")

	if err := writeEnvironmentFile(filepath.Join(out, "ops/int/topics"), "int"); err != nil {
		t.Fatalf("writeEnvironmentFile: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(out, "ops/int/topics", "environment.txt"))
	if err != nil {
		t.Fatalf("reading environment.txt: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "int" {
		t.Errorf("environment.txt = %q, want %q", got, "int")
	}
}

func TestWriteEnvironmentFileSkipsAnEmptyName(t *testing.T) {
	dir := t.TempDir()
	if err := writeEnvironmentFile(dir, ""); err != nil {
		t.Fatalf("writeEnvironmentFile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "environment.txt")); !os.IsNotExist(err) {
		t.Error("environment.txt was written for an empty environment; without a wish the feature is off")
	}
}
