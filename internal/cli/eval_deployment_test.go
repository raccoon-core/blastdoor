package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// planEnvTree writes a minimal but valid plan for one unit, with its environment.
func planEnvTree(t *testing.T, dir, unit, environment string) {
	t.Helper()
	dest := filepath.Join(dir, unit)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := `{"format_version":"1.2","resource_changes":[]}`
	if err := os.WriteFile(filepath.Join(dest, "plan.json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	if environment != "" {
		if err := os.WriteFile(filepath.Join(dest, "environment.txt"), []byte(environment+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnvironmentForReadsWhatPlanRecorded(t *testing.T) {
	dir := t.TempDir()
	planEnvTree(t, dir, "ops/int/topics", "int")

	got := environmentFor(filepath.Join(dir, "ops/int/topics", "plan.json"))
	if got != "int" {
		t.Errorf("environmentFor = %q, want %q", got, "int")
	}
}

// Missing is silence, not an error. Decide reports it, and only when a wish
// makes it matter.
func TestEnvironmentForIsEmptyWhenNothingRecordedIt(t *testing.T) {
	dir := t.TempDir()
	planEnvTree(t, dir, "ops/int/topics", "")

	if got := environmentFor(filepath.Join(dir, "ops/int/topics", "plan.json")); got != "" {
		t.Errorf("environmentFor = %q, want empty", got)
	}
}

func TestEvalWritesTheApplyPipeline(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, "plans")
	outDir := filepath.Join(dir, "out")
	planEnvTree(t, planDir, "ops/int/topics", "int")

	policyDir := filepath.Join(dir, "policy")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rego := "package blastdoor\n"
	if err := os.WriteFile(filepath.Join(policyDir, "p.rego"), []byte(rego), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	cmd := NewRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"eval",
		"--plan-dir", planDir,
		"--policy", policyDir,
		"--out-dir", outDir,
		"--deployment-method-wish", "int=auto,prd=manual",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("eval: %v\n%s", err, out.String())
	}

	envFile, err := os.ReadFile(filepath.Join(outDir, "blastdoor.env"))
	if err != nil {
		t.Fatalf("reading blastdoor.env: %v", err)
	}
	if !strings.Contains(string(envFile), "BLASTDOOR_DEPLOY_INT=auto") {
		t.Errorf("blastdoor.env missing the int method:\n%s", envFile)
	}
	if !strings.Contains(string(envFile), "BLASTDOOR_DEPLOY_PRD=none") {
		t.Errorf("blastdoor.env missing the prd method:\n%s", envFile)
	}

	applyFile, err := os.ReadFile(filepath.Join(outDir, "apply.gitlab-ci.yml"))
	if err != nil {
		t.Fatalf("reading apply.gitlab-ci.yml: %v", err)
	}
	if !strings.Contains(string(applyFile), "apply:int:") {
		t.Errorf("apply.gitlab-ci.yml has no int job:\n%s", applyFile)
	}
}

// All environments resolve to none — the ordinary case for a docs-only or
// CI-only change — so there is nothing to apply anywhere. eval still writes
// apply.gitlab-ci.yml, because a wish was stated, but as a single placeholder
// job: a file holding only an include: header and no jobs is one GitLab
// refuses to build a child pipeline from, so WriteApplyYAML never produces
// that shape.
func TestEvalWritesAPlaceholderApplyPipelineWhenEveryEnvironmentIsNone(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, "plans")
	outDir := filepath.Join(dir, "out")
	// No units at all: every wished environment resolves to none.
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}

	policyDir := filepath.Join(dir, "policy")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "p.rego"), []byte("package blastdoor\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	cmd := NewRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"eval",
		"--plan-dir", planDir,
		"--policy", policyDir,
		"--out-dir", outDir,
		"--deployment-method-wish", "int=auto,stg=auto,prd=manual",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("eval: %v\n%s", err, out.String())
	}

	envFile, err := os.ReadFile(filepath.Join(outDir, "blastdoor.env"))
	if err != nil {
		t.Fatalf("reading blastdoor.env: %v", err)
	}
	for _, want := range []string{"BLASTDOOR_DEPLOY_INT=none", "BLASTDOOR_DEPLOY_STG=none", "BLASTDOOR_DEPLOY_PRD=none"} {
		if !strings.Contains(string(envFile), want) {
			t.Errorf("blastdoor.env missing %q:\n%s", want, envFile)
		}
	}

	applyFile, err := os.ReadFile(filepath.Join(outDir, "apply.gitlab-ci.yml"))
	if err != nil {
		t.Fatalf("reading apply.gitlab-ci.yml: %v", err)
	}
	if !strings.Contains(string(applyFile), "blastdoor:nothing-to-apply:") {
		t.Errorf("apply.gitlab-ci.yml has no placeholder job:\n%s", applyFile)
	}
	if strings.Contains(string(applyFile), "include:") {
		t.Errorf("apply.gitlab-ci.yml should not include the repository's apply file when nothing applies:\n%s", applyFile)
	}
}

// No unit carries an environment, no generated pipeline: a repository that
// has not arranged per-environment planning sees no new files, wish or no
// wish — a wish alone no longer turns this on; see TestDecide in
// internal/report for why.
func TestEvalWritesNoApplyPipelineWithoutAnyEnvironment(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, "plans")
	outDir := filepath.Join(dir, "out")
	planEnvTree(t, planDir, "ops/topics", "") // no --environment recorded

	policyDir := filepath.Join(dir, "policy")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "p.rego"), []byte("package blastdoor\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCmd()
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"eval", "--plan-dir", planDir, "--policy", policyDir, "--out-dir", outDir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("eval: %v", err)
	}

	if _, err := os.Stat(filepath.Join(outDir, "apply.gitlab-ci.yml")); !os.IsNotExist(err) {
		t.Error("apply.gitlab-ci.yml was written with no unit carrying an environment")
	}
}

// The point of this change: a repository that plans per environment gets an
// apply pipeline even with no wish stated at all, as long as policy's own
// allow rules vouch for the environment. Nobody has to state
// BLASTDOOR_DEPLOYMENT_METHOD_WISH for automation to happen any more.
func TestEvalWritesTheApplyPipelineWithoutAWishWhenPolicyVouches(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, "plans")
	outDir := filepath.Join(dir, "out")

	unitDir := filepath.Join(planDir, "ops/int/topics")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := `{"format_version":"1.2","resource_changes":[
		{"address":"kafka_topic.x","type":"kafka_topic","change":{"actions":["create"]}}
	]}`
	if err := os.WriteFile(filepath.Join(unitDir, "plan.json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, "environment.txt"), []byte("int\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	policyDir := filepath.Join(dir, "policy")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rego := `package blastdoor

allow contains {"resource": rc.address, "reason": "creating a topic", "deployment_method": {"int": "auto"}} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["create"]
}
`
	if err := os.WriteFile(filepath.Join(policyDir, "p.rego"), []byte(rego), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	cmd := NewRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"eval", "--plan-dir", planDir, "--policy", policyDir, "--out-dir", outDir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("eval: %v\n%s", err, out.String())
	}

	envFile, err := os.ReadFile(filepath.Join(outDir, "blastdoor.env"))
	if err != nil {
		t.Fatalf("reading blastdoor.env: %v", err)
	}
	if !strings.Contains(string(envFile), "BLASTDOOR_DEPLOY_INT=auto") {
		t.Errorf("blastdoor.env missing the int method:\n%s", envFile)
	}

	applyFile, err := os.ReadFile(filepath.Join(outDir, "apply.gitlab-ci.yml"))
	if err != nil {
		t.Fatalf("reading apply.gitlab-ci.yml: %v", err)
	}
	if !strings.Contains(string(applyFile), "apply:int:") {
		t.Errorf("apply.gitlab-ci.yml has no int job:\n%s", applyFile)
	}
	if !strings.Contains(string(applyFile), "when: on_success") {
		t.Errorf("apply.gitlab-ci.yml did not resolve int to an unattended apply:\n%s", applyFile)
	}
}
