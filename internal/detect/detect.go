// Package detect finds which units a change touches.
//
// A unit is a directory holding a root config file (terragrunt.hcl, or *.tf
// for plain Terraform/OpenTofu). Terragrunt's find_in_parent_folders() means a
// unit's inputs can come from .hcl files in any ancestor directory, so a
// change to a shared component.hcl affects every unit beneath it.
package detect

import (
	"fmt"
	"io/fs"
	"iter"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Options configures unit detection.
type Options struct {
	// Root is the directory to search for units, relative to the repo root.
	Root string
	// BaseRef and HeadRef bound the git diff.
	BaseRef, HeadRef string
	// RepoDir is where git commands run. Defaults to the current directory.
	RepoDir string
}

// Changed returns the units affected by the diff between BaseRef and HeadRef,
// sorted and de-duplicated.
func Changed(opts Options) ([]string, error) {
	changed, err := ChangedFiles(opts)
	if err != nil {
		return nil, err
	}
	units, err := FindUnits(opts.RepoDir, opts.Root)
	if err != nil {
		return nil, err
	}
	return affected(units, changed, opts.Root), nil
}

// FindUnits lists every unit directory under root.
func FindUnits(repoDir, root string) ([]string, error) {
	base := root
	if repoDir != "" {
		base = filepath.Join(repoDir, root)
	}

	seen := map[string]bool{}
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Cached module sources are copies of upstream modules, not
			// units of this repo.
			if d.Name() == ".terragrunt-cache" || d.Name() == ".terraform" || d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if name != "terragrunt.hcl" && filepath.Ext(name) != ".tf" {
			return nil
		}
		dir := filepath.Dir(p)
		if repoDir != "" {
			rel, relErr := filepath.Rel(repoDir, dir)
			if relErr != nil {
				return relErr
			}
			dir = rel
		}
		seen[filepath.ToSlash(dir)] = true
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning %s for units: %w", base, err)
	}

	units := make([]string, 0, len(seen))
	for u := range seen {
		units = append(units, u)
	}
	sort.Strings(units)
	return units, nil
}

// ancestors yields a unit's own directory and each parent above it, stopping
// at root.
//
// How far a unit's configuration reaches is one rule with two readings: a unit
// is affected by a change anywhere above it (affected), and a directory covers
// a unit when some unit sits at or below it (uncovered). Both walk this chain,
// so it is written once — two copies of the stop condition would eventually
// disagree about whether root itself counts.
func ancestors(unit, root string) iter.Seq[string] {
	return func(yield func(string) bool) {
		for dir := unit; ; dir = path.Dir(dir) {
			if !yield(dir) {
				return
			}
			if dir == root || dir == "." || dir == "/" {
				return
			}
		}
	}
}

// affected keeps the units for which a .hcl or .tf file changed in the unit
// itself or in any ancestor directory up to root.
func affected(units, changed []string, root string) []string {
	changedInDir := map[string]bool{}
	for _, f := range changed {
		if !affectsPlan(f) {
			continue
		}
		changedInDir[path.Dir(f)] = true
	}

	root = filepath.ToSlash(filepath.Clean(root))

	var out []string
	for _, unit := range units {
		for dir := range ancestors(unit, root) {
			if changedInDir[dir] {
				out = append(out, unit)
				break
			}
		}
	}
	return out
}

// ResolveBaseRef works out what to diff against.
//
// On a merge request pipeline GitLab hands us the merge base directly. On a
// branch pipeline it does not, and the obvious-looking variables are traps:
// CI_COMMIT_SHA is HEAD, so diffing against it finds nothing at all. So fall
// back to the merge base with the default branch, which is what "what does
// this branch change" actually means.
func ResolveBaseRef(opts Options) (string, error) {
	if opts.BaseRef != "" {
		// GitLab sets CI_COMMIT_BEFORE_SHA to all zeros on a branch's first
		// pipeline, which is what the default-branch jobs pass as --base-ref.
		// Git resolves it to nothing, and the failure that follows names a SHA
		// nobody wrote, in a job whose real problem is that there is no
		// previous commit.
		if isZeroSHA(opts.BaseRef) {
			return "", fmt.Errorf(
				"base ref %q is the all-zero SHA, which is what CI_COMMIT_BEFORE_SHA holds on a "+
					"branch's first pipeline: there is no previous commit to diff against",
				opts.BaseRef)
		}
		return opts.BaseRef, nil
	}
	if sha := os.Getenv("CI_MERGE_REQUEST_DIFF_BASE_SHA"); sha != "" {
		return sha, nil
	}

	head := opts.HeadRef
	if head == "" {
		head = "HEAD"
	}

	defaultBranch := os.Getenv("CI_DEFAULT_BRANCH")
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	// origin/<branch> first: on a CI checkout the local branch usually does
	// not exist, only the remote-tracking ref.
	for _, candidate := range []string{"origin/" + defaultBranch, defaultBranch} {
		cmd := exec.Command("git", "merge-base", candidate, head)
		cmd.Dir = opts.RepoDir
		out, err := cmd.Output()
		if err == nil {
			return strings.TrimSpace(string(out)), nil
		}
	}

	return "", fmt.Errorf(
		"cannot work out what to diff against: no --base-ref, no CI_MERGE_REQUEST_DIFF_BASE_SHA, "+
			"and no merge base with %q or %q. On a branch pipeline set GIT_DEPTH: 0 so the default "+
			"branch is fetched, or pass --base-ref explicitly",
		"origin/"+defaultBranch, defaultBranch)
}

// ChangedFiles lists the paths that differ between the base and head refs.
func ChangedFiles(opts Options) ([]string, error) {
	base, err := ResolveBaseRef(opts)
	if err != nil {
		return nil, err
	}
	head := opts.HeadRef
	if head == "" {
		head = "HEAD"
	}

	// An empty diff and a misconfigured base look identical downstream —
	// no units, nothing planned, nothing gated. Refuse the one case that is
	// always a mistake rather than reporting "no changes".
	if same, err := sameCommit(opts.RepoDir, base, head); err == nil && same {
		return nil, fmt.Errorf(
			"base ref %q is the same commit as %q, so the diff is empty. On a branch pipeline "+
				"CI_COMMIT_SHA is HEAD — leave --base-ref unset to diff against the default branch, "+
				"or pass something like origin/main",
			base, head)
	}

	// Three dots: changes on head since it forked, not changes that landed
	// on the base branch afterwards.
	cmd := exec.Command("git", "diff", "--name-only", base+"..."+head)
	cmd.Dir = opts.RepoDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s...%s: %w", base, head, err)
	}

	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// affectsPlan reports whether changing this file could change a plan.
//
// Variable files count: a .tfvars edit changes what gets applied while
// touching no .tf at all, and missing it would leave that unit unplanned and
// so ungated.
func affectsPlan(file string) bool {
	switch path.Ext(file) {
	case ".hcl", ".tf", ".tfvars":
		return true
	}
	// Ext only sees ".json" for these, so match the full suffix.
	return strings.HasSuffix(file, ".tf.json") || strings.HasSuffix(file, ".tfvars.json")
}

// sameCommit reports whether two refs resolve to the same commit.
func sameCommit(repoDir, a, b string) (bool, error) {
	resolve := func(ref string) (string, error) {
		cmd := exec.Command("git", "rev-parse", ref)
		cmd.Dir = repoDir
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}

	first, err := resolve(a)
	if err != nil {
		return false, err
	}
	second, err := resolve(b)
	if err != nil {
		return false, err
	}
	return first == second, nil
}

// isZeroSHA reports whether a ref is git's all-zero object id.
//
// Both widths, because a repository on sha256 gets a 64-character one.
func isZeroSHA(ref string) bool {
	if len(ref) != 40 && len(ref) != 64 {
		return false
	}
	return strings.Trim(ref, "0") == ""
}
