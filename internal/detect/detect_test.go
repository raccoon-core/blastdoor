package detect

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

func TestChangedRequiresBaseRef(t *testing.T) {
	if _, err := Changed(Options{Root: "terraform", RepoDir: repo(t)}); err == nil {
		t.Fatal("expected an error when no base ref is given")
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
