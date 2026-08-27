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
	covering := coveringDirs(units, root)

	seen := map[string]bool{}
	var out []string
	for _, file := range changed {
		f := filepath.ToSlash(filepath.Clean(file))
		if seen[f] {
			continue
		}
		if affectsPlan(f) && covering[path.Dir(f)] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// coveringDirs is the set of directories that have a unit at or below them:
// every unit's own directory and every ancestor of one, up to root.
//
// Built once for the whole diff rather than re-derived per file. A repository
// with hundreds of units and a diff touching hundreds of files would otherwise
// re-walk every unit's ancestry for each of them, and the answer never depends
// on which file is being asked about.
func coveringDirs(units []string, root string) map[string]bool {
	covering := map[string]bool{}
	for _, unit := range units {
		for dir := range ancestors(unit, root) {
			// An ancestor already recorded means the rest of this unit's chain
			// was recorded by an earlier unit too.
			if covering[dir] {
				break
			}
			covering[dir] = true
		}
	}
	return covering
}
