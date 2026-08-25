package detect

import (
	"path"
	"path/filepath"
	"sort"
)

// Uncovered returns the changed files that select no unit, sorted and
// de-duplicated.
//
// Changed reports which units a diff affects; this reports what the diff
// changed that no unit accounts for. The two are complementary, and the gap
// between them is the interesting part: a file that changes what gets applied
// but selects nothing is planned by nothing, so it is judged by nothing.
//
// It is a real gap rather than a theoretical one. A .terragrunt-version above
// a tree of units decides the binary that plans and applies all of them. A
// topics.yaml a unit reads through yamldecode(file(...)) is the change itself.
// Neither is .hcl, so neither selects a unit, and a merge request holding only
// those sails through with nothing planned and nothing scored.
//
// Every file the caller has not accounted for is reported, including files
// outside Root. Deciding that docs and playbooks are harmless is a policy
// about one repository's layout, which belongs to the caller and its ignore
// list, not to a silent rule in here.
func Uncovered(opts Options) ([]string, error) {
	changed, err := ChangedFiles(opts)
	if err != nil {
		return nil, err
	}
	units, err := FindUnits(opts.RepoDir, opts.Root)
	if err != nil {
		return nil, err
	}
	return uncovered(units, changed, opts.Root), nil
}

// uncovered keeps the changed files that select no unit.
//
// A file is covered when it could change a plan and some unit sits at or below
// its directory — the same relationship affected() walks in the other
// direction, from each unit up towards the root.
func uncovered(units, changed []string, root string) []string {
	root = filepath.ToSlash(filepath.Clean(root))

	seen := map[string]bool{}
	var out []string
	for _, file := range changed {
		f := filepath.ToSlash(filepath.Clean(file))
		if seen[f] {
			continue
		}
		if affectsPlan(f) && coversAUnit(path.Dir(f), units, root) {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// coversAUnit reports whether any unit is dir itself or lives beneath it.
func coversAUnit(dir string, units []string, root string) bool {
	for _, unit := range units {
		for d := unit; ; d = path.Dir(d) {
			if d == dir {
				return true
			}
			if d == root || d == "." || d == "/" {
				break
			}
		}
	}
	return false
}
