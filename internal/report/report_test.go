package report

import (
	"strings"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/policy"
)

func change(address string, v policy.Verdict, reasons ...string) policy.Change {
	return policy.Change{Address: address, Verdict: v, Reasons: reasons, Actions: []string{"create"}}
}

// autoChange is a Pass change whose rule vouched for the named environments —
// the Decide tests below fold a wish and this together the way blastdoor
// eval does, so a test wanting "auto" now has to earn it on both counts.
func autoChange(address string, envs ...string) policy.Change {
	method := map[string]string{}
	for _, e := range envs {
		method[e] = "auto"
	}
	return policy.Change{Address: address, Verdict: policy.Pass, Reasons: []string{"fine"}, Actions: []string{"create"}, DeploymentMethod: method}
}

// The plan takes the worst verdict anywhere in it — no arithmetic.
func TestBuildTakesTheWorstVerdict(t *testing.T) {
	tests := []struct {
		name  string
		units []Unit
		want  policy.Verdict
	}{
		{"all pass", []Unit{{Path: "a", Changes: []policy.Change{change("x", policy.Pass, "fine")}}}, policy.Pass},
		{
			"one review among passes",
			[]Unit{{Path: "a", Changes: []policy.Change{
				change("x", policy.Pass, "fine"),
				change("y", policy.Review, "look"),
			}}},
			policy.Review,
		},
		{
			"one deny outweighs many passes",
			[]Unit{{Path: "a", Changes: []policy.Change{
				change("x", policy.Pass, "fine"),
				change("y", policy.Pass, "fine"),
				change("z", policy.Deny, "never"),
			}}},
			policy.Deny,
		},
		{
			"worst verdict crosses units",
			[]Unit{
				{Path: "a", Changes: []policy.Change{change("x", policy.Pass, "fine")}},
				{Path: "b", Changes: []policy.Change{change("y", policy.Deny, "never")}},
			},
			policy.Deny,
		},
		{"no units", nil, policy.Pass},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := Build(tc.units)
			if rep.Verdict != tc.want {
				t.Errorf("verdict = %q, want %q", rep.Verdict, tc.want)
			}
		})
	}
}

func TestBuildCountsAndSortsUnits(t *testing.T) {
	rep := Build([]Unit{
		{Path: "b", Changes: []policy.Change{change("y", policy.Deny, "never")}},
		{Path: "a", Changes: []policy.Change{change("x", policy.Pass, "fine"), change("z", policy.Review, "look")}},
	})

	if rep.Units[0].Path != "a" {
		t.Errorf("units are not sorted: %+v", rep.Units)
	}
	if rep.Units[0].Verdict != policy.Review {
		t.Errorf("unit a verdict = %q, want %q", rep.Units[0].Verdict, policy.Review)
	}
	if rep.Counts[policy.Pass] != 1 || rep.Counts[policy.Review] != 1 || rep.Counts[policy.Deny] != 1 {
		t.Errorf("counts = %v", rep.Counts)
	}
}

// The guard forces at least review, and never softens a denial.
func TestRequireReview(t *testing.T) {
	passing := Build([]Unit{{Path: "a", Changes: []policy.Change{change("x", policy.Pass, "fine")}}})
	passing.RequireReview([]string{"policy/x.rego"})
	if passing.Verdict != policy.Review {
		t.Errorf("verdict = %q, want %q", passing.Verdict, policy.Review)
	}

	denied := Build([]Unit{{Path: "a", Changes: []policy.Change{change("x", policy.Deny, "never")}}})
	denied.RequireReview([]string{"policy/x.rego"})
	if denied.Verdict != policy.Deny {
		t.Errorf("a denial was softened to %q", denied.Verdict)
	}

	untouched := Build([]Unit{{Path: "a", Changes: []policy.Change{change("x", policy.Pass, "fine")}}})
	untouched.RequireReview(nil)
	if untouched.Verdict != policy.Pass {
		t.Errorf("verdict = %q, want %q", untouched.Verdict, policy.Pass)
	}
}

func TestWriteEnv(t *testing.T) {
	rep := Build([]Unit{{Path: "a", Changes: []policy.Change{
		change("x", policy.Pass, "fine"),
		change("y", policy.Deny, "never"),
	}}})

	var b strings.Builder
	if err := rep.WriteEnv(&b); err != nil {
		t.Fatalf("WriteEnv: %v", err)
	}

	for _, want := range []string{
		"BLASTDOOR_VERDICT=deny",
		"BLASTDOOR_UNIT_COUNT=1",
		"BLASTDOOR_PASS_COUNT=1",
		"BLASTDOOR_DENY_COUNT=1",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("dotenv is missing %q:\n%s", want, b.String())
		}
	}
}

// A denial says so plainly, and names what caused it.
func TestWriteMarkdownForDeny(t *testing.T) {
	rep := Build([]Unit{{Path: "terraform/prd", Changes: []policy.Change{
		change("aws_iam_policy.admin", policy.Deny, policy.ReasonUnjudged),
	}}})

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}

	for _, want := range []string{"Denied", "aws_iam_policy.admin", "terraform/prd", "no policy at all", "Approving does not clear this"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("summary is missing %q:\n%s", want, b.String())
		}
	}
}

func TestWriteMarkdownForPassAndReview(t *testing.T) {
	pass := Build([]Unit{{Path: "u", Changes: []policy.Change{change("x", policy.Pass, "additive")}}})
	var b strings.Builder
	if err := pass.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	if !strings.Contains(b.String(), "Pass") || !strings.Contains(b.String(), "additive") {
		t.Errorf("pass summary:\n%s", b.String())
	}

	review := Build([]Unit{{Path: "u", Changes: []policy.Change{change("x", policy.Review, "someone look")}}})
	b.Reset()
	if err := review.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	if !strings.Contains(b.String(), "Review required") || !strings.Contains(b.String(), "someone look") {
		t.Errorf("review summary:\n%s", b.String())
	}
}

// Zero units must not read as approval.
func TestWriteMarkdownWithNoUnits(t *testing.T) {
	var b strings.Builder
	if err := Build(nil).WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	if !strings.Contains(b.String(), "nothing here has been checked") {
		t.Errorf("summary:\n%s", b.String())
	}
}

func TestWriteMarkdownEscapesPipes(t *testing.T) {
	rep := Build([]Unit{{Path: "u", Changes: []policy.Change{change("x", policy.Pass, "a | b")}}})

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	if !strings.Contains(b.String(), `a \| b`) {
		t.Errorf("pipe was not escaped:\n%s", b.String())
	}
}

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
		{Path: "ops/int/a", Environment: "int", Changes: []policy.Change{autoChange("x", "int")}},
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
		{Path: "ops/int/a", Environment: "int", Changes: []policy.Change{autoChange("x", "int")}},
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
