package baseline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoWithBaselineAndHead builds a real repository whose main branch holds one
// unit, and whose feature branch adds a second. It returns the repo directory
// and main's commit.
func repoWithBaselineAndHead(t *testing.T) (repoDir, mainCommit string) {
	t.Helper()
	repoDir = t.TempDir()

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(repoDir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")

	write("units/old/main.tf", "resource \"null_resource\" \"a\" {}\n")
	run("add", ".")
	run("commit", "-qm", "baseline")
	mainCommit = run("rev-parse", "HEAD")

	run("checkout", "-q", "-b", "feature")
	write("units/new/main.tf", "resource \"null_resource\" \"b\" {}\n")
	run("add", ".")
	run("commit", "-qm", "head")

	return repoDir, mainCommit
}

func TestWorktreeResolvesRefToCommit(t *testing.T) {
	repoDir, mainCommit := repoWithBaselineAndHead(t)

	wt, err := NewWorktree(context.Background(), repoDir, "main")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Close()

	// The ref is what was asked for; the commit is what makes the verdict
	// explainable later, once the ref has moved.
	if wt.Ref() != "main" {
		t.Errorf("Ref() = %q, want main", wt.Ref())
	}
	if wt.Commit() != mainCommit {
		t.Errorf("Commit() = %q, want %q", wt.Commit(), mainCommit)
	}
}

func TestWorktreeUnitDirReportsAbsentUnits(t *testing.T) {
	repoDir, _ := repoWithBaselineAndHead(t)

	wt, err := NewWorktree(context.Background(), repoDir, "main")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Close()

	if _, ok := wt.UnitDir("units/old"); !ok {
		t.Error("units/old exists on main but UnitDir says it does not")
	}
	// A unit the merge request creates is not at the baseline. That is clean,
	// not an error.
	if _, ok := wt.UnitDir("units/new"); ok {
		t.Error("units/new exists only on the feature branch but UnitDir found it")
	}
}

func TestWorktreeCloseRemovesTheCheckout(t *testing.T) {
	repoDir, _ := repoWithBaselineAndHead(t)

	wt, err := NewWorktree(context.Background(), repoDir, "main")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	dir := wt.Dir()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("worktree not on disk: %v", err)
	}

	if err := wt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("worktree still on disk after Close: %v", err)
	}
}

func TestWorktreeUnknownRefFails(t *testing.T) {
	repoDir, _ := repoWithBaselineAndHead(t)

	// A ref that is not there is the shallow-clone case, and it must fail the
	// command rather than resolve to nothing and look like a clean baseline.
	_, err := NewWorktree(context.Background(), repoDir, "origin/nope")
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if !strings.Contains(err.Error(), "origin/nope") {
		t.Errorf("error should name the ref, got %v", err)
	}
}
