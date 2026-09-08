package detect

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// repo builds a git repo with a Terragrunt layout: a shared component.hcl
// above two per-environment units.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	files := map[string]string{
		"terraform/component.hcl":            "locals {}\n",
		"terraform/kafka/stg/terragrunt.hcl": "inputs = {}\n",
		"terraform/kafka/prd/terragrunt.hcl": "inputs = {}\n",
		"terraform/s3/prd/terragrunt.hcl":    "inputs = {}\n",
		"README.md":                          "hello\n",
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"add", "."},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func commit(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "change " + name}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// Editing one unit's own file affects only that unit.
func TestChangedUnitOwnFile(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "terraform/kafka/stg/terragrunt.hcl", "inputs = { a = 1 }\n")

	got, err := Changed(Options{Root: "terraform", BaseRef: "HEAD~1", HeadRef: "HEAD", RepoDir: dir})
	if err != nil {
		t.Fatalf("Changed: %v", err)
	}

	want := []string{"terraform/kafka/stg"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A shared ancestor .hcl pulls in every unit beneath it — the behaviour that
// makes a regenerated component.hcl plan both environments.
func TestChangedSharedAncestorAffectsAllUnitsBeneath(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "terraform/component.hcl", "locals { changed = true }\n")

	got, err := Changed(Options{Root: "terraform", BaseRef: "HEAD~1", HeadRef: "HEAD", RepoDir: dir})
	if err != nil {
		t.Fatalf("Changed: %v", err)
	}

	want := []string{"terraform/kafka/prd", "terraform/kafka/stg", "terraform/s3/prd"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A non-infrastructure file affects nothing.
func TestChangedIgnoresUnrelatedFiles(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "README.md", "changed\n")

	got, err := Changed(Options{Root: "terraform", BaseRef: "HEAD~1", HeadRef: "HEAD", RepoDir: dir})
	if err != nil {
		t.Fatalf("Changed: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want no units", got)
	}
}

// On the default branch itself there is nothing to compare against, which is
// an error rather than an empty result that would gate nothing.
func TestChangedOnTheDefaultBranchHasNoBase(t *testing.T) {
	t.Setenv("CI_MERGE_REQUEST_DIFF_BASE_SHA", "")
	t.Setenv("CI_DEFAULT_BRANCH", "main")

	if _, err := Changed(Options{Root: "terraform", RepoDir: repo(t)}); err == nil {
		t.Fatal("expected an error: HEAD is the default branch, so there is nothing to diff")
	}
}

// Cached module copies are not units of this repo.
func TestFindUnitsSkipsTerragruntCache(t *testing.T) {
	dir := repo(t)
	cached := filepath.Join(dir, "terraform/kafka/stg/.terragrunt-cache/abc/def")
	if err := os.MkdirAll(cached, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cached, "main.tf"), []byte("# module\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	units, err := FindUnits(dir, "terraform")
	if err != nil {
		t.Fatalf("FindUnits: %v", err)
	}

	for _, u := range units {
		if filepath.Base(filepath.Dir(u)) == ".terragrunt-cache" || u == "terraform/kafka/stg/.terragrunt-cache/abc/def" {
			t.Errorf("cache directory %q was reported as a unit", u)
		}
	}
	if len(units) != 3 {
		t.Errorf("got %d units (%v), want 3", len(units), units)
	}
}

// A .tfvars edit changes what gets applied without touching any .tf, so it
// has to pull its unit into the plan.
func TestChangedPicksUpVariableFiles(t *testing.T) {
	for _, name := range []string{"terraform.tfvars", "prod.auto.tfvars.json", "main.tf.json"} {
		t.Run(name, func(t *testing.T) {
			dir := repo(t)
			commit(t, dir, "terraform/kafka/stg/"+name, "{}\n")

			got, err := Changed(Options{Root: "terraform", BaseRef: "HEAD~1", HeadRef: "HEAD", RepoDir: dir})
			if err != nil {
				t.Fatalf("Changed: %v", err)
			}
			if len(got) != 1 || got[0] != "terraform/kafka/stg" {
				t.Errorf("got %v, want [terraform/kafka/stg]", got)
			}
		})
	}
}

// branch creates a branch off the current HEAD.
func branch(t *testing.T, dir, name string) {
	t.Helper()
	cmd := exec.Command("git", "checkout", "-q", "-b", name)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b %s: %v\n%s", name, err, out)
	}
}

func checkout(t *testing.T, dir, name string) {
	t.Helper()
	cmd := exec.Command("git", "checkout", "-q", name)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout %s: %v\n%s", name, err, out)
	}
}

// On a branch pipeline there is no merge request base, so blastdoor has to
// work one out from the default branch by itself.
func TestChangedOnABranchWithoutAnyBaseRef(t *testing.T) {
	t.Setenv("CI_MERGE_REQUEST_DIFF_BASE_SHA", "")
	t.Setenv("CI_DEFAULT_BRANCH", "main")

	dir := repo(t)
	branch(t, dir, "feature")
	commit(t, dir, "terraform/kafka/stg/terragrunt.hcl", "inputs = { a = 1 }\n")

	got, err := Changed(Options{Root: "terraform", RepoDir: dir})
	if err != nil {
		t.Fatalf("Changed: %v", err)
	}
	want := []string{"terraform/kafka/stg"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// CI_COMMIT_SHA is HEAD on a branch pipeline, so using it as the base finds
// nothing. That must be an error, not a quiet "no changes" that leaves the
// whole pipeline gating nothing.
func TestChangedRejectsABaseRefEqualToHead(t *testing.T) {
	dir := repo(t)
	branch(t, dir, "feature")
	commit(t, dir, "terraform/kafka/stg/terragrunt.hcl", "inputs = { a = 1 }\n")

	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}

	_, err = Changed(Options{Root: "terraform", BaseRef: strings.TrimSpace(string(head)), RepoDir: dir})
	if err == nil {
		t.Fatal("a base ref equal to HEAD was accepted, so the diff was silently empty")
	}
	if !strings.Contains(err.Error(), "same commit") {
		t.Errorf("error does not explain the problem: %v", err)
	}
}

// Work that landed on the default branch after this one forked is not this
// branch's doing, and must not be planned as if it were.
func TestChangedIgnoresCommitsLandedOnTheBaseBranch(t *testing.T) {
	t.Setenv("CI_MERGE_REQUEST_DIFF_BASE_SHA", "")
	t.Setenv("CI_DEFAULT_BRANCH", "main")

	dir := repo(t)
	branch(t, dir, "feature")
	commit(t, dir, "terraform/kafka/stg/terragrunt.hcl", "inputs = { a = 1 }\n")

	// Someone else changes a different unit on main, after the fork.
	checkout(t, dir, "main")
	commit(t, dir, "terraform/s3/prd/terragrunt.hcl", "inputs = { b = 2 }\n")
	checkout(t, dir, "feature")

	got, err := Changed(Options{Root: "terraform", RepoDir: dir})
	if err != nil {
		t.Fatalf("Changed: %v", err)
	}

	want := []string{"terraform/kafka/stg"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v — the other branch's unit leaked in", got, want)
	}
}

// An explicit base ref still wins over everything blastdoor would work out.
func TestResolveBaseRefPrefersTheExplicitOne(t *testing.T) {
	t.Setenv("CI_MERGE_REQUEST_DIFF_BASE_SHA", "deadbeef")

	got, err := ResolveBaseRef(Options{BaseRef: "origin/release", RepoDir: repo(t)})
	if err != nil {
		t.Fatalf("ResolveBaseRef: %v", err)
	}
	if got != "origin/release" {
		t.Errorf("got %q, want origin/release", got)
	}
}

// A merge request pipeline hands us the merge base directly; use it.
func TestResolveBaseRefUsesTheMergeRequestBase(t *testing.T) {
	t.Setenv("CI_MERGE_REQUEST_DIFF_BASE_SHA", "cafebabe")

	got, err := ResolveBaseRef(Options{RepoDir: repo(t)})
	if err != nil {
		t.Fatalf("ResolveBaseRef: %v", err)
	}
	if got != "cafebabe" {
		t.Errorf("got %q, want cafebabe", got)
	}
}

// Nothing to go on is an error that says what to do, not a silent empty diff.
func TestResolveBaseRefFailsWithGuidance(t *testing.T) {
	t.Setenv("CI_MERGE_REQUEST_DIFF_BASE_SHA", "")
	t.Setenv("CI_DEFAULT_BRANCH", "no-such-branch")

	_, err := ResolveBaseRef(Options{RepoDir: repo(t)})
	if err == nil {
		t.Fatal("expected an error when there is no base to work out")
	}
	if !strings.Contains(err.Error(), "GIT_DEPTH") {
		t.Errorf("error does not mention the shallow-clone fix: %v", err)
	}
}

// The default-branch pipeline diffs against CI_COMMIT_BEFORE_SHA, which GitLab
// sets to all zeros on a branch's first pipeline. Git resolves it to nothing,
// and the error that follows names a SHA nobody wrote — so say what actually
// happened.
func TestResolveBaseRefRejectsTheAllZeroSHA(t *testing.T) {
	tests := []struct {
		name string
		ref  string
	}{
		{"sha1", strings.Repeat("0", 40)},
		{"sha256", strings.Repeat("0", 64)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveBaseRef(Options{BaseRef: tc.ref})
			if err == nil {
				t.Fatal("ResolveBaseRef = nil error, want one")
			}
			if !strings.Contains(err.Error(), "CI_COMMIT_BEFORE_SHA") {
				t.Errorf("error does not name the variable that holds it: %v", err)
			}
		})
	}
}

func TestResolveBaseRefKeepsARealRef(t *testing.T) {
	got, err := ResolveBaseRef(Options{BaseRef: "origin/main"})
	if err != nil {
		t.Fatalf("ResolveBaseRef: %v", err)
	}
	if got != "origin/main" {
		t.Errorf("ResolveBaseRef = %q, want %q", got, "origin/main")
	}
}
