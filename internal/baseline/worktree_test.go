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
	// A file, not a directory: UnitDir must not report a hit just because
	// something exists at that path.
	if _, ok := wt.UnitDir("units/old/main.tf"); ok {
		t.Error("units/old/main.tf is a file but UnitDir reports it as a unit directory")
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

	// The directory being gone isn't enough: a stale registration in the
	// repository's own worktree list is what breaks the next run's
	// 'worktree add' in a cached CI workspace, even after the checkout
	// directory itself has been wiped some other way.
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list --porcelain: %v\n%s", err, out)
	}
	if strings.Contains(string(out), dir) {
		t.Errorf("worktree %s still registered after Close:\n%s", dir, out)
	}
}

func TestWorktreeAddFailureLeavesNoTempDir(t *testing.T) {
	repoDir, _ := repoWithBaselineAndHead(t)

	// Make the ref resolve but the checkout itself fail: strip write
	// permission from .git so 'git worktree add' cannot create its
	// administrative directory under .git/worktrees, while read-only
	// commands like 'rev-parse --verify' still work.
	gitDir := filepath.Join(repoDir, ".git")
	if err := os.Chmod(gitDir, 0o555); err != nil {
		t.Fatalf("chmod .git: %v", err)
	}
	defer func() {
		// Restore write permission so t.TempDir()'s own cleanup can remove
		// the repo; a read-only .git left behind would fail that cleanup
		// for a reason unrelated to this test.
		if err := os.Chmod(gitDir, 0o755); err != nil {
			t.Fatalf("restoring .git permissions: %v", err)
		}
	}()

	before, err := filepath.Glob(filepath.Join(os.TempDir(), "blastdoor-baseline-*"))
	if err != nil {
		t.Fatalf("glob before: %v", err)
	}

	_, err = NewWorktree(context.Background(), repoDir, "main")
	if err == nil {
		t.Fatal("want an error from a repo that cannot host a worktree, got nil")
	}

	after, err := filepath.Glob(filepath.Join(os.TempDir(), "blastdoor-baseline-*"))
	if err != nil {
		t.Fatalf("glob after: %v", err)
	}
	if len(after) > len(before) {
		t.Errorf("NewWorktree leaked a temp dir on failure: before %v, after %v", before, after)
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
