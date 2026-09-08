# Deployment Method Per Environment — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Decide per environment whether a change may be applied unattended, and deliver that decision to GitLab as both a dotenv record and a generated child pipeline carrying a literal `when:`.

**Architecture:** `blastdoor plan --environment <name>` records each unit's environment beside its plan, exactly as `engine.txt` is already recorded. `blastdoor eval` reads it back, folds the per-unit verdicts into one method per environment against a ceiling the pipeline states, and writes `BLASTDOOR_DEPLOY_<ENV>=auto|manual|none` plus an `apply.gitlab-ci.yml`. The wish can only be tightened, never loosened. No Rego changes.

**Tech Stack:** Go 1.27, cobra, gopkg.in/yaml.v3 (already a dependency, used in tests here), OPA v1 (untouched by this plan).

**Spec:** [docs/superpowers/specs/2026-08-31-deployment-method-design.md](../specs/2026-08-31-deployment-method-design.md)

## Global Constraints

- `go.mod` says `go 1.27.0`. If the machine has an older Go the toolchain downloads it. **Do not lower the `go` directive.**
- **No new dependencies.** `opa`, `cobra` and `yaml.v3` are what exist; this plan adds none.
- **No changes under `internal/policy`.** The `manual` rule set was considered and cut — see the spec's closing section. If a task seems to need one, stop and re-read it.
- Run `make check` (fmt, vet, `go test -race ./...`) before calling any task done.
- Tests run real things: real git repositories in `t.TempDir()`, real Rego compilation. Keep it that way.
- Conventional Commits — `feat:` minor, `fix:` patch, `docs:`/`test:`/`ci:` no release.
- **Commit locally per task. Do not push, do not tag.** blastdoor's AGENTS.md forbids pushing without being asked.
- Every new exported symbol gets a doc comment that says *why*, matching the register of the surrounding code. This repo's comments explain decisions, not mechanics.

## File Structure

| Path | Responsibility |
|---|---|
| `internal/report/method.go` | **New.** `Method`, `Wish`, `ParseWish`, `EnvDecision`, `Report.Decide`. The fold and its input parsing. |
| `internal/report/apply.go` | **New.** Generating `apply.gitlab-ci.yml`. |
| `internal/report/report.go` | `Unit.Environment`, `Report.Environments`, dotenv keys, summary table. |
| `internal/cli/plan.go` | `--environment`, writes `environment.txt`. |
| `internal/cli/eval.go` | `--deployment-method-wish`, `--apply-include`, reads `environment.txt`, calls `Decide` after guards. |
| `internal/detect/detect.go` | Rejects the all-zero base ref (Part B1). |
| `ci/gitlab/blastdoor.yml` | Wish variable, trigger job, default-branch jobs, guard the apply include. |

`method.go` and `apply.go` are new files rather than additions to `report.go`, which is already 385 lines and has one clear job (folding verdicts and rendering them). The fold to a deployment method is a second question with its own input type; it earns its own file.

---

### Task 1: `Method` and the wish

**Files:**
- Create: `internal/report/method.go`
- Test: `internal/report/method_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `type Method string`; consts `Auto`, `Manual`, `None Method`; `type Wish struct{...}`; `func ParseWish(s string) (Wish, error)`; `func (w Wish) Stated() bool`; `func (w Wish) Names() []string`; `func (w Wish) Method(env string) (Method, bool)`.

- [ ] **Step 1: Write the failing test**

Create `internal/report/method_test.go`:

```go
package report

import "testing"

func TestParseWishKeepsStatedOrder(t *testing.T) {
	w, err := ParseWish("int=auto,stg=auto,prd=manual")
	if err != nil {
		t.Fatalf("ParseWish: %v", err)
	}
	want := []string{"int", "stg", "prd"}
	got := w.Names()
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if m, ok := w.Method("prd"); !ok || m != Manual {
		t.Errorf("Method(prd) = %q, %v, want manual, true", m, ok)
	}
	if !w.Stated() {
		t.Error("Stated() = false, want true")
	}
}

func TestParseWishEmptyIsNotStated(t *testing.T) {
	w, err := ParseWish("")
	if err != nil {
		t.Fatalf("ParseWish: %v", err)
	}
	if w.Stated() {
		t.Error("Stated() = true for an empty wish, want false")
	}
}

func TestParseWishRejects(t *testing.T) {
	tests := []struct {
		name, input string
	}{
		{"no equals", "int"},
		{"no environment", "=auto"},
		{"unknown method", "int=sometimes"},
		{"none cannot be wished for", "int=none"},
		{"duplicate environment", "int=auto,int=manual"},
		{"name is not a dotenv key", "my-env=auto"},
		{"name starts with a digit", "1int=auto"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseWish(tc.input); err == nil {
				t.Errorf("ParseWish(%q) = nil error, want one", tc.input)
			}
		})
	}
}

func TestParseWishTolerAtesSpacingAndTrailingComma(t *testing.T) {
	w, err := ParseWish(" int = auto , prd=manual, ")
	if err != nil {
		t.Fatalf("ParseWish: %v", err)
	}
	if m, _ := w.Method("int"); m != Auto {
		t.Errorf("Method(int) = %q, want auto", m)
	}
	if len(w.Names()) != 2 {
		t.Errorf("Names() = %v, want 2 entries", w.Names())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/report/ -run TestParseWish -v`
Expected: FAIL — `undefined: ParseWish`, `undefined: Auto`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/report/method.go`:

```go
package report

import (
	"fmt"
	"regexp"
	"strings"
)

// Method is how an environment's changes reach the infrastructure.
type Method string

const (
	// Auto applies with nobody watching.
	Auto Method = "auto"
	// Manual waits for a person to start the job.
	Manual Method = "manual"
	// None is an environment with nothing to apply, which is not the same as
	// one that is safe to apply automatically.
	None Method = "none"
)

// Wish is the ceiling the pipeline states, in the order it named the
// environments.
//
// The order is kept rather than sorted because "int, stg, prd" is a promotion
// order and reads as one. Sorting gives "int, prd, stg", which puts production
// in the middle of the summary table where a reader skims past it.
type Wish struct {
	order []string
	byEnv map[string]Method
}

// dotenvKey is what GitLab accepts as a variable name. An environment name
// becomes one, so a name that is not one has to fail while it can still be
// attributed to the wish that stated it.
var dotenvKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseWish reads "int=auto,stg=auto,prd=manual".
//
// Comma-separated rather than space-separated on purpose: the package doc of
// internal/config records what happened the last time a list travelled through
// a CI variable with a chance for the shell to expand it first.
func ParseWish(s string) (Wish, error) {
	w := Wish{byEnv: map[string]Method{}}

	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			return Wish{}, fmt.Errorf("deployment method wish %q is not <environment>=<method>", entry)
		}
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)

		if name == "" {
			return Wish{}, fmt.Errorf("deployment method wish %q names no environment", entry)
		}
		if !dotenvKey.MatchString(strings.ToUpper(name)) {
			return Wish{}, fmt.Errorf(
				"environment %q cannot become a dotenv variable name: use letters, digits and underscores, not starting with a digit", name)
		}
		if _, dup := w.byEnv[name]; dup {
			return Wish{}, fmt.Errorf("environment %q is named twice in the deployment method wish", name)
		}

		// None is not something a pipeline asks for. It is what the fold says
		// about an environment nothing changed in.
		switch Method(value) {
		case Auto, Manual:
		default:
			return Wish{}, fmt.Errorf("environment %q wishes for %q: it has to be auto or manual", name, value)
		}

		w.order = append(w.order, name)
		w.byEnv[name] = Method(value)
	}
	return w, nil
}

// Stated reports whether any wish was given. Without one the whole feature is
// off and nothing new is written, so an existing pipeline is untouched.
func (w Wish) Stated() bool { return len(w.order) > 0 }

// Names lists the environments in the order the wish named them.
func (w Wish) Names() []string { return w.order }

// Method returns the ceiling for one environment.
func (w Wish) Method(env string) (Method, bool) {
	m, ok := w.byEnv[env]
	return m, ok
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/report/ -run TestParseWish -v`
Expected: PASS, all four tests.

- [ ] **Step 5: Commit**

```bash
git add internal/report/method.go internal/report/method_test.go
git commit -m "feat: parse the per-environment deployment method wish"
```

---

### Task 2: The fold

**Files:**
- Modify: `internal/report/method.go` (append)
- Modify: `internal/report/report.go:15-41` — add `Unit.Environment` and `Report.Environments`
- Test: `internal/report/method_test.go` (append)

**Interfaces:**
- Consumes: `Method`, `Wish`, `Auto`, `Manual`, `None` from Task 1. `policy.Verdict`, `policy.Worse`, `report.Build` as they exist.
- Produces: `type EnvDecision struct{Name string; Wish, Method Method; Verdict policy.Verdict; UnitCount int; Reasons []string}`; `func (r *Report) Decide(w Wish) error`; field `Unit.Environment string`; field `Report.Environments []EnvDecision`.

- [ ] **Step 1: Write the failing test**

In `internal/report/method_test.go`, extend the existing import block to:

```go
import (
	"testing"

	"github.com/raccoon-core/blastdoor/internal/policy"
)
```

then append:

```go
// unit builds a report unit in one environment with one change.
func unit(path, env string, v policy.Verdict) Unit {
	return Unit{
		Path:        path,
		Environment: env,
		Changes:     []policy.Change{{Address: "x", Verdict: v, Reasons: []string{"because"}, Actions: []string{"create"}}},
	}
}

func methodFor(t *testing.T, r Report, env string) Method {
	t.Helper()
	for _, e := range r.Environments {
		if e.Name == env {
			return e.Method
		}
	}
	t.Fatalf("no decision for environment %q in %+v", env, r.Environments)
	return ""
}

// The wish is a ceiling: nothing can turn a manual wish into an auto apply.
func TestDecideCeilingHolds(t *testing.T) {
	rep := Build([]Unit{unit("ops/prd/a", "prd", policy.Pass)})
	w, _ := ParseWish("prd=manual")
	if err := rep.Decide(w); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got := methodFor(t, rep, "prd"); got != Manual {
		t.Errorf("prd = %q, want manual: an all-pass plan must not override a manual wish", got)
	}
}

func TestDecideVerdictTightens(t *testing.T) {
	rep := Build([]Unit{unit("ops/int/a", "int", policy.Review)})
	w, _ := ParseWish("int=auto")
	if err := rep.Decide(w); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got := methodFor(t, rep, "int"); got != Manual {
		t.Errorf("int = %q, want manual: the verdict here is review", got)
	}
}

func TestDecideAutoWhenNothingObjects(t *testing.T) {
	rep := Build([]Unit{unit("ops/int/a", "int", policy.Pass)})
	w, _ := ParseWish("int=auto")
	if err := rep.Decide(w); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got := methodFor(t, rep, "int"); got != Auto {
		t.Errorf("int = %q, want auto", got)
	}
}

// The ordering bug this guards: an environment nothing changed in has a
// vacuously passing verdict, so asking about auto first makes it auto.
func TestDecideUntouchedEnvironmentIsNoneNotAuto(t *testing.T) {
	rep := Build([]Unit{unit("ops/int/a", "int", policy.Pass)})
	w, _ := ParseWish("int=auto,prd=auto")
	if err := rep.Decide(w); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got := methodFor(t, rep, "prd"); got != None {
		t.Errorf("prd = %q, want none: no unit changed there, and its wish is auto", got)
	}
}

// A change that rewrites the rules judging it cannot apply unattended anywhere.
func TestDecideGuardsForceManualEverywhere(t *testing.T) {
	rep := Build([]Unit{
		unit("ops/int/a", "int", policy.Pass),
		unit("ops/stg/a", "stg", policy.Pass),
	})
	rep.RequireReview([]string{"policy/kafka.rego"})
	w, _ := ParseWish("int=auto,stg=auto")
	if err := rep.Decide(w); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	for _, env := range []string{"int", "stg"} {
		if got := methodFor(t, rep, env); got != Manual {
			t.Errorf("%s = %q, want manual: the change edits guarded paths", env, got)
		}
	}
}

func TestDecideUncoveredFilesForceManualEverywhere(t *testing.T) {
	rep := Build([]Unit{unit("ops/int/a", "int", policy.Pass)})
	rep.RequireCoverage([]string{"topics.yaml"})
	w, _ := ParseWish("int=auto")
	if err := rep.Decide(w); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got := methodFor(t, rep, "int"); got != Manual {
		t.Errorf("int = %q, want manual: a file no plan covers", got)
	}
}

func TestDecideKeepsWishOrder(t *testing.T) {
	rep := Build([]Unit{unit("ops/prd/a", "prd", policy.Pass)})
	w, _ := ParseWish("int=auto,stg=auto,prd=manual")
	if err := rep.Decide(w); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	want := []string{"int", "stg", "prd"}
	for i, name := range want {
		if rep.Environments[i].Name != name {
			t.Errorf("Environments[%d] = %q, want %q — the wish order, not alphabetical", i, rep.Environments[i].Name, name)
		}
	}
}

func TestDecideRejectsUnplaceableUnits(t *testing.T) {
	tests := []struct {
		name string
		unit Unit
		wish string
	}{
		{"no environment recorded", unit("ops/a", "", policy.Pass), "int=auto"},
		{"environment the wish does not name", unit("ops/dev/a", "dev", policy.Pass), "int=auto"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := Build([]Unit{tc.unit})
			w, _ := ParseWish(tc.wish)
			if err := rep.Decide(w); err == nil {
				t.Error("Decide = nil error, want one: an unplaceable unit is applied by nobody or by a job nobody configured")
			}
		})
	}
}

// No wish means the feature is off, including its errors.
func TestDecideWithoutAWishIsInert(t *testing.T) {
	rep := Build([]Unit{unit("ops/a", "", policy.Pass)})
	if err := rep.Decide(Wish{}); err != nil {
		t.Fatalf("Decide with no wish: %v", err)
	}
	if len(rep.Environments) != 0 {
		t.Errorf("Environments = %v, want none", rep.Environments)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/report/ -run TestDecide -v`
Expected: FAIL — `unknown field Environment in struct literal`, `rep.Decide undefined`.

- [ ] **Step 3: Write minimal implementation**

In `internal/report/report.go`, add the field to `Unit` (currently lines 15-20):

```go
// Unit is one planned directory and what the policies made of it.
type Unit struct {
	Path string `json:"path"`
	// Environment is what 'blastdoor plan --environment' recorded beside this
	// unit's plan. Empty when nothing recorded one.
	Environment string          `json:"environment,omitempty"`
	Verdict     policy.Verdict  `json:"verdict"`
	Changes     []policy.Change `json:"changes"`
}
```

and to `Report`, after `Engines` (currently line 40):

```go
	// Environments says, per environment, whether this change may be applied
	// unattended. Empty when no wish was stated, which turns the feature off.
	Environments []EnvDecision `json:"environments,omitempty"`
```

In `internal/report/method.go`, extend the existing import block to:

```go
import (
	"fmt"
	"regexp"
	"strings"

	"github.com/raccoon-core/blastdoor/internal/policy"
)
```

then append:

```go
// EnvDecision is one environment's answer, and why.
type EnvDecision struct {
	Name      string         `json:"name"`
	Wish      Method         `json:"wish"`
	Method    Method         `json:"method"`
	Verdict   policy.Verdict `json:"verdict"`
	UnitCount int            `json:"unit_count"`
	// Reasons says why this is not auto, most specific first, and is empty when
	// it is. "prd is manual" and "prd is manual because a topic is being
	// deleted" are different facts, and the second is the one that says whether
	// the pipeline is working or misconfigured.
	Reasons []string `json:"reasons,omitempty"`
}

// Decide folds the units into one deployment method per environment.
//
// It must run AFTER RequireReview and RequireCoverage. Both force review across
// the whole repository, and an environment cannot apply unattended while the
// change is rewriting the rules that judge it — but Decide can only see that
// once those have been recorded.
//
// The wish is a ceiling. Every condition here can only move an environment
// towards manual; nothing turns a manual wish into an auto apply.
func (r *Report) Decide(w Wish) error {
	if !w.Stated() {
		return nil
	}

	// A unit nobody can place would be applied by a job nobody configured, or
	// by no job at all — and "silently skipped" is indistinguishable from
	// "applied fine" in every artefact downstream.
	for _, u := range r.Units {
		if u.Environment == "" {
			return fmt.Errorf(
				"unit %q has no environment recorded: pass --environment to 'blastdoor plan' so it writes one beside the plan", u.Path)
		}
		if _, ok := w.Method(u.Environment); !ok {
			return fmt.Errorf("unit %q is in environment %q, which the deployment method wish does not name (it names %s)",
				u.Path, u.Environment, strings.Join(w.Names(), ", "))
		}
	}

	// Repository-wide, so they cannot be attributed to any one environment.
	// Both already force review, so the verdict test below would catch them
	// anyway; they are named separately so they survive someone later deciding
	// that a review verdict alone should not block an unattended apply.
	var wide []string
	if len(r.Guarded) > 0 {
		wide = append(wide, "the change edits guarded paths")
	}
	if len(r.Uncovered) > 0 {
		wide = append(wide, "the change edits files no plan covers")
	}

	for _, name := range w.Names() {
		wish, _ := w.Method(name)
		d := EnvDecision{Name: name, Wish: wish, Method: Manual, Verdict: policy.Pass}

		for _, u := range r.Units {
			if u.Environment != name {
				continue
			}
			d.UnitCount++
			d.Verdict = policy.Worse(d.Verdict, u.Verdict)
		}

		// None is tested first, and the order is load-bearing: an environment
		// with no changed units has a vacuously passing verdict, so asking
		// about auto first resolves every untouched environment to auto and
		// generates an apply job for it.
		if d.UnitCount == 0 {
			d.Method = None
			d.Reasons = []string{"no unit changed in this environment"}
			r.Environments = append(r.Environments, d)
			continue
		}

		d.Reasons = manualReasons(d, wide)
		if len(d.Reasons) == 0 {
			d.Method = Auto
		}
		r.Environments = append(r.Environments, d)
	}
	return nil
}

// manualReasons lists why an environment cannot apply unattended. Empty means
// nothing objects, which is the only way to auto.
func manualReasons(d EnvDecision, wide []string) []string {
	var out []string
	if d.Wish == Manual {
		out = append(out, "the pipeline asks for a manual apply in this environment")
	}
	if d.Verdict != policy.Pass {
		out = append(out, fmt.Sprintf("the verdict here is %s", d.Verdict))
	}
	return append(out, wide...)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/report/ -v` then `make check`
Expected: PASS. `make check` green — existing report tests must still pass, since `Unit` gained an `omitempty` field and `Report` gained one.

- [ ] **Step 5: Commit**

```bash
git add internal/report/method.go internal/report/method_test.go internal/report/report.go
git commit -m "feat: fold unit verdicts into a deployment method per environment"
```

---

### Task 3: dotenv keys and the summary table

**Files:**
- Modify: `internal/report/report.go:188-193` (`WriteEnv`), `:196-234` (`WriteMarkdown`)
- Test: `internal/report/report_test.go` (append)

**Interfaces:**
- Consumes: `EnvDecision`, `Report.Environments`, `Method`, `Auto`, `Manual`, `None` from Task 2.
- Produces: `BLASTDOOR_DEPLOY_<ENV>` lines in `WriteEnv`; `func (r Report) environmentTable() string`; `func methodMarker(m Method) string`.

- [ ] **Step 1: Write the failing test**

Append to `internal/report/report_test.go`:

```go
// decided builds a report that has already been through Decide.
func decided(t *testing.T, wish string, units []Unit) Report {
	t.Helper()
	rep := Build(units)
	w, err := ParseWish(wish)
	if err != nil {
		t.Fatalf("ParseWish: %v", err)
	}
	if err := rep.Decide(w); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	return rep
}

func TestWriteEnvCarriesTheDeploymentMethods(t *testing.T) {
	rep := decided(t, "int=auto,stg=auto,prd=manual", []Unit{
		{Path: "ops/int/a", Environment: "int", Changes: []policy.Change{change("x", policy.Pass, "fine")}},
		{Path: "ops/stg/a", Environment: "stg", Changes: []policy.Change{change("y", policy.Review, "look")}},
	})

	var b strings.Builder
	if err := rep.WriteEnv(&b); err != nil {
		t.Fatalf("WriteEnv: %v", err)
	}
	got := b.String()

	for _, want := range []string{
		"BLASTDOOR_DEPLOY_INT=auto\n",
		"BLASTDOOR_DEPLOY_STG=manual\n",
		"BLASTDOOR_DEPLOY_PRD=none\n",
		"BLASTDOOR_VERDICT=review\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("WriteEnv missing %q, got:\n%s", want, got)
		}
	}
}

// No wish, no new keys: an existing pipeline sees exactly what it saw before.
func TestWriteEnvUnchangedWithoutAWish(t *testing.T) {
	rep := Build([]Unit{{Path: "a", Changes: []policy.Change{change("x", policy.Pass, "fine")}}})

	var b strings.Builder
	if err := rep.WriteEnv(&b); err != nil {
		t.Fatalf("WriteEnv: %v", err)
	}
	if strings.Contains(b.String(), "BLASTDOOR_DEPLOY_") {
		t.Errorf("WriteEnv wrote a deployment key with no wish stated:\n%s", b.String())
	}
}

func TestWriteMarkdownShowsTheEnvironmentTable(t *testing.T) {
	rep := decided(t, "int=auto,prd=manual", []Unit{
		{Path: "ops/int/a", Environment: "int", Changes: []policy.Change{change("x", policy.Pass, "fine")}},
	})

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	got := b.String()

	for _, want := range []string{"| Environment | Apply | Why |", "✅ auto", "— none"} {
		if !strings.Contains(got, want) {
			t.Errorf("WriteMarkdown missing %q, got:\n%s", want, got)
		}
	}

	// Above the verdict table: a reviewer decides whether to approve knowing
	// what approving will cause.
	if strings.Index(got, "| Environment |") > strings.Index(got, "| Verdict |") {
		t.Error("the environment table must come before the verdict table")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/report/ -run 'TestWriteEnvCarries|TestWriteMarkdownShows' -v`
Expected: FAIL — `WriteEnv missing "BLASTDOOR_DEPLOY_INT=auto"`, and the table string absent.

- [ ] **Step 3: Write minimal implementation**

Replace `WriteEnv` in `internal/report/report.go`:

```go
// WriteEnv writes a dotenv file, for GitLab's `artifacts:reports:dotenv` to
// pass the verdict to later jobs.
//
// The deployment methods are a record, not a mechanism: GitLab's `when:` does
// not expand a variable, and `rules:` — which can set `when:` — is evaluated at
// pipeline creation, before this file exists. WriteApplyYAML is what actually
// carries the decision into a job.
func (r Report) WriteEnv(w io.Writer) error {
	if _, err := fmt.Fprintf(w,
		"BLASTDOOR_VERDICT=%s\nBLASTDOOR_UNIT_COUNT=%d\nBLASTDOOR_PASS_COUNT=%d\nBLASTDOOR_REVIEW_COUNT=%d\nBLASTDOOR_DENY_COUNT=%d\n",
		r.Verdict, r.UnitCount, r.Counts[policy.Pass], r.Counts[policy.Review], r.Counts[policy.Deny]); err != nil {
		return err
	}
	for _, e := range r.Environments {
		if _, err := fmt.Fprintf(w, "BLASTDOOR_DEPLOY_%s=%s\n", strings.ToUpper(e.Name), e.Method); err != nil {
			return err
		}
	}
	return nil
}
```

In `WriteMarkdown`, insert the table immediately after `b.WriteString(r.headline())` (currently line 200):

```go
	b.WriteString(r.headline())
	b.WriteString(r.environmentTable())
```

and add, next to `verdictTable`:

```go
// environmentTable says what the apply will do, per environment.
//
// Above the verdict table deliberately. "What does this change contain" and
// "what will approving it cause" are different questions, and the second is the
// one a reviewer is answering when they click approve.
func (r Report) environmentTable() string {
	if len(r.Environments) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n| Environment | Apply | Why |\n|---|---|---|\n")
	for _, e := range r.Environments {
		b.WriteString(fmt.Sprintf("| %s | %s | %s |\n",
			escapePipes(e.Name),
			methodMarker(e.Method),
			escapePipes(strings.Join(e.Reasons, "; "))))
	}
	return b.String()
}

// methodMarker is the symbol for a deployment method.
//
// The word is always kept alongside the symbol, for the same reason emoji()
// keeps it: a symbol alone is lost to a screen reader and to a plain-text copy
// of the note.
func methodMarker(m Method) string {
	switch m {
	case Auto:
		return "✅ auto"
	case Manual:
		return "✋ manual"
	default:
		return "— none"
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/report/ -v` then `make check`
Expected: PASS. Watch `TestWriteMarkdown*` and the emoji tests — the new table sits between the headline and the guarded block.

- [ ] **Step 5: Commit**

```bash
git add internal/report/report.go internal/report/report_test.go
git commit -m "feat: report the deployment method in the dotenv and the summary"
```

---

### Task 4: generating the apply pipeline

**Files:**
- Create: `internal/report/apply.go`
- Test: `internal/report/apply_test.go`

**Interfaces:**
- Consumes: `Report.Environments`, `EnvDecision`, `None`, `Manual` from Task 2. The test helpers `decided(t, wish, units)` and `change(address, verdict, reasons...)` live in `report_test.go` — `decided` is added by **Task 3**, so do that task first.
- Produces: `func (r Report) WriteApplyYAML(w io.Writer, include string) error`.

- [ ] **Step 1: Write the failing test**

Create `internal/report/apply_test.go`:

```go
package report

import (
	"strings"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/policy"
	"gopkg.in/yaml.v3"
)

func applyYAML(t *testing.T, rep Report) (string, map[string]any) {
	t.Helper()
	var b strings.Builder
	if err := rep.WriteApplyYAML(&b, ".gitlab/blastdoor-apply.yml"); err != nil {
		t.Fatalf("WriteApplyYAML: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(b.String()), &doc); err != nil {
		t.Fatalf("generated YAML does not parse: %v\n%s", err, b.String())
	}
	return b.String(), doc
}

func job(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	raw, ok := doc[name]
	if !ok {
		t.Fatalf("no job %q in %v", name, doc)
	}
	j, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("job %q is %T, want a mapping", name, raw)
	}
	return j
}

func TestWriteApplyYAMLCarriesALiteralWhen(t *testing.T) {
	rep := decided(t, "int=auto,stg=manual", []Unit{
		{Path: "ops/int/a", Environment: "int", Changes: []policy.Change{change("x", policy.Pass, "fine")}},
		{Path: "ops/stg/a", Environment: "stg", Changes: []policy.Change{change("y", policy.Pass, "fine")}},
	})

	_, doc := applyYAML(t, rep)

	if got := job(t, doc, "apply:int")["when"]; got != "on_success" {
		t.Errorf("apply:int when = %v, want on_success", got)
	}
	if got := job(t, doc, "apply:stg")["when"]; got != "manual" {
		t.Errorf("apply:stg when = %v, want manual", got)
	}
	if got := job(t, doc, "apply:int")["extends"]; got != ".blastdoor:apply" {
		t.Errorf("apply:int extends = %v, want .blastdoor:apply", got)
	}
}

// Nothing to apply, so no job: an empty one would run the repository's apply
// script against no units.
func TestWriteApplyYAMLSkipsEnvironmentsWithNothingToApply(t *testing.T) {
	rep := decided(t, "int=auto,prd=auto", []Unit{
		{Path: "ops/int/a", Environment: "int", Changes: []policy.Change{change("x", policy.Pass, "fine")}},
	})

	_, doc := applyYAML(t, rep)

	if _, found := doc["apply:prd"]; found {
		t.Error("apply:prd was generated for an environment where nothing changed")
	}
	if _, found := doc["apply:int"]; !found {
		t.Error("apply:int is missing")
	}
}

func TestWriteApplyYAMLIncludesTheRepositoryJob(t *testing.T) {
	rep := decided(t, "int=auto", []Unit{
		{Path: "ops/int/a", Environment: "int", Changes: []policy.Change{change("x", policy.Pass, "fine")}},
	})

	text, doc := applyYAML(t, rep)

	if _, found := doc["include"]; !found {
		t.Errorf("no include in:\n%s", text)
	}
	if !strings.Contains(text, ".gitlab/blastdoor-apply.yml") {
		t.Errorf("the include does not name the file:\n%s", text)
	}
	if got := job(t, doc, "apply:int")["variables"]; got == nil {
		t.Error("apply:int has no variables block naming its environment")
	}
}

// The dotenv and the YAML come from one fold and must never disagree.
func TestApplyYAMLAgreesWithTheDotenv(t *testing.T) {
	tests := []struct {
		name, wish string
		units      []Unit
	}{
		{"all auto", "int=auto", []Unit{{Path: "a", Environment: "int", Changes: []policy.Change{change("x", policy.Pass, "fine")}}}},
		{"review tightens", "int=auto", []Unit{{Path: "a", Environment: "int", Changes: []policy.Change{change("x", policy.Review, "look")}}}},
		{"manual wish", "prd=manual", []Unit{{Path: "a", Environment: "prd", Changes: []policy.Change{change("x", policy.Pass, "fine")}}}},
		{"nothing changed", "int=auto,prd=auto", []Unit{{Path: "a", Environment: "int", Changes: []policy.Change{change("x", policy.Pass, "fine")}}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := decided(t, tc.wish, tc.units)

			var env strings.Builder
			if err := rep.WriteEnv(&env); err != nil {
				t.Fatalf("WriteEnv: %v", err)
			}
			_, doc := applyYAML(t, rep)

			for _, e := range rep.Environments {
				line := "BLASTDOOR_DEPLOY_" + strings.ToUpper(e.Name) + "=" + string(e.Method) + "\n"
				if !strings.Contains(env.String(), line) {
					t.Errorf("dotenv missing %q", line)
				}

				name := "apply:" + e.Name
				_, generated := doc[name]
				switch e.Method {
				case None:
					if generated {
						t.Errorf("%s generated, but the dotenv says none", name)
					}
				case Auto:
					if !generated || job(t, doc, name)["when"] != "on_success" {
						t.Errorf("%s does not match the dotenv's auto", name)
					}
				case Manual:
					if !generated || job(t, doc, name)["when"] != "manual" {
						t.Errorf("%s does not match the dotenv's manual", name)
					}
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/report/ -run 'Apply' -v`
Expected: FAIL — `rep.WriteApplyYAML undefined`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/report/apply.go`:

```go
package report

import (
	"fmt"
	"io"
	"strings"
)

// WriteApplyYAML writes a child pipeline whose jobs carry a literal `when:`.
//
// This file exists because GitLab cannot make this decision from a variable.
// `when:` does not expand one, and `rules:` — which can set `when:` — is
// evaluated at pipeline creation, before any job has run, so the dotenv written
// by 'blastdoor eval' is not visible to it. Generating the pipeline is the only
// place the decision can become a literal the runner will honour.
//
// Blastdoor writes the `when:` and nothing else. The image, the credentials and
// the apply command belong to the repository, which supplies them as a hidden
// .blastdoor:apply job in the included file — and that file must be guarded,
// because it runs with the credentials that change infrastructure.
func (r Report) WriteApplyYAML(w io.Writer, include string) error {
	var b strings.Builder
	b.WriteString("# generated by blastdoor; do not edit\n")
	b.WriteString("include:\n  - local: " + yamlString(include) + "\n")

	for _, e := range r.Environments {
		// Nothing to apply, so no job. An empty one would run the repository's
		// apply script against no units, which fails in a way that reads as a
		// broken pipeline rather than as an environment nothing touched.
		if e.Method == None {
			continue
		}

		when := "on_success"
		if e.Method == Manual {
			when = "manual"
		}

		fmt.Fprintf(&b,
			"\napply:%s:\n  extends: .blastdoor:apply\n  when: %s\n  variables:\n    BLASTDOOR_ENV: %s\n",
			e.Name, when, yamlString(e.Name))
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// yamlString quotes a value so it cannot be read as YAML syntax.
//
// Environment names are already restricted to what a dotenv key allows, but the
// include path is an arbitrary string from a flag.
func yamlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/report/ -v` then `make check`
Expected: PASS, including `TestApplyYAMLAgreesWithTheDotenv` across all four cases.

- [ ] **Step 5: Commit**

```bash
git add internal/report/apply.go internal/report/apply_test.go
git commit -m "feat: generate the apply child pipeline from the deployment methods"
```

---

### Task 5: `plan --environment`

**Files:**
- Modify: `internal/cli/plan.go:15-113`
- Test: `internal/cli/plan_environment_test.go` (create)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `--environment` flag on `blastdoor plan`; `<out-dir>/<unit>/environment.txt` containing the name plus a newline.

- [ ] **Step 1: Write the failing test**

Create `internal/cli/plan_environment_test.go`:

```go
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The flag is recorded per unit rather than once per run, for the same reason
// engine.txt is: eval reads it from another job, and when plans are split
// across a parallel matrix their artifacts are merged. One file per unit
// merges; one file per run collides.
func TestPlanRecordsTheEnvironmentPerUnit(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, ".blastdoor")
	unitDir := filepath.Join(dir, "ops", "int", "topics")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := writeEnvironmentFile(filepath.Join(out, "ops/int/topics"), "int"); err != nil {
		t.Fatalf("writeEnvironmentFile: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(out, "ops/int/topics", "environment.txt"))
	if err != nil {
		t.Fatalf("reading environment.txt: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "int" {
		t.Errorf("environment.txt = %q, want %q", got, "int")
	}
}

func TestWriteEnvironmentFileSkipsAnEmptyName(t *testing.T) {
	dir := t.TempDir()
	if err := writeEnvironmentFile(dir, ""); err != nil {
		t.Fatalf("writeEnvironmentFile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "environment.txt")); !os.IsNotExist(err) {
		t.Error("environment.txt was written for an empty environment; without a wish the feature is off")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestPlanRecords -v`
Expected: FAIL — `undefined: writeEnvironmentFile`.

- [ ] **Step 3: Write minimal implementation**

In `internal/cli/plan.go`, add `environment` to the `var` block (line 16-26):

```go
	var (
		units       []string
		unitsFile   string
		root        string
		baseRef     string
		headRef     string
		outDir      string
		tool        string
		tgTFPath    string
		manager     string
		environment string
	)
```

After the `engine.txt` block (currently lines 90-95), add:

```go
				if err := writeEnvironmentFile(dest, environment); err != nil {
					return err
				}
```

Register the flag next to the others:

```go
	cmd.Flags().StringVar(&environment, "environment", "", "environment these units belong to, recorded beside each plan for 'blastdoor eval' to fold into a deployment method")
```

And add the helper at the end of the file:

```go
// writeEnvironmentFile records which environment a unit belongs to, beside its
// plan.
//
// Per unit rather than once per run, for the same reason engine.txt is: eval
// runs in another job, and when plans are split across a parallel matrix their
// artifacts are merged. One file per unit merges; one file per run collides,
// and the survivor is whichever leg finished last.
//
// An empty name writes nothing. Without a deployment method wish the whole
// feature is off, and a file saying nothing is worse than no file.
func writeEnvironmentFile(dest, environment string) error {
	if environment == "" {
		return nil
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dest, err)
	}
	path := filepath.Join(dest, "environment.txt")
	if err := os.WriteFile(path, []byte(environment+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -v` then `make check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/plan.go internal/cli/plan_environment_test.go
git commit -m "feat: record each unit's environment beside its plan"
```

---

### Task 6: wiring `eval`

**Files:**
- Modify: `internal/cli/eval.go:22-184` (flags and `RunE`), `:196-218` (`judgePlans`), `:289-318` (`writeReport`)
- Test: `internal/cli/eval_deployment_test.go` (create)

**Interfaces:**
- Consumes: `report.ParseWish`, `report.Wish`, `(*report.Report).Decide`, `(report.Report).WriteApplyYAML` from Tasks 1, 2 and 4. The `environment.txt` files Task 5 writes.
- Produces: `--deployment-method-wish` and `--apply-include` flags; `func environmentFor(planFile string) string`; `writeReport(rep report.Report, outDir, applyInclude string) error`.

- [ ] **Step 1: Write the failing test**

Create `internal/cli/eval_deployment_test.go`:

```go
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// planTree writes a minimal but valid plan for one unit, with its environment.
func planTree(t *testing.T, dir, unit, environment string) {
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
	planTree(t, dir, "ops/int/topics", "int")

	got := environmentFor(filepath.Join(dir, "ops/int/topics", "plan.json"))
	if got != "int" {
		t.Errorf("environmentFor = %q, want %q", got, "int")
	}
}

// Missing is silence, not an error. Decide reports it, and only when a wish
// makes it matter.
func TestEnvironmentForIsEmptyWhenNothingRecordedIt(t *testing.T) {
	dir := t.TempDir()
	planTree(t, dir, "ops/int/topics", "")

	if got := environmentFor(filepath.Join(dir, "ops/int/topics", "plan.json")); got != "" {
		t.Errorf("environmentFor = %q, want empty", got)
	}
}

func TestEvalWritesTheApplyPipeline(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, "plans")
	outDir := filepath.Join(dir, "out")
	planTree(t, planDir, "ops/int/topics", "int")

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

// No wish, no generated pipeline: an existing consumer sees no new files.
func TestEvalWritesNoApplyPipelineWithoutAWish(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, "plans")
	outDir := filepath.Join(dir, "out")
	planTree(t, planDir, "ops/int/topics", "int")

	policyDir := filepath.Join(dir, "policy")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "p.rego"), []byte("package blastdoor\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"eval", "--plan-dir", planDir, "--policy", policyDir, "--out-dir", outDir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("eval: %v", err)
	}

	if _, err := os.Stat(filepath.Join(outDir, "apply.gitlab-ci.yml")); !os.IsNotExist(err) {
		t.Error("apply.gitlab-ci.yml was written with no wish stated")
	}
}
```

**Note for the implementer:** `internal/cli/eval_test.go` already drives the command this way — follow its pattern if anything here disagrees with it. `blastdoor eval` writes `report.json`, `summary.md` and `blastdoor.env` into `--out-dir`, so the second test asserts on what is *absent*, not on the command failing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run 'TestEnvironmentFor|TestEvalWrites' -v`
Expected: FAIL to compile — `undefined: environmentFor`, then at runtime `unknown flag: --deployment-method-wish`.

- [ ] **Step 3: Write minimal implementation**

In `internal/cli/eval.go`, add to the `var` block (line 23-36):

```go
		wishFlag     string
		applyInclude string
```

Register the flags next to the others (after line 181):

```go
	// Deliberately not readable from .blastdoor.yml, and this needs no code:
	// the config decoder runs with KnownFields(true), so an `environments:` key
	// rejects the whole file. A branch declaring prd=auto would be a branch
	// arranging its own unattended production apply — the same reasoning that
	// keeps the approver group ids out of the branch's hands in gate.go.
	cmd.Flags().StringVar(&wishFlag, "deployment-method-wish",
		envOr("BLASTDOOR_DEPLOYMENT_METHOD_WISH", ""),
		"per-environment ceiling, e.g. int=auto,stg=auto,prd=manual (env: BLASTDOOR_DEPLOYMENT_METHOD_WISH)")
	cmd.Flags().StringVar(&applyInclude, "apply-include", ".gitlab/blastdoor-apply.yml",
		"file the generated apply pipeline includes for the repository's .blastdoor:apply job")
```

In `RunE`, after the `requireCoverage` block (currently lines 143-149) and before `writeReport`:

```go
			wish, err := report.ParseWish(wishFlag)
			if err != nil {
				return err
			}
			// After the guards, deliberately. Both RequireReview and
			// RequireCoverage force review across the whole repository, and an
			// environment cannot apply unattended while the change is
			// rewriting the rules that judge it — but Decide can only see that
			// once they have recorded it.
			if err := rep.Decide(wish); err != nil {
				return err
			}

			if err := writeReport(rep, outDir, applyInclude); err != nil {
				return err
			}
```

(delete the old `if err := writeReport(rep, outDir); err != nil {` block it replaces)

In `judgePlans`, record the environment (line 215):

```go
		units = append(units, report.Unit{
			Path:        p.name,
			Environment: environmentFor(p.file),
			Changes:     res.Changes,
		})
```

Add next to `enginesFor`:

```go
// environmentFor reads back the environment 'blastdoor plan --environment'
// recorded beside a plan.
//
// A missing file is silence, not an error: plans passed straight to --plan have
// no environment recorded, and neither do plans from a blastdoor old enough not
// to have written one. Report.Decide reports it, and only when a deployment
// method wish makes it matter.
func environmentFor(planFile string) string {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(planFile), "environment.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
```

Replace `writeReport`:

```go
// writeReport writes the files the CI jobs consume.
//
// apply.gitlab-ci.yml is written only when a wish was stated. Without one the
// whole feature is off, and a pipeline that never asked for it should not find
// a new artifact it does not know what to do with.
func writeReport(rep report.Report, outDir, applyInclude string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", outDir, err)
	}

	files := []struct {
		name  string
		write func(io.Writer) error
	}{
		{"report.json", rep.WriteJSON},
		{"summary.md", rep.WriteMarkdown},
		{"blastdoor.env", rep.WriteEnv},
	}

	if len(rep.Environments) > 0 {
		files = append(files, struct {
			name  string
			write func(io.Writer) error
		}{"apply.gitlab-ci.yml", func(w io.Writer) error {
			return rep.WriteApplyYAML(w, applyInclude)
		}})
	}

	for _, f := range files {
		path := filepath.Join(outDir, f.name)
		file, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("creating %s: %w", path, err)
		}
		if err := f.write(file); err != nil {
			file.Close()
			return fmt.Errorf("writing %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("closing %s: %w", path, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -v` then `make check`
Expected: PASS. Any other caller of `writeReport` must be updated for the new third parameter — `go build ./...` will name it.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/eval.go internal/cli/eval_deployment_test.go
git commit -m "feat: decide the deployment method in eval and write the apply pipeline"
```

---

### Task 7: reject the all-zero base ref (Part B1)

**Files:**
- Modify: `internal/detect/detect.go:145-179` (`ResolveBaseRef`)
- Test: `internal/detect/detect_test.go` (append)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `func isZeroSHA(ref string) bool`; an error from `ResolveBaseRef` when `opts.BaseRef` is the all-zero SHA.

- [ ] **Step 1: Write the failing test**

Append to `internal/detect/detect_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/detect/ -run TestResolveBaseRefRejects -v`
Expected: FAIL — `ResolveBaseRef = nil error, want one` (the all-zero string is returned unchanged today).

- [ ] **Step 3: Write minimal implementation**

In `internal/detect/detect.go`, replace the opening of `ResolveBaseRef`:

```go
func ResolveBaseRef(opts Options) (string, error) {
	if opts.BaseRef != "" {
		// GitLab sets CI_COMMIT_BEFORE_SHA to all zeros on a branch's first
		// pipeline, which is what the default-branch jobs pass as --base-ref.
		// Git resolves it to nothing, and the failure that follows names a SHA
		// nobody wrote, in a job whose real problem is that there is no
		// previous commit.
		if isZeroSHA(opts.BaseRef) {
			return "", fmt.Errorf(
				"base ref %q is the all-zero SHA, which is what CI_COMMIT_BEFORE_SHA holds on a "+
					"branch's first pipeline: there is no previous commit to diff against",
				opts.BaseRef)
		}
		return opts.BaseRef, nil
	}
```

and add at the end of the file:

```go
// isZeroSHA reports whether a ref is git's all-zero object id.
//
// Both widths, because a repository on sha256 gets a 64-character one.
func isZeroSHA(ref string) bool {
	if len(ref) != 40 && len(ref) != 64 {
		return false
	}
	return strings.Trim(ref, "0") == ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/detect/ -v` then `make check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/detect/detect.go internal/detect/detect_test.go
git commit -m "fix: reject the all-zero base ref instead of diffing against nothing"
```

---

### Task 8: the GitLab template and the docs

**Files:**
- Modify: `ci/gitlab/blastdoor.yml`
- Modify: `docs/gitlab.md`, `docs/hardening.md`, `docs/verdicts.md`
- Modify: `AGENTS.md`

**Interfaces:**
- Consumes: every flag and artifact from Tasks 1-7.
- Produces: no Go symbols. The template consumers include.

- [ ] **Step 1: Add the wish and the environment to the template**

In `ci/gitlab/blastdoor.yml`, under `variables:`:

```yaml
  # The ceiling for each environment's apply, stated by the pipeline rather than
  # by .blastdoor.yml. A branch declaring prd=auto would be a branch arranging
  # its own unattended production apply, so this is not readable from the
  # repository's config at all — blastdoor rejects an `environments:` key there.
  #
  # Leave it empty and the whole feature is off: no new dotenv keys, no
  # generated pipeline, no change in behaviour.
  BLASTDOOR_DEPLOYMENT_METHOD_WISH: ""
  # The file the generated apply pipeline includes, holding the repository's own
  # .blastdoor:apply job — its image, its credentials, its apply command.
  BLASTDOOR_APPLY_INCLUDE: .gitlab/blastdoor-apply.yml
```

Add the apply include to the guard list. `BLASTDOOR_GUARD_PATHS` is a
space-separated string; append the literal path — **not** `$BLASTDOOR_APPLY_INCLUDE`,
because the guard list is read by a shell loop with globbing disabled and a
variable there would not be expanded when the pipeline that states it is the
thing being guarded. Read the existing value in the file and add one entry, so
it becomes:

```yaml
  # .gitlab/blastdoor-apply.yml is guarded because it is the script that applies
  # infrastructure. A merge request that can rewrite it unreviewed can run
  # anything in a job holding production credentials, after the gate has already
  # passed. This ranks with guarding the policy directory itself.
  BLASTDOOR_GUARD_PATHS: "$BLASTDOOR_POLICY_DIR .gitlab-ci.yml .blastdoor.yml .gitlab/blastdoor-apply.yml"
```

Keep whatever entries the file already lists; the point is the one new path at
the end.

- [ ] **Step 2: Pass the environment through the plan job**

In the plan job's script, add `--environment "$ENV"` to the `blastdoor plan` call, so it reads:

```yaml
    - blastdoor plan --units-file "units.${ENV}.txt" --out-dir "$BLASTDOOR_DIR" --environment "$ENV"
```

- [ ] **Step 3: Publish the generated pipeline from the eval job**

In the eval job, add the wish and include flags to the `blastdoor eval` call, and the generated file to its artifacts:

```yaml
      blastdoor eval \
        --plan-dir "$BLASTDOOR_DIR" \
        --out-dir "$BLASTDOOR_DIR" \
        --deployment-method-wish "$BLASTDOOR_DEPLOYMENT_METHOD_WISH" \
        --apply-include "$BLASTDOOR_APPLY_INCLUDE" \
        $guards
```

```yaml
  artifacts:
    reports:
      dotenv: $BLASTDOOR_DIR/blastdoor.env
    paths:
      - $BLASTDOOR_DIR/report.json
      - $BLASTDOOR_DIR/summary.md
      - $BLASTDOOR_DIR/apply.gitlab-ci.yml
    expire_in: 1 day
```

With no wish stated, `apply.gitlab-ci.yml` is not written and GitLab logs a
warning for the missing path rather than failing the job. That is the right
trade: listing it conditionally would need a second job definition, and the
`blastdoor:apply` trigger does not run without a wish anyway.

- [ ] **Step 4: Add the default-branch jobs and the trigger**

Add a rules anchor beside the existing `default`:

```yaml
.blastdoor:rules:
  default:
    - if: $CI_COMMIT_BRANCH && $CI_COMMIT_BRANCH != $CI_DEFAULT_BRANCH
  # The apply side. Only on the default branch, and only when a wish was
  # stated — without one there is no generated pipeline to trigger.
  main:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH && $BLASTDOOR_DEPLOYMENT_METHOD_WISH != ""
```

The default-branch plan and eval jobs are the merge-request ones with two
differences: the `main` rule, and an explicit base ref.

Add the `main` rule to both jobs' `rules:` lists, alongside the existing
`!reference [".blastdoor:rules", default]`, so each runs on both kinds of
pipeline:

```yaml
  rules:
    - !reference [".blastdoor:rules", default]
    - !reference [".blastdoor:rules", main]
```

In the plan job's `script:`, before `blastdoor detect`, build the base-ref
argument. It is built as a shell variable rather than passed unconditionally,
because an empty `--base-ref ""` is not the same as omitting the flag — the
merge-request path relies on blastdoor resolving the base itself:

```yaml
    # On the default branch there is no merge base to diff against — it IS the
    # default branch, so the merge base is HEAD and the diff would be empty.
    # The previous commit is what "what did this change" means here. Blastdoor
    # rejects the all-zero value GitLab puts in CI_COMMIT_BEFORE_SHA on a
    # branch's first pipeline rather than diffing against nothing.
    - |
      base_ref_arg=""
      if [ "$CI_COMMIT_BRANCH" = "$CI_DEFAULT_BRANCH" ]; then
        base_ref_arg="--base-ref $CI_COMMIT_BEFORE_SHA"
      fi
```

Then use it, unquoted so an empty value expands to nothing, in **both** calls
that take a diff:

```yaml
    - blastdoor detect $base_ref_arg | tee units.all.txt
```

```yaml
    - blastdoor plan --units-file "units.${ENV}.txt" --out-dir "$BLASTDOOR_DIR" --environment "$ENV" $base_ref_arg
```

The eval job needs it too, for `--guard-path`:

```yaml
      blastdoor eval \
        --plan-dir "$BLASTDOOR_DIR" \
        --out-dir "$BLASTDOOR_DIR" \
        --deployment-method-wish "$BLASTDOOR_DEPLOYMENT_METHOD_WISH" \
        --apply-include "$BLASTDOOR_APPLY_INCLUDE" \
        $base_ref_arg \
        $guards
```

with the same `base_ref_arg` block added to the eval job's script. Then the
trigger:

```yaml
# Runs the pipeline 'blastdoor eval' generated, whose jobs carry a literal
# when: — which is the only way this decision can reach GitLab. `when:` does not
# expand a variable, and rules: are evaluated before blastdoor.env exists.
blastdoor:apply:
  stage: apply
  needs:
    - job: blastdoor:eval
      artifacts: true
  rules:
    - !reference [".blastdoor:rules", main]
  trigger:
    include:
      - artifact: $BLASTDOOR_DIR/apply.gitlab-ci.yml
        job: blastdoor:eval
    strategy: depend
```

Add `apply` to the documented `stages:` list in the file's header comment.

- [ ] **Step 5: Verify the template parses**

Run:

```bash
python3 -c "import yaml,sys; yaml.safe_load(open('ci/gitlab/blastdoor.yml')); print('ok')"
```

Expected: `ok`. This catches indentation mistakes; it does not validate GitLab's schema.

- [ ] **Step 6: Document it**

- `docs/gitlab.md` — a section on the deployment method: the wish variable, what the repository must put in `.gitlab/blastdoor-apply.yml` (a `.blastdoor:apply` hidden job reading `$BLASTDOOR_ENV`), and the `auto|manual|none` values. State plainly that the dotenv is a record and the generated pipeline is the mechanism, with the reason — otherwise the first reader will try to use the variable in `rules:` and it will silently never match.
- `docs/hardening.md` — two entries: the apply include must be guarded, and the wish is pipeline-only by design.
- `docs/verdicts.md` — how a verdict becomes a method, and that the wish is a ceiling.
- `AGENTS.md` — under "Load-bearing decisions", two entries:
  - **The deployment method wish is not config.** Same reasoning as the approver group ids. A branch declaring `prd=auto` is a branch arranging its own unattended production apply.
  - **`none` is tested before `auto` in `Report.Decide`.** An environment with no changed units has a vacuously passing verdict, so the other order makes every untouched environment `auto` and generates an apply job for it.

- [ ] **Step 7: Commit**

```bash
git add ci/gitlab/blastdoor.yml docs/ AGENTS.md
git commit -m "docs: document the per-environment deployment method"
```

---

## Verification

- [ ] `make check` passes — fmt, vet, `go test -race ./...`.
- [ ] `make build && ./bin/blastdoor eval --plan examples/plans/kafka-topic-create.json --policy examples/policies --out-dir /tmp/bd` writes no `apply.gitlab-ci.yml` and no `BLASTDOOR_DEPLOY_*` keys — the feature is off without a wish.
- [ ] The same with `--deployment-method-wish int=auto` fails, naming the unit with no environment recorded.
- [ ] `make examples` still passes.

## Out of scope

**Part B2 — `verify-decision`.** Recording the decision as a block in the gate's note, reading it back through the GitLab API filtered by the token's own user id, and failing the apply on any disagreement. It needs `gate.go`, a new `verify.go`, and three new `gitlabapi` methods. Ship B1 first: running it is what produces the evidence for whether strict-both-directions is livable, which is the open question the spec records.

Do not implement it as part of this plan.
