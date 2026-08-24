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
		for dir := unit; ; dir = path.Dir(dir) {
			if changedInDir[dir] {
				out = append(out, unit)
				break
			}
			if dir == root || dir == "." || dir == "/" {
				break
			}
		}
	}
	return out
}

// ChangedFiles lists the paths that differ between BaseRef and HeadRef.
func ChangedFiles(opts Options) ([]string, error) {
	if opts.BaseRef == "" {
		return nil, fmt.Errorf("no base ref: pass --base-ref or set CI_MERGE_REQUEST_DIFF_BASE_SHA")
	}
	head := opts.HeadRef
	if head == "" {
		head = "HEAD"
	}

	cmd := exec.Command("git", "diff", "--name-only", opts.BaseRef, head)
	cmd.Dir = opts.RepoDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s %s: %w", opts.BaseRef, head, err)
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
