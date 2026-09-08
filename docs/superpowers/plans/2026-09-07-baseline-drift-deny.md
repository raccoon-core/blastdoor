# Baseline Drift Deny Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deny a merge request when the branch it targets still has infrastructure changes waiting to be applied, so an unapplied backlog cannot ride along inside someone else's approval.

**Architecture:** `blastdoor plan` gains `--baseline-ref`. For each unit whose own plan would apply something, it plans that unit a second time in a detached `git worktree` at the target branch tip and writes a per-unit `baseline.json` sidecar. `blastdoor eval --require-clean-baseline` reads the sidecars and worsens the report's verdict to `deny` for any unit whose baseline is dirty or missing. Everything is off unless the flags are passed.

**Tech Stack:** Go 1.27, cobra, OPA. No new dependencies — `go.mod` has two on purpose, and this adds none.

**Spec:** [docs/superpowers/specs/2026-09-07-baseline-drift-deny-design.md](../specs/2026-09-07-baseline-drift-deny-design.md)

## Global Constraints

- Go 1.27.0 per `go.mod`. If the machine has an older Go the toolchain downloads 1.27; **do not lower the `go` directive**.
- `make check` (fmt, vet, test) must pass before any task is called done.
- Tests run real things: real git repositories in `t.TempDir()`, real Rego compilation, `httptest` servers. Keep it that way. Every behaviour change needs a test that fails without it.
- No new third-party dependencies.
- Conventional Commits — `feat:` minor, `fix:` patch, `feat!:` major. Releases are generated from them. Do not tag by hand.
- Fail closed everywhere. A fact that cannot be read is never treated as a safe one.
- Comments in this repo are dense and explain *why*, not *what*. Match that register — but do not restate the spec at each site; one clear reason per decision.
- Do not modify `internal/policy/policy.go:495` or the existing `isNoOp` helper. `Evaluate` uses it and its meaning ("does nothing at all") is narrower than the new predicate on purpose.

---

### Task 1: The applicable-change predicate

Both "does this unit's own plan apply anything" and "is the baseline dirty" are the same question. One predicate answers it, in the package that already owns the plan-JSON shape.

**Files:**
- Modify: `internal/policy/policy.go` (add to the bottom, beside `isNoOp`/`actions`/`stringField`)
- Test: `internal/policy/policy_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `policy.ApplicableAddresses(raw []byte) ([]string, error)` — sorted addresses of the resource changes that would do something. Empty slice and `nil` error means the plan applies nothing. Used by Tasks 5 and 6.

- [ ] **Step 1: Write the failing tests**

Add to `internal/policy/policy_test.go`:

```go
func TestApplicableAddresses(t *testing.T) {
	tests := []struct {
		name string
		plan string
		want []string
	}{
		{
			name: "a create applies something",
			plan: `{"format_version":"1.2","resource_changes":[
				{"address":"kafka_topic.a","mode":"managed","type":"kafka_topic",
				 "change":{"actions":["create"]}}]}`,
			want: []string{"kafka_topic.a"},
		},
		{
			name: "a no-op applies nothing",
			plan: `{"format_version":"1.2","resource_changes":[
				{"address":"kafka_topic.a","mode":"managed","type":"kafka_topic",
				 "change":{"actions":["no-op"]}}]}`,
			want: nil,
		},
		{
			name: "reading a data source applies nothing",
			plan: `{"format_version":"1.2","resource_changes":[
				{"address":"data.vault_generic_secret.a","mode":"data","type":"vault_generic_secret",
				 "change":{"actions":["read"]}}]}`,
			want: nil,
		},
		{
			// The distinction examples/plans/managed-resource-read-lookalike.json
			// exists to defend. A managed resource being read is not a data
			// lookup, and a unit whose only pending change is one must not skip
			// its baseline.
			name: "reading a managed resource applies something",
			plan: `{"format_version":"1.2","resource_changes":[
				{"address":"kafka_topic.sneaky","mode":"managed","type":"kafka_topic",
				 "change":{"actions":["read"]}}]}`,
			want: []string{"kafka_topic.sneaky"},
		},
		{
			name: "a replace applies something",
			plan: `{"format_version":"1.2","resource_changes":[
				{"address":"kafka_topic.a","mode":"managed","type":"kafka_topic",
				 "change":{"actions":["delete","create"]}}]}`,
			want: []string{"kafka_topic.a"},
		},
		{
			name: "an unrecognised action applies something",
			plan: `{"format_version":"1.2","resource_changes":[
				{"address":"kafka_topic.a","mode":"managed","type":"kafka_topic",
				 "change":{"actions":["forget"]}}]}`,
			want: []string{"kafka_topic.a"},
		},
		{
			name: "addresses come back sorted",
			plan: `{"format_version":"1.2","resource_changes":[
				{"address":"kafka_topic.z","mode":"managed","type":"kafka_topic",
				 "change":{"actions":["create"]}},
				{"address":"kafka_topic.a","mode":"managed","type":"kafka_topic",
				 "change":{"actions":["update"]}}]}`,
			want: []string{"kafka_topic.a", "kafka_topic.z"},
		},
		{
			name: "an empty plan applies nothing",
			plan: `{"format_version":"1.2","resource_changes":[]}`,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ApplicableAddresses([]byte(tt.plan))
			if err != nil {
				t.Fatalf("ApplicableAddresses: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplicableAddressesRejectsNonPlans(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"truncated JSON", `{"resource_changes":[`},
		{"state output", `{"values":{"root_module":{}}}`},
		{"an error message", `not json at all`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Failing to read a plan must never come back looking like a plan
			// that applies nothing — that is a clean baseline for free.
			if _, err := ApplicableAddresses([]byte(tt.raw)); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
}
```

Add `"slices"` to that file's imports if it is not already there.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/policy/ -run ApplicableAddresses -v`
Expected: FAIL, `undefined: ApplicableAddresses`.

- [ ] **Step 3: Implement the predicate**

Append to `internal/policy/policy.go`:

```go
// ApplicableAddresses lists the addresses of the resource changes in a plan
// that would actually do something if applied, sorted.
//
// An empty result means the plan applies nothing, which is what "this unit has
// no changes" means everywhere it is asked. Reading the plan is part of the
// answer: a truncated file or an error message must never come back as a plan
// with nothing in it, so the same ValidatePlan that guards eval guards this.
func ApplicableAddresses(raw []byte) ([]string, error) {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("parsing plan JSON: %w", err)
	}
	if err := ValidatePlan(decoded); err != nil {
		return nil, err
	}

	var out []string
	for _, rc := range resourceChanges(decoded) {
		if isApplicable(rc) {
			out = append(out, stringField(rc, "address"))
		}
	}
	sort.Strings(out)
	return out, nil
}

// isApplicable reports whether one resource change would do anything.
//
// Two actions do nothing. ["no-op"] is nothing by definition. ["read"] is
// nothing too — but only on a data source: the same action on a managed
// resource is not a data lookup, and the repository already treats those
// differently on purpose (examples/plans/managed-resource-read-lookalike.json
// gets review where examples/plans/data-source-read.json passes). Filtering
// every read here would let a unit whose only pending change is a managed
// read skip its baseline check entirely.
//
// Everything else is applicable, including an action this does not recognise.
// A new Terraform action must read as "something happens", not as silence.
func isApplicable(rc map[string]any) bool {
	acts := actions(rc)
	if len(acts) != 1 {
		return len(acts) > 0
	}
	switch acts[0] {
	case "no-op":
		return false
	case "read":
		return stringField(rc, "mode") != "data"
	}
	return true
}
```

`encoding/json`, `fmt` and `sort` are already imported by this file. Leave `isNoOp` exactly as it is — `Evaluate` uses it at line 495 and its narrower meaning is deliberate.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/policy/ -run ApplicableAddresses -v`
Expected: PASS, every subtest.

- [ ] **Step 5: Run the full check**

Run: `make check`
Expected: PASS. Nothing else changed behaviour, so `examples_test.go` must still be green.

- [ ] **Step 6: Commit**

```bash
git add internal/policy/policy.go internal/policy/policy_test.go
git commit -m "feat(policy): add ApplicableAddresses for deciding a plan is empty"
```

---

### Task 2: The baseline sidecar

Where the result of a baseline plan is written and read back. Same per-unit shape as `engine.txt` and `environment.txt`, and for the same reason: `eval` runs in a different job, and plans split across a `parallel:matrix` have their artifacts merged. One file per unit merges; one file per run collides.

**Files:**
- Create: `internal/baseline/baseline.go`
- Test: `internal/baseline/baseline_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `baseline.State` (`Clean`/`Dirty`/`Absent`), `baseline.Result{Ref, Commit, State, Addresses}`, `baseline.FileName`, `baseline.Write(dir string, r Result) error`, `baseline.Read(dir string) (Result, bool, error)`. Used by Tasks 5 and 6.

- [ ] **Step 1: Write the failing tests**

Create `internal/baseline/baseline_test.go`:

```go
package baseline

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWriteThenRead(t *testing.T) {
	dir := t.TempDir()
	want := Result{
		Ref:       "origin/main",
		Commit:    "2907a6899eda3dfb5a06647963bf5b5759eca6e6",
		State:     Dirty,
		Addresses: []string{`kafka_topic.topics["scp.example.v1"]`},
	}
	if err := Write(dir, want); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, found, err := Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !found {
		t.Fatal("want found, got not found")
	}
	if got.Ref != want.Ref || got.Commit != want.Commit || got.State != want.State {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if !slices.Equal(got.Addresses, want.Addresses) {
		t.Errorf("addresses: got %v, want %v", got.Addresses, want.Addresses)
	}
}

func TestReadMissingIsNotAnError(t *testing.T) {
	// A missing sidecar is a fact the caller has to weigh, not a broken run:
	// a plan handed straight to --plan has no baseline, and neither does one
	// from a blastdoor old enough not to write them. eval is what decides that
	// a unit with changes and no baseline denies.
	_, found, err := Read(t.TempDir())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if found {
		t.Fatal("want not found, got found")
	}
}

func TestReadRejectsMalformed(t *testing.T) {
	tests := map[string]string{
		"truncated":     `{"state":`,
		"unknown state": `{"ref":"origin/main","commit":"abc","state":"probably-fine"}`,
		"empty state":   `{"ref":"origin/main","commit":"abc"}`,
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			// A sidecar that cannot be understood is not a clean one.
			if _, _, err := Read(dir); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/baseline/ -v`
Expected: FAIL to build — the package does not exist yet.

- [ ] **Step 3: Implement the sidecar**

Create `internal/baseline/baseline.go`:

```go
// Package baseline records what the branch a merge request targets still has
// waiting to be applied, per unit.
//
// A plan is desired state against live state, not "what this merge request
// changed", so anything already pending on a unit turns up in the plan of the
// next merge request that touches it — and in the apply that follows its
// approval. This package holds the second plan that makes the difference
// visible.
package baseline

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileName is the sidecar's name, written beside the unit's plan.json.
//
// Per unit rather than once per run, for the same reason engine.txt and
// environment.txt are: eval runs in another job, and when plans are split
// across a parallel matrix their artifacts are merged. One file per unit
// merges; one file per run collides, and the survivor is whichever leg
// finished last.
const FileName = "baseline.json"

// State is what the baseline plan found for one unit.
type State string

const (
	// Clean means the baseline plan applies nothing.
	Clean State = "clean"
	// Dirty means the baseline still has changes waiting to be applied.
	Dirty State = "dirty"
	// Absent means the unit does not exist at the baseline at all, which is
	// what a unit this merge request creates looks like. Nothing can be
	// pending on a unit that is not there, so this is clean, not an error.
	Absent State = "absent"
)

// Result is one unit's baseline.
type Result struct {
	// Ref is the ref that was planned, as it was asked for.
	Ref string `json:"ref"`
	// Commit is what that ref resolved to. A ref moves; without the commit a
	// verdict cannot be explained afterwards. Same reasoning as report.Layer.
	Commit string `json:"commit"`
	State  State  `json:"state"`
	// Addresses are the pending changes, when State is Dirty.
	Addresses []string `json:"addresses,omitempty"`
}

// Write records a unit's baseline beside its plan.
func Write(dir string, r Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding baseline for %s: %w", dir, err)
	}
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// Read reads back a unit's baseline. The bool reports whether one was recorded.
//
// A missing file is not an error — it is a fact for the caller to weigh, and
// eval is where it becomes a denial. A file that is present but cannot be
// understood *is* an error: a sidecar nobody can read must never come out
// looking like a clean one.
func Read(dir string) (Result, bool, error) {
	path := filepath.Join(dir, FileName)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, fmt.Errorf("reading %s: %w", path, err)
	}

	var r Result
	if err := json.Unmarshal(raw, &r); err != nil {
		return Result{}, false, fmt.Errorf("parsing %s: %w", path, err)
	}
	switch r.State {
	case Clean, Dirty, Absent:
		return r, true, nil
	default:
		return Result{}, false, fmt.Errorf("%s: state is %q, want one of clean, dirty or absent", path, r.State)
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/baseline/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/baseline/baseline.go internal/baseline/baseline_test.go
git commit -m "feat(baseline): record a unit's baseline beside its plan"
```

---

### Task 3: The baseline worktree

A detached checkout of the target branch tip, so the same unit can be planned twice from one job without disturbing the working tree.

**Files:**
- Create: `internal/baseline/worktree.go`
- Test: `internal/baseline/worktree_test.go`

**Interfaces:**
- Consumes: nothing from Task 2 (same package, no shared symbols).
- Produces: `baseline.NewWorktree(ctx context.Context, repoDir, ref string) (*Worktree, error)`, and on `*Worktree`: `Dir() string`, `Ref() string`, `Commit() string`, `UnitDir(unit string) (string, bool)`, `Close() error`. Used by Task 5.

- [ ] **Step 1: Write the failing tests**

Create `internal/baseline/worktree_test.go`:

```go
package baseline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoWithBaselineAndHead builds a real repository whose main branch holds one
// unit, and whose feature branch adds a second. It returns the repo directory
// and main's commit.
func repoWithBaselineAndHead(t *testing.T) (repoDir, mainCommit string) {
	t.Helper()
	repoDir = t.TempDir()

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(repoDir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")

	write("units/old/main.tf", "resource \"null_resource\" \"a\" {}\n")
	run("add", ".")
	run("commit", "-qm", "baseline")
	mainCommit = run("rev-parse", "HEAD")

	run("checkout", "-q", "-b", "feature")
	write("units/new/main.tf", "resource \"null_resource\" \"b\" {}\n")
	run("add", ".")
	run("commit", "-qm", "head")

	return repoDir, mainCommit
}

func TestWorktreeResolvesRefToCommit(t *testing.T) {
	repoDir, mainCommit := repoWithBaselineAndHead(t)

	wt, err := NewWorktree(context.Background(), repoDir, "main")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Close()

	// The ref is what was asked for; the commit is what makes the verdict
	// explainable later, once the ref has moved.
	if wt.Ref() != "main" {
		t.Errorf("Ref() = %q, want main", wt.Ref())
	}
	if wt.Commit() != mainCommit {
		t.Errorf("Commit() = %q, want %q", wt.Commit(), mainCommit)
	}
}

func TestWorktreeUnitDirReportsAbsentUnits(t *testing.T) {
	repoDir, _ := repoWithBaselineAndHead(t)

	wt, err := NewWorktree(context.Background(), repoDir, "main")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer wt.Close()

	if _, ok := wt.UnitDir("units/old"); !ok {
		t.Error("units/old exists on main but UnitDir says it does not")
	}
	// A unit the merge request creates is not at the baseline. That is clean,
	// not an error.
	if _, ok := wt.UnitDir("units/new"); ok {
		t.Error("units/new exists only on the feature branch but UnitDir found it")
	}
}

func TestWorktreeCloseRemovesTheCheckout(t *testing.T) {
	repoDir, _ := repoWithBaselineAndHead(t)

	wt, err := NewWorktree(context.Background(), repoDir, "main")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	dir := wt.Dir()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("worktree not on disk: %v", err)
	}

	if err := wt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("worktree still on disk after Close: %v", err)
	}
}

func TestWorktreeUnknownRefFails(t *testing.T) {
	repoDir, _ := repoWithBaselineAndHead(t)

	// A ref that is not there is the shallow-clone case, and it must fail the
	// command rather than resolve to nothing and look like a clean baseline.
	_, err := NewWorktree(context.Background(), repoDir, "origin/nope")
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if !strings.Contains(err.Error(), "origin/nope") {
		t.Errorf("error should name the ref, got %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/baseline/ -run Worktree -v`
Expected: FAIL, `undefined: NewWorktree`.

- [ ] **Step 3: Implement the worktree**

Create `internal/baseline/worktree.go`:

```go
package baseline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree is a detached checkout of the baseline ref.
//
// A second checkout rather than a stash or a checkout in place: the head plan
// has already run against the working tree, the artifacts are there, and a
// command that moves a CI checkout out from under itself is a command that
// leaves a wrecked workspace behind when it fails halfway.
type Worktree struct {
	repoDir string
	parent  string // the temporary directory holding the checkout
	dir     string
	ref     string
	commit  string
}

// NewWorktree resolves ref to a commit and checks it out, detached.
//
// The ref must exist. A shallow clone has no origin/<default branch>, and
// resolving to nothing there would produce an empty baseline plan — a clean
// baseline for free, gating nothing while looking green. Fail instead, and say
// what to set.
func NewWorktree(ctx context.Context, repoDir, ref string) (*Worktree, error) {
	commit, err := gitOutput(ctx, repoDir, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf(
			"resolving baseline ref %q: %w. On a shallow clone the target branch is not fetched — set GIT_DEPTH: 0",
			ref, err)
	}

	parent, err := os.MkdirTemp("", "blastdoor-baseline-")
	if err != nil {
		return nil, fmt.Errorf("creating a directory for the baseline checkout: %w", err)
	}
	// git worktree add insists on creating the directory itself.
	dir := filepath.Join(parent, "tree")

	if _, err := gitOutput(ctx, repoDir, "worktree", "add", "--detach", dir, commit); err != nil {
		os.RemoveAll(parent)
		return nil, fmt.Errorf("checking out %s (%.7s) as a baseline: %w", ref, commit, err)
	}

	return &Worktree{repoDir: repoDir, parent: parent, dir: dir, ref: ref, commit: commit}, nil
}

// Dir is where the baseline is checked out.
func (w *Worktree) Dir() string { return w.dir }

// Ref is the ref that was asked for.
func (w *Worktree) Ref() string { return w.ref }

// Commit is what that ref resolved to.
func (w *Worktree) Commit() string { return w.commit }

// UnitDir gives a unit's directory inside the baseline, and whether the unit
// is there at all.
//
// Not there is the ordinary case for a unit the merge request creates, so the
// caller records Absent rather than treating it as a failure.
func (w *Worktree) UnitDir(unit string) (string, bool) {
	dir := filepath.Join(w.dir, filepath.FromSlash(unit))
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}

// Close removes the checkout and the git metadata pointing at it.
//
// Both, and in that order: 'worktree remove' is what unregisters it from the
// repository, and a stale registration outlives the job in a cached CI
// workspace and makes the next run's 'worktree add' fail on a path that is no
// longer there.
func (w *Worktree) Close() error {
	_, gitErr := gitOutput(context.Background(), w.repoDir, "worktree", "remove", "--force", w.dir)
	rmErr := os.RemoveAll(w.parent)
	if gitErr != nil {
		return fmt.Errorf("removing the baseline checkout: %w", gitErr)
	}
	if rmErr != nil {
		return fmt.Errorf("removing %s: %w", w.parent, rmErr)
	}
	return nil
}

// gitOutput runs a git command in repoDir and returns its trimmed stdout.
//
// Stderr is folded into the error: git says why on stderr, and an exit status
// on its own tells a reader nothing.
func gitOutput(ctx context.Context, repoDir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/baseline/ -v`
Expected: PASS, all of Task 2's and Task 3's tests.

- [ ] **Step 5: Commit**

```bash
git add internal/baseline/worktree.go internal/baseline/worktree_test.go
git commit -m "feat(baseline): check out the baseline ref in a detached worktree"
```

---

### Task 4: The denial and the note

Where a dirty baseline becomes a verdict, in the same shape as the two checks that already force review.

**Files:**
- Modify: `internal/report/report.go` (add `BaselineUnit`; add `Baseline` to `Report`; add `RequireCleanBaseline` after `RequireCoverage` at line 191; add a block to `WriteMarkdown` after the `Uncovered` block at line 240; fix `verdictSentence` at line 389)
- Test: `internal/report/report_test.go`

**Interfaces:**
- Consumes: `policy.Worse`, `policy.Deny` (already imported).
- Produces: `report.BaselineUnit{Path, Ref, Commit string; Addresses []string; Missing bool}`, `report.Report.Baseline []BaselineUnit`, `(*report.Report).RequireCleanBaseline(dirty []BaselineUnit)`. Used by Task 6.

- [ ] **Step 1: Write the failing tests**

Add to `internal/report/report_test.go`:

```go
func TestRequireCleanBaselineDenies(t *testing.T) {
	rep := Build([]Unit{{
		Path:    "terraform/prd",
		Changes: []policy.Change{{Address: "kafka_topic.new", Verdict: policy.Pass}},
	}})
	if rep.Verdict != policy.Pass {
		t.Fatalf("setup: verdict is %s, want pass", rep.Verdict)
	}

	rep.RequireCleanBaseline([]BaselineUnit{{
		Path:      "terraform/prd",
		Ref:       "origin/main",
		Commit:    "2907a6899eda3dfb5a06647963bf5b5759eca6e6",
		Addresses: []string{`kafka_topic.topics["scp.example.v1"]`},
	}})

	// Deny, not review: approving does not apply the backlog, so approving
	// cannot be what settles it.
	if rep.Verdict != policy.Deny {
		t.Errorf("verdict = %s, want deny", rep.Verdict)
	}
	if len(rep.Baseline) != 1 {
		t.Fatalf("Baseline has %d entries, want 1", len(rep.Baseline))
	}
}

func TestRequireCleanBaselineWithNothingDirtyChangesNothing(t *testing.T) {
	rep := Build([]Unit{{
		Path:    "terraform/int",
		Changes: []policy.Change{{Address: "kafka_topic.new", Verdict: policy.Pass}},
	}})

	rep.RequireCleanBaseline(nil)

	if rep.Verdict != policy.Pass {
		t.Errorf("verdict = %s, want pass", rep.Verdict)
	}
	if len(rep.Baseline) != 0 {
		t.Errorf("Baseline has %d entries, want 0", len(rep.Baseline))
	}
}

func TestRequireCleanBaselineNeverSoftens(t *testing.T) {
	rep := Report{Verdict: policy.Deny}
	rep.RequireCleanBaseline([]BaselineUnit{{Path: "terraform/prd", Missing: true}})
	if rep.Verdict != policy.Deny {
		t.Errorf("verdict = %s, want deny", rep.Verdict)
	}
}

func TestMarkdownNamesTheBaselineBacklog(t *testing.T) {
	rep := Build([]Unit{{
		Path:    "terraform/prd",
		Changes: []policy.Change{{Address: "kafka_topic.new", Verdict: policy.Pass}},
	}})
	rep.RequireCleanBaseline([]BaselineUnit{{
		Path:      "terraform/prd",
		Ref:       "origin/main",
		Commit:    "2907a6899eda3dfb5a06647963bf5b5759eca6e6",
		Addresses: []string{`kafka_topic.topics["scp.example.v1"]`},
	}})

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	got := b.String()

	for _, want := range []string{
		"have not been applied",
		"terraform/prd",
		`kafka_topic.topics["scp.example.v1"]`,
		"2907a68",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary does not mention %q:\n%s", want, got)
		}
	}
}

func TestMarkdownSaysWhenABaselineIsMissing(t *testing.T) {
	rep := Build([]Unit{{
		Path:    "terraform/prd",
		Changes: []policy.Change{{Address: "kafka_topic.new", Verdict: policy.Pass}},
	}})
	rep.RequireCleanBaseline([]BaselineUnit{{Path: "terraform/prd", Missing: true}})

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	if !strings.Contains(b.String(), "no baseline") {
		t.Errorf("summary does not say the baseline is missing:\n%s", b.String())
	}
}

func TestDenyHeadlineWithNoDeniedChanges(t *testing.T) {
	// The deny comes from the baseline, not from a scored change. "0 change(s)
	// a policy does not allow" reads as nothing being wrong, directly above the
	// list of what is wrong. Review already has this special case; deny needs
	// its counterpart.
	rep := Build([]Unit{{
		Path:    "terraform/prd",
		Changes: []policy.Change{{Address: "kafka_topic.new", Verdict: policy.Pass}},
	}})
	rep.RequireCleanBaseline([]BaselineUnit{{Path: "terraform/prd", Missing: true}})

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	if strings.Contains(b.String(), "0 change(s)") {
		t.Errorf("headline claims 0 change(s):\n%s", b.String())
	}
	if !strings.Contains(b.String(), "Denied") {
		t.Errorf("headline does not say Denied:\n%s", b.String())
	}
}
```

Ensure `strings` and `policy` are imported in that test file (they already are for the existing tests).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/report/ -run 'Baseline|DenyHeadline' -v`
Expected: FAIL, `undefined: BaselineUnit` / `rep.RequireCleanBaseline undefined`.

- [ ] **Step 3: Add the type and the field**

In `internal/report/report.go`, add after the `Layer` struct:

```go
// BaselineUnit is one unit whose target branch still has changes waiting to be
// applied.
type BaselineUnit struct {
	Path string `json:"path"`
	// Ref and Commit are the baseline that was planned. The commit matters:
	// a ref moves, and a verdict cannot be explained afterwards without it.
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
	// Addresses are what is pending there.
	Addresses []string `json:"addresses,omitempty"`
	// Missing says the unit has changes of its own but recorded no baseline at
	// all. Not the same fact as a dirty baseline, and it denies for a different
	// reason: nobody knows what is pending, which is not the same as nothing.
	Missing bool `json:"missing,omitempty"`
}
```

Add to the `Report` struct, after `Uncovered`:

```go
	// Baseline lists units whose target branch still has changes waiting to be
	// applied. Such a change rides along inside this merge request's plan and
	// its apply, having appeared in nobody's diff.
	Baseline []BaselineUnit `json:"baseline,omitempty"`
```

- [ ] **Step 4: Add RequireCleanBaseline**

In `internal/report/report.go`, after `RequireCoverage`:

```go
// RequireCleanBaseline denies, recording the units whose target branch still
// has changes waiting to be applied.
//
// Deny rather than review, and the asymmetry is the whole point. A reviewer
// approving this merge request does nothing about a change that merged days ago
// and was never applied — the plan has to change, by applying or reverting on
// the target branch. That is what deny means here and in gate: approving alone
// does not settle it.
//
// Like RequireReview and RequireCoverage, it never softens a verdict.
func (r *Report) RequireCleanBaseline(dirty []BaselineUnit) {
	if len(dirty) == 0 {
		return
	}
	r.Baseline = append(r.Baseline, dirty...)
	sort.Slice(r.Baseline, func(i, j int) bool { return r.Baseline[i].Path < r.Baseline[j].Path })
	r.Verdict = policy.Worse(r.Verdict, policy.Deny)
}
```

- [ ] **Step 5: Add the note section**

In `WriteMarkdown`, after the `Uncovered` block and before the `switch`:

```go
	if len(r.Baseline) > 0 {
		b.WriteString("\nThe branch this targets has changes that have not been applied yet. " +
			"They are in this plan but in nobody's diff, so approving this would apply them too. " +
			"Apply or revert them on the target branch first:\n\n")
		for _, u := range r.Baseline {
			if u.Missing {
				b.WriteString(fmt.Sprintf("- `%s` — no baseline was recorded, so what is pending there is unknown\n",
					escapePipes(u.Path)))
				continue
			}
			addresses := make([]string, 0, len(u.Addresses))
			for _, a := range u.Addresses {
				addresses = append(addresses, "`"+escapePipes(a)+"`")
			}
			b.WriteString(fmt.Sprintf("- `%s` — %s (at `%.7s`)\n",
				escapePipes(u.Path), strings.Join(addresses, ", "), u.Commit))
		}
	}
```

- [ ] **Step 6: Fix the deny headline**

In `verdictSentence`, replace the `policy.Deny` case:

```go
	case policy.Deny:
		// A deny can be forced by something other than a scored change — a
		// baseline still waiting to be applied. Counting changes then reports
		// "0 change(s) a policy does not allow", which reads as nothing being
		// wrong, directly above the list of what is. Same reason Review carries
		// its own version of this below.
		if deny == 0 {
			return "**Denied** — this cannot merge as it stands. Approving does not clear it.\n"
		}
		return fmt.Sprintf("**Denied** — %d change(s) a policy does not allow. Approving does not clear this.\n", deny)
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/report/ -v`
Expected: PASS, including the pre-existing tests.

- [ ] **Step 8: Run the full check**

Run: `make check`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/report/report.go internal/report/report_test.go
git commit -m "feat(report): deny when the target branch has an unapplied baseline"
```

---

### Task 5: Planning the baseline

Wiring the worktree and the sidecar into `blastdoor plan`.

**Files:**
- Modify: `internal/cli/plan.go` (add the flag; add the orchestration inside `RunE` around the unit loop at lines 72-101)
- Test: `internal/cli/plan_test.go` (create if absent)

**Interfaces:**
- Consumes: `baseline.NewWorktree`, `baseline.Write`, `baseline.Result`, `baseline.Clean/Dirty/Absent` (Tasks 2-3); `policy.ApplicableAddresses` (Task 1); `runner.Plan`, `runner.Options` (existing).
- Produces: the `--baseline-ref` flag, and `<out-dir>/<unit>/baseline.json` for Task 6 to read.

- [ ] **Step 1: Write the failing test**

The full command shells out to terraform, which the test suite must not need. Test the decision function directly instead — it is where all the logic is. Create `internal/cli/plan_test.go`:

```go
package cli

import (
	"path/filepath"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/baseline"
)

const planWithACreate = `{"format_version":"1.2","resource_changes":[
	{"address":"kafka_topic.a","mode":"managed","type":"kafka_topic",
	 "change":{"actions":["create"]}}]}`

const planWithNothingToDo = `{"format_version":"1.2","resource_changes":[
	{"address":"kafka_topic.a","mode":"managed","type":"kafka_topic",
	 "change":{"actions":["no-op"]}}]}`

func TestBaselineSkippedWhenTheHeadPlanAppliesNothing(t *testing.T) {
	dest := t.TempDir()

	// The revert case: a merge request that clears the backlog has nothing of
	// its own to apply, so it needs no baseline and is not denied by one.
	planned, err := recordBaseline(t.Context(), nil, "units/a", []byte(planWithNothingToDo), dest, planFn(t, "unused"))
	if err != nil {
		t.Fatalf("recordBaseline: %v", err)
	}
	if planned {
		t.Error("planned a baseline for a unit that applies nothing")
	}
	if _, found, _ := baseline.Read(dest); found {
		t.Error("wrote a baseline sidecar for a unit that applies nothing")
	}
}

func TestBaselineRecordsDirtyWhenTheBaselineHasChanges(t *testing.T) {
	dest := t.TempDir()

	planned, err := recordBaseline(t.Context(), stubWorktree(t), "units/a", []byte(planWithACreate), dest, planFn(t, planWithACreate))
	if err != nil {
		t.Fatalf("recordBaseline: %v", err)
	}
	if !planned {
		t.Fatal("did not plan a baseline for a unit that applies something")
	}

	got, found, err := baseline.Read(dest)
	if err != nil || !found {
		t.Fatalf("Read: %v, found=%v", err, found)
	}
	if got.State != baseline.Dirty {
		t.Errorf("state = %q, want dirty", got.State)
	}
	if len(got.Addresses) != 1 || got.Addresses[0] != "kafka_topic.a" {
		t.Errorf("addresses = %v, want [kafka_topic.a]", got.Addresses)
	}
}

func TestBaselineRecordsCleanWhenTheBaselineHasNothing(t *testing.T) {
	dest := t.TempDir()

	if _, err := recordBaseline(t.Context(), stubWorktree(t), "units/a", []byte(planWithACreate), dest, planFn(t, planWithNothingToDo)); err != nil {
		t.Fatalf("recordBaseline: %v", err)
	}

	got, _, err := baseline.Read(dest)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.State != baseline.Clean {
		t.Errorf("state = %q, want clean", got.State)
	}
}

func TestBaselineRecordsAbsentForANewUnit(t *testing.T) {
	dest := t.TempDir()

	// A unit this merge request creates is not at the baseline. Nothing can be
	// pending on a unit that does not exist, so absent is clean.
	wt := stubWorktree(t)
	wt.units = nil

	if _, err := recordBaseline(t.Context(), wt, "units/a", []byte(planWithACreate), dest, planFn(t, "must not be called")); err != nil {
		t.Fatalf("recordBaseline: %v", err)
	}

	got, _, err := baseline.Read(dest)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.State != baseline.Absent {
		t.Errorf("state = %q, want absent", got.State)
	}
}

func TestBaselineFailsOnAnUnreadablePlan(t *testing.T) {
	dest := t.TempDir()

	// A plan that cannot be read must not come back as a clean baseline.
	if _, err := recordBaseline(t.Context(), stubWorktree(t), "units/a", []byte(planWithACreate), dest, planFn(t, `{"truncated":`)); err == nil {
		t.Fatal("want an error, got nil")
	}
}

// --- helpers ---

// stubWorktree is a baselineTree that reports one unit present and never
// touches git.
func stubWorktree(t *testing.T) *fakeTree {
	t.Helper()
	return &fakeTree{units: map[string]string{"units/a": filepath.Join(t.TempDir(), "units", "a")}}
}

type fakeTree struct {
	units map[string]string
}

func (f *fakeTree) Ref() string    { return "origin/main" }
func (f *fakeTree) Commit() string { return "2907a6899eda3dfb5a06647963bf5b5759eca6e6" }
func (f *fakeTree) UnitDir(unit string) (string, bool) {
	dir, ok := f.units[unit]
	return dir, ok
}

// planFn returns a planner that yields the given JSON, and fails the test if
// it is called when it should not have been.
func planFn(t *testing.T, out string) planner {
	t.Helper()
	return func(_ context.Context, _ string) ([]byte, error) {
		if out == "must not be called" || out == "unused" {
			t.Fatalf("planner called when it should not have been")
		}
		return []byte(out), nil
	}
}
```

Add `"context"` to the test file's imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli/ -run Baseline -v`
Expected: FAIL, `undefined: recordBaseline`, `undefined: planner`.

- [ ] **Step 3: Implement the decision function**

Add to `internal/cli/plan.go`:

```go
// baselineTree is the part of a baseline worktree this file needs.
//
// An interface so the decision below can be tested without a git checkout and
// without shelling out to terraform. baseline.Worktree satisfies it.
type baselineTree interface {
	Ref() string
	Commit() string
	UnitDir(unit string) (string, bool)
}

// planner produces plan JSON for a directory.
type planner func(ctx context.Context, dir string) ([]byte, error)

// recordBaseline plans one unit at the baseline and writes what it found
// beside the unit's own plan. It reports whether it planned anything.
//
// The skip is the load-bearing part. A unit whose own plan applies nothing has
// nothing for a backlog to ride along with, so it needs no baseline — and that
// is also what lets a merge request which clears the backlog through, without
// an override anybody has to be trusted with.
func recordBaseline(ctx context.Context, tree baselineTree, unit string, headJSON []byte, dest string, plan planner) (bool, error) {
	head, err := policy.ApplicableAddresses(headJSON)
	if err != nil {
		return false, fmt.Errorf("%s: %w", unit, err)
	}
	if len(head) == 0 {
		return false, nil
	}

	unitDir, ok := tree.UnitDir(unit)
	if !ok {
		// A unit this change creates. Nothing can be pending on it.
		return false, baseline.Write(dest, baseline.Result{
			Ref: tree.Ref(), Commit: tree.Commit(), State: baseline.Absent,
		})
	}

	raw, err := plan(ctx, unitDir)
	if err != nil {
		return true, fmt.Errorf("planning %s at %s: %w", unit, tree.Ref(), err)
	}
	addresses, err := policy.ApplicableAddresses(raw)
	if err != nil {
		return true, fmt.Errorf("%s at %s: %w", unit, tree.Ref(), err)
	}

	state := baseline.Clean
	if len(addresses) > 0 {
		state = baseline.Dirty
	}
	return true, baseline.Write(dest, baseline.Result{
		Ref: tree.Ref(), Commit: tree.Commit(), State: state, Addresses: addresses,
	})
}
```

Add `"context"`, and the `baseline` and `policy` package imports.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/cli/ -run Baseline -v`
Expected: PASS.

- [ ] **Step 5: Wire it into the command**

In `newPlanCmd`, add `baselineRef` to the `var` block, and this flag:

```go
	cmd.Flags().StringVar(&baselineRef, "baseline-ref", "",
		"git ref to plan each changed unit against as well, to detect changes already waiting to be applied there (default: off)")
```

Inside `RunE`, after the `len(resolved) == 0` early return and after `opts` is built:

```go
			// The tip of the branch this change targets, not the merge base —
			// and this is the one place that disagrees with
			// detect.ResolveBaseRef on purpose. Three-dot merge-base is right
			// for "which units does this branch touch". It is wrong here: a
			// branch that is behind its target has a merge base predating the
			// backlog, so the baseline would come back clean while the backlog
			// is still waiting. The question here is "what is pending if this
			// change does not exist", and the tip is what answers it.
			var tree *baseline.Worktree
			if baselineRef != "" {
				var err error
				if tree, err = baseline.NewWorktree(cmd.Context(), "", baselineRef); err != nil {
					return err
				}
				defer func() {
					if err := tree.Close(); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "%v\n", err)
					}
				}()
			}
```

and inside the unit loop, after `writeEnvironmentFile(dest, environment)`:

```go
				if tree != nil {
					planned, err := recordBaseline(cmd.Context(), tree, unit, res.JSON, dest,
						func(ctx context.Context, dir string) ([]byte, error) {
							r, err := runner.Plan(ctx, dir, opts)
							return r.JSON, err
						})
					if err != nil {
						return err
					}
					if planned {
						fmt.Fprintf(cmd.ErrOrStderr(), "=== planned %s at %s ===\n", unit, tree.Ref())
					}
				}
```

`runner.Plan` uses `os.Setenv` for Terragrunt and is documented as unsafe to run concurrently. This runs it sequentially, one unit at a time, same as the head plans — do not parallelise either.

Two notes on what is deliberately *not* tested here:

**The off path.** `--baseline-ref` unset leaves `tree` nil and nothing runs. There is no test for it because exercising the command end to end means shelling out to terraform, and tests here must not need external tools on `PATH`. The guard is a single `if tree != nil`; keep it that way so it stays true by inspection.

**The spec's open question, now decided.** Do **not** add a check refusing a `--baseline-ref` that resolves to the same commit as `HEAD`, the way `ChangedFiles` refuses a base ref equal to head. The two cases are not alike: an equal base ref always means a misconfigured diff, whereas a merge request whose branch is exactly its target's tip is an ordinary empty branch, and on the default branch it is the normal state. A check would have to tell those apart, and the template already handles the one case that matters by never passing the flag on the default branch. If the ref resolves to HEAD, every unit's baseline simply comes back matching its head plan — which denies loudly rather than passing quietly, so the failure mode is already the safe one.

- [ ] **Step 6: Run the full check**

Run: `make check`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/plan.go internal/cli/plan_test.go
git commit -m "feat(plan): add --baseline-ref to plan each unit against its target branch"
```

---

### Task 6: Reaching the verdict

`eval` reads the sidecars and denies.

**Files:**
- Modify: `internal/cli/eval.go` (flag; call after `RequireCoverage` at line 155 and before `Decide` at line 166; new `dirtyBaselines` helper; the `failOnBlock` message at lines 178-186)
- Test: `internal/cli/eval_test.go` (create if absent)

**Interfaces:**
- Consumes: `baseline.Read` (Task 2), `policy.ApplicableAddresses` (Task 1), `report.BaselineUnit` and `RequireCleanBaseline` (Task 4), `planInput` (existing, `internal/cli/eval.go:221`).
- Produces: the `--require-clean-baseline` flag. Terminal — nothing consumes it.

- [ ] **Step 1: Write the failing test**

Create `internal/cli/eval_test.go`:

```go
package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/baseline"
)

// unitWithPlan writes a unit's plan.json under a fresh plan dir and returns
// both the dir and the planInput pointing at it.
func unitWithPlan(t *testing.T, unit, planJSON string) (string, planInput) {
	t.Helper()
	planDir := t.TempDir()
	dest := filepath.Join(planDir, unit)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dest, "plan.json")
	if err := os.WriteFile(file, []byte(planJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return dest, planInput{name: unit, file: file}
}

func TestDirtyBaselinesReportsADirtyUnit(t *testing.T) {
	dest, p := unitWithPlan(t, "units/prd", planWithACreate)
	if err := baseline.Write(dest, baseline.Result{
		Ref: "origin/main", Commit: "2907a68", State: baseline.Dirty,
		Addresses: []string{`kafka_topic.topics["scp.example.v1"]`},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := dirtyBaselines([]planInput{p})
	if err != nil {
		t.Fatalf("dirtyBaselines: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d dirty units, want 1", len(got))
	}
	if got[0].Path != "units/prd" || got[0].Missing {
		t.Errorf("got %+v", got[0])
	}
}

func TestDirtyBaselinesIgnoresCleanAndAbsent(t *testing.T) {
	for _, state := range []baseline.State{baseline.Clean, baseline.Absent} {
		t.Run(string(state), func(t *testing.T) {
			dest, p := unitWithPlan(t, "units/int", planWithACreate)
			if err := baseline.Write(dest, baseline.Result{
				Ref: "origin/main", Commit: "2907a68", State: state,
			}); err != nil {
				t.Fatal(err)
			}

			got, err := dirtyBaselines([]planInput{p})
			if err != nil {
				t.Fatalf("dirtyBaselines: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("got %d dirty units, want 0", len(got))
			}
		})
	}
}

func TestDirtyBaselinesSkipsUnitsThatApplyNothing(t *testing.T) {
	// No sidecar written at all — plan skips them, so eval must not then
	// report them missing. This is the revert case surviving end to end.
	_, p := unitWithPlan(t, "units/prd", planWithNothingToDo)

	got, err := dirtyBaselines([]planInput{p})
	if err != nil {
		t.Fatalf("dirtyBaselines: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d dirty units, want 0", len(got))
	}
}

func TestDirtyBaselinesReportsAMissingBaseline(t *testing.T) {
	// Changes of its own, and no baseline recorded. A missing fact is not a
	// clean one.
	_, p := unitWithPlan(t, "units/prd", planWithACreate)

	got, err := dirtyBaselines([]planInput{p})
	if err != nil {
		t.Fatalf("dirtyBaselines: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d dirty units, want 1", len(got))
	}
	if !got[0].Missing {
		t.Errorf("got %+v, want Missing", got[0])
	}
}

func TestDirtyBaselinesFailsOnAMalformedSidecar(t *testing.T) {
	dest, p := unitWithPlan(t, "units/prd", planWithACreate)
	if err := os.WriteFile(filepath.Join(dest, baseline.FileName), []byte(`{"state":"fine"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := dirtyBaselines([]planInput{p}); err == nil {
		t.Fatal("want an error, got nil")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli/ -run DirtyBaselines -v`
Expected: FAIL, `undefined: dirtyBaselines`.

- [ ] **Step 3: Implement dirtyBaselines**

Add to `internal/cli/eval.go`, next to `enginesFor` and `environmentFor`:

```go
// dirtyBaselines lists the units whose target branch still has changes waiting
// to be applied.
//
// Unlike enginesFor and environmentFor, a missing sidecar here is not silence.
// Those two report a fact that is nice to have; this one reports the absence of
// a check the caller explicitly asked for. A unit with changes of its own and no
// baseline recorded is reported as missing, and denies — a fact nobody could
// read is not a clean one.
//
// A unit whose own plan applies nothing is skipped, matching what plan does:
// nothing there can carry a backlog along with it, and this is what lets a
// merge request which clears the backlog through.
func dirtyBaselines(plans []planInput) ([]report.BaselineUnit, error) {
	var out []report.BaselineUnit
	for _, p := range plans {
		raw, err := os.ReadFile(p.file)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", p.file, err)
		}
		head, err := policy.ApplicableAddresses(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.file, err)
		}
		if len(head) == 0 {
			continue
		}

		res, found, err := baseline.Read(filepath.Dir(p.file))
		if err != nil {
			return nil, err
		}
		if !found {
			out = append(out, report.BaselineUnit{Path: p.name, Missing: true})
			continue
		}
		if res.State != baseline.Dirty {
			continue
		}
		out = append(out, report.BaselineUnit{
			Path:      p.name,
			Ref:       res.Ref,
			Commit:    res.Commit,
			Addresses: res.Addresses,
		})
	}
	return out, nil
}
```

Add the `baseline` import.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/cli/ -run DirtyBaselines -v`
Expected: PASS.

- [ ] **Step 5: Wire it into the command**

Add `requireCleanBaseline bool` to the `var` block, and:

```go
	cmd.Flags().BoolVar(&requireCleanBaseline, "require-clean-baseline", false,
		"deny when a changed unit's baseline still has changes waiting to be applied")
```

In `RunE`, immediately after the `requireCoverage` block and before `ParseWish`:

```go
			// Before Decide, like the guards above: an environment cannot apply
			// unattended while its target branch has changes nobody reviewed in
			// a diff, and Decide can only see that once this has recorded it.
			// Decide only ever sets a deployment method on Pass, so this deny is
			// what stops the auto-apply.
			if requireCleanBaseline {
				dirty, err := dirtyBaselines(plans)
				if err != nil {
					return err
				}
				rep.RequireCleanBaseline(dirty)
			}
```

And in the `failOnBlock` block, add before the `Guarded` case — a baseline deny outranks a guarded review, and is the more specific thing to say:

```go
				if len(rep.Baseline) > 0 {
					units := make([]string, 0, len(rep.Baseline))
					for _, u := range rep.Baseline {
						units = append(units, u.Path)
					}
					return fmt.Errorf("%s: the branch this targets has changes waiting to be applied (%s)",
						rep.Verdict, strings.Join(units, ", "))
				}
```

- [ ] **Step 6: Update the command's help text**

In the `Long` string, after the `--require-coverage` paragraph:

```
--require-clean-baseline denies when a unit that this change would apply
something to still has changes waiting to be applied on the branch it targets.
Those ride along in this plan and in the apply that follows approval, having
appeared in nobody's diff. It needs 'blastdoor plan --baseline-ref' to have run:

  blastdoor plan --baseline-ref origin/main --units-file units.txt
  blastdoor eval --plan-dir .blastdoor --policy policy --require-clean-baseline

Approving does not settle this. Apply or revert on the target branch first.
```

- [ ] **Step 7: Run the full check**

Run: `make check`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/cli/eval.go internal/cli/eval_test.go
git commit -m "feat(eval): add --require-clean-baseline to deny on an unapplied baseline"
```

---

### Task 7: The template and the documentation

The feature is inert until the template turns it on, and undiscoverable until the docs say it exists.

**Files:**
- Modify: `ci/gitlab/blastdoor.yml` (variables block; `blastdoor:plan` script; `blastdoor:eval` script)
- Modify: `AGENTS.md` (a new load-bearing decision)
- Modify: `docs/verdicts.md`, `docs/commands.md`, `docs/gitlab.md`
- Modify: `README.md` if it lists flags

**Interfaces:**
- Consumes: `--baseline-ref` (Task 5), `--require-clean-baseline` (Task 6).
- Produces: nothing.

- [ ] **Step 1: Add the template variable**

In `ci/gitlab/blastdoor.yml`, after `BLASTDOOR_IGNORE_PATHS`:

```yaml
  # Deny when the branch a merge request targets still has changes waiting to
  # be applied to a unit this change also touches. Those changes are in this
  # merge request's plan and in the apply that follows its approval, but in
  # nobody's diff — so a reviewer approves one thing and applies two.
  #
  # On by default, like BLASTDOOR_GUARD_PATHS: a template that gates is the
  # point of the template. Set it to "" to stop the line — a repository with a
  # permanently dirty unit needs that lever without reverting a template bump.
  #
  # Costs a second plan per unit that has changes, in the same job.
  BLASTDOOR_BASELINE_ENABLED: "true"
```

- [ ] **Step 2: Pass --baseline-ref from blastdoor:plan**

In the `blastdoor:plan` script, extend the block that sets `base_ref_arg`:

```yaml
    - |
      base_ref_arg=""
      baseline_arg=""
      if [ "$CI_COMMIT_BRANCH" = "$CI_DEFAULT_BRANCH" ]; then
        base_ref_arg="--base-ref $CI_COMMIT_BEFORE_SHA"
      elif [ -n "$BLASTDOOR_BASELINE_ENABLED" ]; then
        # The tip of the branch this targets, not the merge base: a branch
        # behind its target has a merge base predating the backlog, and the
        # baseline would come back clean while the backlog is still waiting.
        # Never on the default branch, where the baseline would be this branch
        # itself and its own pending changes would deny every commit.
        baseline_arg="--baseline-ref origin/${CI_MERGE_REQUEST_TARGET_BRANCH_NAME:-$CI_DEFAULT_BRANCH}"
      fi
```

and add `$baseline_arg` to the `blastdoor plan` invocation:

```yaml
    - blastdoor plan --units-file "$BLASTDOOR_DIR/units.txt" --out-dir "$BLASTDOOR_DIR" --environment "$ENV" $base_ref_arg $baseline_arg
```

- [ ] **Step 3: Pass --require-clean-baseline from blastdoor:eval**

In the `blastdoor:eval` script block, after the `ignores` loop:

```bash
      baseline_arg=""
      if [ -n "$BLASTDOOR_BASELINE_ENABLED" ] && [ "$CI_COMMIT_BRANCH" != "$CI_DEFAULT_BRANCH" ]; then
        baseline_arg="--require-clean-baseline"
      fi
```

and add `$baseline_arg` to the `blastdoor eval` invocation, on its own continuation line beside `$base_ref_arg`.

- [ ] **Step 4: Record the load-bearing decision in AGENTS.md**

Add a subsection under "Load-bearing decisions, do not quietly undo", after "The base ref is resolved, never assumed":

```markdown
### The baseline ref is the target branch tip, and the base ref is not

`detect.ResolveBaseRef` resolves a **merge base** and diffs with three dots, so
work that landed on the default branch after the fork is not attributed to this
change. That is right for "which units does this branch touch".

`blastdoor plan --baseline-ref` resolves the **tip** of the branch being
targeted, and this disagreement is deliberate. It asks a different question:
what is pending if this merge request does not exist. A branch that is behind
its target has a merge base predating an unapplied backlog, so a merge-base
baseline comes back clean while the backlog is still waiting — which is the
exact failure the flag exists to catch.

Same-sounding flags, opposite correct answers. Do not "fix" one to match the
other.

### A dirty baseline denies, and only for units with changes of their own

A plan is desired state against live state, not "what this merge request
changed". A change merged but never applied therefore turns up in the plan of
the next merge request that touches that unit, and in the apply that follows its
approval, having appeared in nobody's diff. `Report.RequireCleanBaseline` denies
on that.

**Deny, not review.** Approving does nothing about a backlog; only applying or
reverting on the target branch does. Same asymmetry as everywhere else here.

**Only units whose own plan applies something.** This is not an optimisation.
Out-of-band drift is sometimes fixed by codifying the drifted value, and that
merge request touches the dirty unit — under an unscoped rule it would be denied
too, and the only exit would be applying the target branch and reverting the
change you meant to keep. Scoped, such a merge request has an empty head plan,
needs no baseline, and merges. That is why there is no override variable and no
accept-list: do not add one, and do not "simplify" the scope away, because the
scope *is* the escape hatch.

A unit with changes of its own and no baseline recorded denies as well. A fact
nobody could read is not a clean one.
```

- [ ] **Step 5: Document the flags**

Read each file first and match its existing heading level and table shape — these are additions, not rewrites.

`docs/commands.md`, in the `plan` flag list:

> `--baseline-ref` — plan each changed unit against this ref as well, and record what is still waiting to be applied there. Use the tip of the branch the change targets, `origin/main` for most repositories. Off when empty. Costs a second plan per unit that has changes.

and in the `eval` flag list:

> `--require-clean-baseline` — deny when a changed unit's baseline still has changes waiting to be applied. Needs `blastdoor plan --baseline-ref` to have run; a unit with changes and no baseline recorded denies too.

`docs/verdicts.md`, wherever guarded and uncovered paths are described as forcing a verdict without a policy saying so, add:

> An unapplied baseline is the third, and the only one that produces `deny` rather than `review`. A change merged to the target branch but never applied is still pending, so it appears in the plan of the next merge request touching that unit — and in the apply that follows its approval — having appeared in nobody's diff. Approving cannot settle that: only applying or reverting on the target branch can. Only units whose own plan applies something are checked, so a merge request that clears the backlog is not itself blocked by it.

`docs/gitlab.md`, in the variables table:

> `BLASTDOOR_BASELINE_ENABLED` — deny when the target branch has changes waiting to be applied to a unit this change touches. On by default. Set to `""` to turn it off, for a repository with a unit that is permanently dirty. Costs a second plan per changed unit, in the `blastdoor:plan` job.

Check `README.md` for a flag or variable list; if it has one, add the same two flags and the variable there. If it does not, leave it alone.

- [ ] **Step 6: Verify the template YAML parses**

GitLab's `!reference` is a custom tag that plain `yaml.safe_load` rejects, so the loader has to be told about it — without this the check fails on the file as it stands today and proves nothing about your change.

Run:

```bash
python3 -c "
import yaml
yaml.SafeLoader.add_constructor('!reference', lambda l, n: None)
doc = yaml.safe_load(open('ci/gitlab/blastdoor.yml'))
assert 'BLASTDOOR_BASELINE_ENABLED' in doc['variables'], 'variable missing'
print('ok')
"
```

Expected: `ok`.

- [ ] **Step 7: Run the full check**

Run: `make check`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add ci/gitlab/blastdoor.yml AGENTS.md docs/ README.md
git commit -m "feat(ci)!: deny by default when the target branch has an unapplied baseline

The template now passes --baseline-ref and --require-clean-baseline, which
changes behaviour for existing consumers: a merge request touching a unit whose
target branch still has changes waiting is denied rather than merged. Set
BLASTDOOR_BASELINE_ENABLED to \"\" to turn it off.

The flags stay off in the binary, so driving blastdoor directly is unaffected."
```

---

## Verification

After Task 7, confirm end to end against the case that prompted this — the SCP provisioning repository, MR !176:

- [ ] Build: `make build`
- [ ] In a checkout of the provisioning repo at MR !176's head, run `./bin/blastdoor plan --root terraform --baseline-ref origin/main --out-dir /tmp/bd` and confirm `terraform/components/kafka/instances/g2/config/prd/baseline.json` comes back `dirty` naming `kafka_topic.topics["scp.example.v1"]`, while `int` and `stg` come back `clean`.
- [ ] Run `./bin/blastdoor eval --plan-dir /tmp/bd --policy <policies> --require-clean-baseline` and confirm the verdict is `deny`, the summary names the prd unit and the pending address, and the headline does not say "0 change(s)".

This needs Consul and Kafka credentials for the plan step. If they are not available, say so rather than reporting the verification as done.
