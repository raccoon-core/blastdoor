package baseline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree is a detached checkout of the baseline ref.
//
// A second checkout rather than a stash or a checkout in place: the head plan
// has already run against the working tree, the artifacts are there, and a
// command that moves a CI checkout out from under itself is a command that
// leaves a wrecked workspace behind when it fails halfway.
type Worktree struct {
	repoDir string
	parent  string // the temporary directory holding the checkout
	dir     string
	ref     string
	commit  string
}

// NewWorktree resolves ref to a commit and checks it out, detached.
//
// The ref must exist. A shallow clone has no origin/<default branch>, and
// resolving to nothing there would produce an empty baseline plan — a clean
// baseline for free, gating nothing while looking green. Fail instead, and say
// what to set.
func NewWorktree(ctx context.Context, repoDir, ref string) (*Worktree, error) {
	commit, err := gitOutput(ctx, repoDir, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf(
			"resolving baseline ref %q: %w. On a shallow clone the target branch is not fetched — set GIT_DEPTH: 0",
			ref, err)
	}

	parent, err := os.MkdirTemp("", "blastdoor-baseline-")
	if err != nil {
		return nil, fmt.Errorf("creating a directory for the baseline checkout: %w", err)
	}
	// git worktree add insists on creating the directory itself.
	dir := filepath.Join(parent, "tree")

	if _, err := gitOutput(ctx, repoDir, "worktree", "add", "--detach", dir, commit); err != nil {
		os.RemoveAll(parent)
		return nil, fmt.Errorf("checking out %s (%.7s) as a baseline: %w", ref, commit, err)
	}

	return &Worktree{repoDir: repoDir, parent: parent, dir: dir, ref: ref, commit: commit}, nil
}

// Dir is where the baseline is checked out.
func (w *Worktree) Dir() string { return w.dir }

// Ref is the ref that was asked for.
func (w *Worktree) Ref() string { return w.ref }

// Commit is what that ref resolved to.
func (w *Worktree) Commit() string { return w.commit }

// UnitDir gives a unit's directory inside the baseline, and whether the unit
// is there at all.
//
// Not there is the ordinary case for a unit the merge request creates, so the
// caller records Absent rather than treating it as a failure.
func (w *Worktree) UnitDir(unit string) (string, bool) {
	dir := filepath.Join(w.dir, filepath.FromSlash(unit))
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}

// Close removes the checkout and the git metadata pointing at it.
//
// Both, and in that order: 'worktree remove' is what unregisters it from the
// repository, and a stale registration outlives the job in a cached CI
// workspace and makes the next run's 'worktree add' fail on a path that is no
// longer there.
func (w *Worktree) Close() error {
	_, gitErr := gitOutput(context.Background(), w.repoDir, "worktree", "remove", "--force", w.dir)
	rmErr := os.RemoveAll(w.parent)
	if gitErr != nil {
		return fmt.Errorf("removing the baseline checkout: %w", gitErr)
	}
	if rmErr != nil {
		return fmt.Errorf("removing %s: %w", w.parent, rmErr)
	}
	return nil
}

// gitOutput runs a git command in repoDir and returns its trimmed stdout.
//
// Stderr is folded into the error: git says why on stderr, and an exit status
// on its own tells a reader nothing.
func gitOutput(ctx context.Context, repoDir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}
