package report

import (
	"testing"

	"github.com/raccoon-core/blastdoor/internal/policy"
)

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
		// ı (U+0131, dotless i) uppercases to ASCII I via strings.ToUpper,
		// so validating the uppercased form would let it through raw and
		// then store it unvalidated. Validation must run on the raw name.
		{"non-ASCII name that uppercases into ASCII", "ı=auto"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseWish(tc.input); err == nil {
				t.Errorf("ParseWish(%q) = nil error, want one", tc.input)
			}
		})
	}
}

func TestParseWishToleratesSpacingAndTrailingComma(t *testing.T) {
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

// unit builds a report unit in one environment with one change.
func unit(path, env string, v policy.Verdict) Unit {
	return Unit{
		Path:        path,
		Environment: env,
		Changes:     []policy.Change{{Address: "x", Verdict: v, Reasons: []string{"because"}, Actions: []string{"create"}}},
	}
}

// autoUnit builds a unit whose one change is Pass and vouched safe to
// automate in exactly the named environments.
func autoUnit(path, env string, auto ...string) Unit {
	method := map[string]string{}
	for _, e := range auto {
		method[e] = "auto"
	}
	return Unit{
		Path:        path,
		Environment: env,
		Changes:     []policy.Change{{Address: "x", Verdict: policy.Pass, Reasons: []string{"because"}, Actions: []string{"create"}, DeploymentMethod: method}},
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
	rep := Build([]Unit{autoUnit("ops/int/a", "int", "int")})
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

// No wish and no unit carrying an environment means the feature is off,
// including its errors: a repository that has not arranged per-environment
// planning sees no change in behaviour just because blastdoor gained one.
func TestDecideWithoutAWishOrEnvironmentsIsInert(t *testing.T) {
	rep := Build([]Unit{unit("ops/a", "", policy.Pass)})
	if err := rep.Decide(Wish{}); err != nil {
		t.Fatalf("Decide with no wish: %v", err)
	}
	if len(rep.Environments) != 0 {
		t.Errorf("Environments = %v, want none", rep.Environments)
	}
}

// The main promise of this change: an environment can go auto with no wish
// stated at all, purely because policy's own allow rules vouched for it.
func TestDecideAutoWithNoWishAtAll(t *testing.T) {
	rep := Build([]Unit{autoUnit("ops/int/a", "int", "int", "stg", "prd")})
	if err := rep.Decide(Wish{}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got := methodFor(t, rep, "int"); got != Auto {
		t.Errorf("int = %q, want auto: policy named int safe to automate and nothing else objects", got)
	}
}

// A Pass verdict alone is not enough: the specific allow rule that matched
// still has to have named this environment, or the change stays manual.
func TestDecideStaysManualWithoutPolicyAutoEvenWhenPass(t *testing.T) {
	rep := Build([]Unit{unit("ops/int/a", "int", policy.Pass)}) // unit() sets no Auto
	if err := rep.Decide(Wish{}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	d := rep.Environments[0]
	if d.Method != Manual {
		t.Errorf("method = %q, want manual", d.Method)
	}
	if !containsString(d.Reasons, "no policy rule marked this environment safe to automate") {
		t.Errorf("reasons = %v, want one explaining no rule vouched for automation", d.Reasons)
	}
}

// A change that vouches for stg does not thereby vouch for int: automation
// is per environment, not a blanket "this unit is fine".
func TestDecideAutoIsPerEnvironment(t *testing.T) {
	rep := Build([]Unit{autoUnit("ops/int/a", "int", "stg", "prd")}) // int's own unit, but the rule only named stg and prd
	if err := rep.Decide(Wish{}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got := methodFor(t, rep, "int"); got != Manual {
		t.Errorf("int = %q, want manual: no rule named int itself", got)
	}
}

// Every unit in an environment has to be auto-eligible for the environment to
// go auto — one plain unit alongside an auto-vouched one still holds it back.
func TestDecideAutoRequiresEveryUnitToAgree(t *testing.T) {
	rep := Build([]Unit{
		autoUnit("ops/int/a", "int", "int"),
		unit("ops/int/b", "int", policy.Pass), // no rule vouched for this one
	})
	if err := rep.Decide(Wish{}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got := methodFor(t, rep, "int"); got != Manual {
		t.Errorf("int = %q, want manual: one unit in it has no policy vouching for automation", got)
	}
}

// A stated wish is still a ceiling on top of what policy allows: it can only
// narrow, never widen, what an allow rule already vouched for.
func TestDecideWishStillCeilingsPolicyAuto(t *testing.T) {
	rep := Build([]Unit{autoUnit("ops/prd/a", "prd", "prd")})
	w, err := ParseWish("prd=manual")
	if err != nil {
		t.Fatalf("ParseWish: %v", err)
	}
	if err := rep.Decide(w); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got := methodFor(t, rep, "prd"); got != Manual {
		t.Errorf("prd = %q, want manual: the wish asked for manual even though policy alone would have allowed auto", got)
	}
}

// Environments come from what the units recorded, not from a wish naming
// them — a wish that names nothing extra beyond the units still gets a
// decision for every environment those units carry.
func TestDecideEnvironmentsComeFromUnitsWithoutAWish(t *testing.T) {
	rep := Build([]Unit{
		autoUnit("ops/int/a", "int", "int"),
		unit("ops/prd/a", "prd", policy.Review),
	})
	if err := rep.Decide(Wish{}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if len(rep.Environments) != 2 {
		t.Fatalf("Environments = %+v, want 2", rep.Environments)
	}
	if got := methodFor(t, rep, "int"); got != Auto {
		t.Errorf("int = %q, want auto", got)
	}
	if got := methodFor(t, rep, "prd"); got != Manual {
		t.Errorf("prd = %q, want manual: the verdict here is review", got)
	}
}
