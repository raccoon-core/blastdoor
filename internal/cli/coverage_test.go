package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// coverageRepo builds a repository whose branch changes three files: one that
// selects a unit, and two that change what gets applied while selecting
// nothing.
func coverageRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	write := func(name, body string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	write("terraform/.terragrunt-version", "0.59.0\n")
	write("terraform/kafka/stg/terragrunt.hcl", "inputs = {}\n")
	write("terraform/kafka/stg/topics.yaml", "topics: [orders]\n")
	write("docs/runbook.md", "hello\n")
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	run("add", ".")
	run("commit", "-qm", "initial")

	run("checkout", "-qb", "feature")
	write("terraform/.terragrunt-version", "0.60.0\n")
	write("terraform/kafka/stg/topics.yaml", "topics: [orders, payments]\n")
	write("terraform/kafka/stg/terragrunt.hcl", "inputs = { a = 1 }\n")
	write("docs/runbook.md", "changed\n")
	run("add", ".")
	run("commit", "-qm", "change")

	return dir
}

// The files that plan nothing are reported; the one that selects a unit is
// not, because a plan and a policy already speak for it.
func TestUncoveredFilesReportsWhatNoUnitSelects(t *testing.T) {
	t.Chdir(coverageRepo(t))

	got, err := uncoveredFiles(".", "main", "HEAD", nil)
	if err != nil {
		t.Fatalf("uncoveredFiles: %v", err)
	}

	want := []string{
		"docs/runbook.md",
		"terraform/.terragrunt-version",
		"terraform/kafka/stg/topics.yaml",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Exempt paths — the ignore list, plus the guards that already force review —
// drop out, directories included.
func TestUncoveredFilesHonoursExemptPaths(t *testing.T) {
	t.Chdir(coverageRepo(t))

	got, err := uncoveredFiles(".", "main", "HEAD", []string{"docs", "terraform/.terragrunt-version"})
	if err != nil {
		t.Fatalf("uncoveredFiles: %v", err)
	}

	want := []string{"terraform/kafka/stg/topics.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Scoping to a root does not excuse what lies outside it: the caller says
// what is allowed to go unplanned, and it says so out loud.
func TestUncoveredFilesStillReportsOutsideRoot(t *testing.T) {
	t.Chdir(coverageRepo(t))

	got, err := uncoveredFiles("terraform", "main", "HEAD", nil)
	if err != nil {
		t.Fatalf("uncoveredFiles: %v", err)
	}

	found := false
	for _, f := range got {
		if f == "docs/runbook.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("got %v, want it to include docs/runbook.md", got)
	}
}
