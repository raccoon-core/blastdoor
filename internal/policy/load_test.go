package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowNull passes any null_resource, so a test can tell "the policy was
// loaded and ran" from "nothing was loaded".
const allowNull = `package blastdoor

allow contains {"resource": rc.address, "reason": "fine"} if {
	some rc in input.resource_changes
	rc.type == "null_resource"
}
`

// writeTree lays out a policy directory, creating parents as needed.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", p, err)
		}
	}
	return dir
}

// judgeIn evaluates one null_resource create against the policies at paths.
func judgeIn(t *testing.T, paths ...string) Change {
	t.Helper()
	e, err := New(context.Background(), Options{PolicyPaths: paths})
	if err != nil {
		t.Fatalf("compiling policies: %v", err)
	}
	res, err := e.Evaluate(context.Background(), planDoc(change("null_resource.x", "null_resource", "create")))
	if err != nil {
		t.Fatalf("evaluating: %v", err)
	}
	if len(res.Changes) != 1 {
		t.Fatalf("want 1 change, got %d", len(res.Changes))
	}
	return res.Changes[0]
}

// A policy repository is a tree, not a flat directory: rules are grouped into
// subdirectories and every one of them has to be loaded.
func TestLoadsRegoFromSubdirectories(t *testing.T) {
	dir := writeTree(t, map[string]string{"local/kafka/topics.rego": allowNull})

	if got := judgeIn(t, dir).Verdict; got != Pass {
		t.Fatalf("nested policy did not run: verdict %s, want %s", got, Pass)
	}
}

// Only .rego is a policy. The loader would otherwise take .json and .yaml as
// data documents, so a policy repository's own fixtures could break the gate.
func TestIgnoresEverythingButRego(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"kafka.rego":           allowNull,
		"testdata/broken.json": `{"not json`,
		"testdata/plan.yaml":   "a: [1,\n  b: :",
		"README.md":            "# policies",
		".git/HEAD":            "ref: refs/heads/main\n",
		"fixtures/topic.json":  `{"resource_changes":[]}`,
		"fixtures/values.yaml": "replicas: 2\n",
	})

	if got := judgeIn(t, dir).Verdict; got != Pass {
		t.Fatalf("policy did not run alongside non-rego files: verdict %s, want %s", got, Pass)
	}
}

// A data document must not be able to land on the namespace the rules live
// in: data that shadows a rule set is a way to silence policies with a file
// that never looks like a policy.
func TestDataDocumentCannotShadowRules(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"kafka.rego": allowNull,
		"data.json":  `{"blastdoor": {"allow": "hijacked"}}`,
	})

	if got := judgeIn(t, dir).Verdict; got != Pass {
		t.Fatalf("data document interfered with the rules: verdict %s, want %s", got, Pass)
	}
}

// A single .rego file is still a valid --policy argument.
func TestLoadsSingleRegoFile(t *testing.T) {
	dir := writeTree(t, map[string]string{"kafka.rego": allowNull})

	if got := judgeIn(t, filepath.Join(dir, "kafka.rego")).Verdict; got != Pass {
		t.Fatalf("single policy file did not run: verdict %s, want %s", got, Pass)
	}
}

// Policy paths that hold no policies are a mistake — a mistyped path, a wrong
// subdirectory. Left alone it reads as "every change denied", which looks like
// a verdict rather than an error.
func TestErrorsWhenPathsHoldNoRego(t *testing.T) {
	dir := writeTree(t, map[string]string{"README.md": "# no policies here"})

	_, err := New(context.Background(), Options{PolicyPaths: []string{dir}})
	if err == nil {
		t.Fatal("want an error for a policy path with no .rego files, got nil")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("error should name the path searched, got: %v", err)
	}
}

// A path that does not exist is worth its own message.
func TestErrorsWhenPolicyPathMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")

	if _, err := New(context.Background(), Options{PolicyPaths: []string{missing}}); err == nil {
		t.Fatal("want an error for a policy path that does not exist, got nil")
	}
}

// No --policy at all stays legal: every change is then unjudged, which the
// evaluator denies on its own.
func TestNoPolicyPathsIsNotAnError(t *testing.T) {
	if got := judgeIn(t).Verdict; got != Deny {
		t.Fatalf("verdict without policies: %s, want %s", got, Deny)
	}
}
