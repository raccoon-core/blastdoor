package detect

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// opts for the repo built by repo(), diffing the last commit.
func lastCommit(dir string) Options {
	return Options{Root: "terraform", BaseRef: "HEAD~1", HeadRef: "HEAD", RepoDir: dir}
}

// git runs one git command in dir, failing the test if it does not.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// commitAll writes several files and commits them together.
func commitAll(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "change several files")
}

// remove deletes a tracked file and commits the deletion.
func remove(t *testing.T, dir, name string) {
	t.Helper()
	git(t, dir, "rm", "-q", name)
	git(t, dir, "commit", "-m", "remove "+name)
}

// A file a unit reads but that plans nothing on its own — a topic list, a
// template, a values file — changes what gets applied while selecting no unit.
// Nothing downstream would ever see it.
func TestUncoveredFileInsideAUnit(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "terraform/kafka/stg/topics.yaml", "topics: [orders]\n")

	got, err := Uncovered(lastCommit(dir))
	if err != nil {
		t.Fatalf("Uncovered: %v", err)
	}

	want := []string{"terraform/kafka/stg/topics.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The version files decide which binary plans and applies every unit beneath
// them. They are not .hcl, so they select nothing.
func TestUncoveredToolVersionFileAboveUnits(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "terraform/.terragrunt-version", "0.60.0\n")

	got, err := Uncovered(lastCommit(dir))
	if err != nil {
		t.Fatalf("Uncovered: %v", err)
	}

	want := []string{"terraform/.terragrunt-version"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A file that does select units is covered: something plans it, and a policy
// judges what the plan says.
func TestCoveredFilesAreNotReported(t *testing.T) {
	for _, file := range []string{
		"terraform/kafka/stg/terragrunt.hcl", // the unit's own config
		"terraform/component.hcl",            // an ancestor, pulls in every unit below
		"terraform/kafka/stg/main.tf",        // plain terraform in a unit
		"terraform/kafka/stg/prod.tfvars",    // variables change what is applied
	} {
		t.Run(file, func(t *testing.T) {
			dir := repo(t)
			commit(t, dir, file, "# changed\n")

			got, err := Uncovered(lastCommit(dir))
			if err != nil {
				t.Fatalf("Uncovered: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("got %v, want nothing: this file selects a unit", got)
			}
		})
	}
}

// Files outside the root are still reported. Deciding they are harmless is
// the caller's job, through its ignore list — not a silent rule in here.
func TestUncoveredReportsFilesOutsideRoot(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "README.md", "changed\n")

	got, err := Uncovered(lastCommit(dir))
	if err != nil {
		t.Fatalf("Uncovered: %v", err)
	}

	want := []string{"README.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A .tfvars in a directory with no unit under it plans nothing, even though
// its extension says it could.
func TestUncoveredInfraFileWithNoUnitBeneath(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "scratch/experiment.tfvars", "a = 1\n")

	got, err := Uncovered(lastCommit(dir))
	if err != nil {
		t.Fatalf("Uncovered: %v", err)
	}

	want := []string{"scratch/experiment.tfvars"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Deleting a unit leaves a directory that holds no terragrunt.hcl, so nothing
// plans the removal. It has to be visible rather than silently unplanned.
func TestUncoveredReportsADeletedUnit(t *testing.T) {
	dir := repo(t)
	remove(t, dir, "terraform/s3/prd/terragrunt.hcl")

	got, err := Uncovered(lastCommit(dir))
	if err != nil {
		t.Fatalf("Uncovered: %v", err)
	}

	want := []string{"terraform/s3/prd/terragrunt.hcl"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Several changes at once report every uncovered file, sorted, once each.
func TestUncoveredSortsAndDeduplicates(t *testing.T) {
	dir := repo(t)
	commitAll(t, dir, map[string]string{
		"terraform/.terragrunt-version":      "0.60.0\n",
		"terraform/kafka/stg/topics.yaml":    "topics: [orders]\n",
		"terraform/kafka/stg/terragrunt.hcl": "inputs = { a = 1 }\n",
		"README.md":                          "changed\n",
	})

	got, err := Uncovered(lastCommit(dir))
	if err != nil {
		t.Fatalf("Uncovered: %v", err)
	}

	want := []string{
		"README.md",
		"terraform/.terragrunt-version",
		"terraform/kafka/stg/topics.yaml",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
