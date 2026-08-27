package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return p
}

func TestLoadReadsEveryKey(t *testing.T) {
	path := write(t, `
root: terraform
tool: terragrunt
manager: tenv
terragrunt_tf_path: tofu
policies:
  company:
    repository: https://git.example.com/policies
    ref: v1
    directory: rules
    weight: 0
  local:
    repository: .
    directory: policy
    weight: 99
require_coverage: true
guard:
  - policy
  - .gitlab-ci.yml
ignore:
  - ansible
  - "**/README.md"
approver_group_ids: [12, 34]
rule_name: blastdoor
auto_merge: true
squash: false
`)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.Root != "terraform" || got.Tool != "terragrunt" || got.Manager != "tenv" || got.TerragruntTFPath != "tofu" {
		t.Errorf("scalars: %+v", got)
	}
	if len(got.Policies) != 2 {
		t.Fatalf("policies = %+v, want two layers", got.Policies)
	}
	company := got.Policies["company"]
	if company.Repository != "https://git.example.com/policies" || company.Ref != "v1" || company.Directory != "rules" {
		t.Errorf("company layer = %+v", company)
	}
	// Weight is a pointer because 0 is a real weight, and the company layer
	// is exactly the one that has it.
	if company.Weight == nil || *company.Weight != 0 {
		t.Errorf("company weight = %v, want 0", company.Weight)
	}
	if !got.Policies["local"].Local() {
		t.Error(`repository "." should read the working tree`)
	}
	if !reflect.DeepEqual(got.Guard, []string{"policy", ".gitlab-ci.yml"}) {
		t.Errorf("guard = %v", got.Guard)
	}
	// The pattern must survive as written: rewriting it is the bug this
	// file exists to prevent.
	if !reflect.DeepEqual(got.Ignore, []string{"ansible", "**/README.md"}) {
		t.Errorf("ignore = %v", got.Ignore)
	}
	if !reflect.DeepEqual(got.ApproverGroupIDs, []GroupID{12, 34}) {
		t.Errorf("approver_group_ids = %v", got.ApproverGroupIDs)
	}
	if got.RequireCoverage == nil || !*got.RequireCoverage {
		t.Errorf("require_coverage = %v", got.RequireCoverage)
	}
	if got.AutoMerge == nil || !*got.AutoMerge {
		t.Errorf("auto_merge = %v", got.AutoMerge)
	}
	// Distinguishing "absent" from "false" is what the pointer is for: the
	// squash flag defaults to true, so a config saying false must win.
	if got.Squash == nil || *got.Squash {
		t.Errorf("squash = %v", got.Squash)
	}
}

// A group id reads the same quoted or bare. The ids also travel as
// BLASTDOOR_APPROVER_GROUP_IDS, where everything is a string, and YAML quotes
// a number as soon as it is copied out of a CI variable or grows a comment.
func TestLoadReadsQuotedGroupIDs(t *testing.T) {
	got, err := Load(write(t, "approver_group_ids:\n  - \"15685\" # Operations SRE\n  - 34\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got.ApproverGroupIDs, []GroupID{15685, 34}) {
		t.Errorf("approver_group_ids = %v, want [15685 34]", got.ApproverGroupIDs)
	}
}

// A group path is not a group id. Accepting it here would fail at the gate
// instead, which is the wrong end of the pipeline to find out.
func TestLoadRejectsNonNumericGroupID(t *testing.T) {
	_, err := Load(write(t, "approver_group_ids:\n  - operations/sre\n"))
	if err == nil {
		t.Fatal("Load: want an error for a group path")
	}
	if !strings.Contains(err.Error(), "approver_group_ids") || !strings.Contains(err.Error(), "operations/sre") {
		t.Errorf("error should name the key and the value: %v", err)
	}
}

func TestLoadEmptyFileIsValid(t *testing.T) {
	got, err := Load(write(t, ""))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Root != "" || got.Guard != nil || got.RequireCoverage != nil {
		t.Errorf("empty config should set nothing: %+v", got)
	}
}

// An unhandled key rejects the whole file. Skipping it, or carrying on
// without the config, would run with no guards and no ignore list.
func TestLoadRejectsUnknownKey(t *testing.T) {
	_, err := Load(write(t, "ingore:\n  - ansible\n"))
	if err == nil {
		t.Fatal("want an error for an unknown key, got nil")
	}
	if !strings.Contains(err.Error(), `unknown key "ingore"`) {
		t.Errorf("error should name the key, got: %v", err)
	}
	// The reader has a YAML file in front of them, not blastdoor's source.
	if strings.Contains(err.Error(), "config.Config") {
		t.Errorf("error leaks the Go type: %v", err)
	}
}

// The mistake the string-splitting variables invite: a list written as a
// string. It must not be coerced into a one-element list.
func TestLoadRejectsWrongType(t *testing.T) {
	_, err := Load(write(t, `ignore: "ansible roles"`))
	if err == nil {
		t.Fatal("want an error for a string where a list belongs, got nil")
	}
	if !strings.Contains(err.Error(), "ignore") {
		t.Errorf("error should name the key, got: %v", err)
	}
}

func TestLoadRejectsUnparseableYAML(t *testing.T) {
	if _, err := Load(write(t, "root: [terraform\n")); err == nil {
		t.Fatal("want an error for unparseable YAML, got nil")
	}
}

// Asking for a config and silently getting none is the failure this whole
// mechanism exists to prevent.
func TestFindNamedFileMustExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yml")

	if _, err := Find(missing); err == nil {
		t.Fatal("want an error for a --config that does not exist, got nil")
	}
}

func TestFindDefaultInWorkingDirectory(t *testing.T) {
	path := write(t, "root: terraform\n")
	t.Chdir(filepath.Dir(path))

	got, err := Find("")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != FileName {
		t.Errorf("got %q, want %q", got, FileName)
	}
}

// No config is not an error: every setting keeps its default.
func TestFindNoDefaultIsNotAnError(t *testing.T) {
	t.Chdir(t.TempDir())

	got, err := Find("")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want no path", got)
	}
}

// Discovery looks in one place. A config in a parent directory does not
// quietly take effect from a subdirectory.
func TestFindDoesNotSearchUpwards(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("root: terraform\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "terraform", "kafka")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	got, err := Find("")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want no path: discovery must not walk up", got)
	}
}

// A weight left out would default to zero, which for a local layer means the
// company rules quietly override it — the opposite of what was meant.
func TestLoadRejectsALayerWithNoWeight(t *testing.T) {
	_, err := Load(write(t, "policies:\n  local:\n    repository: .\n    directory: policy\n"))
	if err == nil {
		t.Fatal("want an error for a layer with no weight")
	}
	if !strings.Contains(err.Error(), "local") || !strings.Contains(err.Error(), "weight") {
		t.Errorf("error should name the layer and the missing weight, got: %v", err)
	}
}

// A remote source with no ref has no defined content: "whatever HEAD is
// today" cannot be reproduced or explained.
func TestLoadRejectsARemoteLayerWithNoRef(t *testing.T) {
	_, err := Load(write(t, "policies:\n  company:\n    repository: https://git.example.com/p\n    weight: 0\n"))
	if err == nil {
		t.Fatal("want an error for a remote layer with no ref")
	}
	if !strings.Contains(err.Error(), "ref") {
		t.Errorf("error should name the missing ref, got: %v", err)
	}
}

// The removed key fails by name rather than being quietly ignored, which is
// what makes the migration a message instead of an empty policy set.
func TestLoadRejectsTheRemovedPolicyKey(t *testing.T) {
	_, err := Load(write(t, "policy:\n  - policy\n"))
	if err == nil {
		t.Fatal("want an error for the removed policy key")
	}
	if !strings.Contains(err.Error(), `unknown key "policy"`) {
		t.Errorf("error should name the key, got: %v", err)
	}
}
