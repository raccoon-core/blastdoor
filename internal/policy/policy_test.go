package policy

import (
	"context"
	"os"
	"testing"
)

func change(address, resourceType string, actions ...string) map[string]any {
	acts := make([]any, len(actions))
	for i, a := range actions {
		acts[i] = a
	}
	return map[string]any{
		"address": address,
		"type":    resourceType,
		"change":  map[string]any{"actions": acts},
	}
}

func planDoc(changes ...map[string]any) map[string]any {
	entries := make([]any, len(changes))
	for i, c := range changes {
		entries[i] = c
	}
	return map[string]any{"format_version": "1.2", "resource_changes": entries}
}

func writePolicy(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/policy.rego", []byte(body), 0o600); err != nil {
		t.Fatalf("writing policy: %v", err)
	}
	return dir
}

func judge(t *testing.T, policyBody string, plan map[string]any) Result {
	t.Helper()
	opts := Options{}
	if policyBody != "" {
		opts.PolicyPaths = []string{writePolicy(t, policyBody)}
	}

	e, err := New(context.Background(), opts)
	if err != nil {
		t.Fatalf("compiling policies: %v", err)
	}
	res, err := e.Evaluate(context.Background(), plan)
	if err != nil {
		t.Fatalf("evaluating: %v", err)
	}
	return res
}

// verdictFor finds one change's verdict.
func verdictFor(t *testing.T, res Result, address string) Change {
	t.Helper()
	for _, c := range res.Changes {
		if c.Address == address {
			return c
		}
	}
	t.Fatalf("no verdict for %s in %+v", address, res.Changes)
	return Change{}
}

// The core promise: no rule, no pass — but not a hard block either.
func TestUnjudgedChangeIsSentToReview(t *testing.T) {
	res := judge(t, "", planDoc(change("aws_iam_policy.admin", "aws_iam_policy", "create")))

	if res.Verdict != Review {
		t.Errorf("verdict = %q, want %q", res.Verdict, Review)
	}
	c := verdictFor(t, res, "aws_iam_policy.admin")
	if c.Verdict != Review {
		t.Errorf("change verdict = %q, want %q", c.Verdict, Review)
	}
	if len(c.Reasons) != 1 || c.Reasons[0] != ReasonUnjudged {
		t.Errorf("reasons = %v, want [%q]", c.Reasons, ReasonUnjudged)
	}
	if len(res.Unjudged()) != 1 {
		t.Errorf("Unjudged() = %v, want one change", res.Unjudged())
	}
}

// Every action shape needs a rule; none of them slips through as a pass.
func TestEveryActionNeedsARule(t *testing.T) {
	for _, actions := range [][]string{
		{"create"}, {"update"}, {"delete"},
		{"create", "delete"}, {"delete", "create"}, {"read"},
	} {
		res := judge(t, "", planDoc(change("x.y", "x", actions...)))
		if res.Verdict != Review {
			t.Errorf("actions %v: verdict = %q, want %q", actions, res.Verdict, Review)
		}
	}
}

// A no-op changes nothing, so it needs no rule and does not appear.
func TestNoOpIsNotJudged(t *testing.T) {
	res := judge(t, "", planDoc(change("x.y", "x", "no-op")))

	if len(res.Changes) != 0 {
		t.Errorf("changes = %+v, want none", res.Changes)
	}
	if res.Verdict != Pass {
		t.Errorf("verdict = %q, want %q", res.Verdict, Pass)
	}
}

const allowEverything = `package blastdoor

allow contains {"resource": rc.address, "reason": "fine"} if {
	some rc in input.resource_changes
}
`

func TestAllowLetsAChangePass(t *testing.T) {
	res := judge(t, allowEverything, planDoc(change("x.y", "x", "create")))

	if res.Verdict != Pass {
		t.Errorf("verdict = %q, want %q", res.Verdict, Pass)
	}
}

// The worst verdict wins, so adding a rule can only ever make a change
// stricter — never weaker.
func TestMostSevereVerdictWins(t *testing.T) {
	tests := []struct {
		name   string
		policy string
		want   Verdict
	}{
		{
			"allow and review",
			allowEverything + `
review contains {"resource": rc.address, "reason": "look at it"} if {
	some rc in input.resource_changes
}
`,
			Review,
		},
		{
			"allow and deny",
			allowEverything + `
deny contains {"resource": rc.address, "reason": "never"} if {
	some rc in input.resource_changes
}
`,
			Deny,
		},
		{
			"review and deny",
			`package blastdoor

review contains {"resource": rc.address, "reason": "look"} if {
	some rc in input.resource_changes
}

deny contains {"resource": rc.address, "reason": "never"} if {
	some rc in input.resource_changes
}
`,
			Deny,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := judge(t, tc.policy, planDoc(change("x.y", "x", "create")))
			if res.Verdict != tc.want {
				t.Errorf("verdict = %q, want %q", res.Verdict, tc.want)
			}
		})
	}
}

// The plan takes the worst verdict of any change in it, with no arithmetic:
// a hundred passing changes do not outvote one denial.
func TestPlanTakesTheWorstChange(t *testing.T) {
	policy := allowEverything + `
deny contains {"resource": "bad.one", "reason": "never"} if { true }
`
	res := judge(t, policy, planDoc(
		change("good.a", "good", "create"),
		change("good.b", "good", "create"),
		change("bad.one", "bad", "create"),
	))

	if res.Verdict != Deny {
		t.Errorf("verdict = %q, want %q", res.Verdict, Deny)
	}
	counts := res.Counts()
	if counts[Pass] != 2 || counts[Deny] != 1 {
		t.Errorf("counts = %v, want 2 pass and 1 deny", counts)
	}
	// The worst change is listed first, so the summary leads with it.
	if res.Changes[0].Address != "bad.one" {
		t.Errorf("changes are not worst-first: %+v", res.Changes)
	}
}

// Allowing one shape must not allow a neighbouring one.
func TestAllowIsNarrow(t *testing.T) {
	policy := `package blastdoor

allow contains {"resource": rc.address, "reason": "creating is fine"} if {
	some rc in input.resource_changes
	rc.change.actions == ["create"]
}
`
	res := judge(t, policy, planDoc(
		change("x.created", "x", "create"),
		change("x.deleted", "x", "delete"),
	))

	if verdictFor(t, res, "x.created").Verdict != Pass {
		t.Error("the create should pass")
	}
	if got := verdictFor(t, res, "x.deleted").Verdict; got != Review {
		t.Errorf("the delete verdict = %q, want %q — it was never judged", got, Review)
	}
}

// A judgement that names no resource cannot be attached to a change, so it is
// an error rather than something silently dropped.
func TestJudgementWithoutResourceIsAnError(t *testing.T) {
	dir := writePolicy(t, `package blastdoor

allow contains {"reason": "no resource named"} if { true }
`)
	e, err := New(context.Background(), Options{PolicyPaths: []string{dir}})
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}
	if _, err := e.Evaluate(context.Background(), planDoc(change("x.y", "x", "create"))); err == nil {
		t.Fatal("a judgement with no resource was accepted")
	}
}

// Every judgement has to say why, because that is what the reviewer reads.
func TestJudgementWithoutReasonIsAnError(t *testing.T) {
	dir := writePolicy(t, `package blastdoor

allow contains {"resource": rc.address} if {
	some rc in input.resource_changes
}
`)
	e, err := New(context.Background(), Options{PolicyPaths: []string{dir}})
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}
	if _, err := e.Evaluate(context.Background(), planDoc(change("x.y", "x", "create"))); err == nil {
		t.Fatal("a judgement with no reason was accepted")
	}
}

func TestBrokenPolicyReportsCompileError(t *testing.T) {
	dir := writePolicy(t, "package blastdoor\n\nthis is not rego\n")

	if _, err := New(context.Background(), Options{PolicyPaths: []string{dir}}); err == nil {
		t.Fatal("expected a compile error")
	}
}

// Anything blastdoor cannot read as a plan is an error, never an empty plan
// that passes.
func TestValidatePlanRejectsNonPlans(t *testing.T) {
	tests := []struct {
		name string
		doc  any
	}{
		{"empty object", map[string]any{}},
		{"json array", []any{}},
		{"json string", "not a plan"},
		{"null", nil},
		{"no format_version", map[string]any{"resource_changes": []any{}}},
		{"state output", map[string]any{"format_version": "1.0", "values": map[string]any{}}},
		{"resource_changes of the wrong type", map[string]any{"format_version": "1.2", "resource_changes": map[string]any{}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePlan(tc.doc); err == nil {
				t.Errorf("%s was accepted as a plan", tc.name)
			}
		})
	}
}

func TestValidatePlanAcceptsRealPlans(t *testing.T) {
	tests := []struct {
		name string
		doc  any
	}{
		{"plan with changes", planDoc(change("a.b", "a", "create"))},
		{"plan with no changes", map[string]any{"format_version": "1.2", "resource_changes": []any{}}},
		{"empty plan keeping only planned_values", map[string]any{"format_version": "1.2", "planned_values": map[string]any{}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePlan(tc.doc); err != nil {
				t.Errorf("a real plan was rejected: %v", err)
			}
		})
	}
}

// An allow rule can name, per environment, whether it considers that
// environment safe to automate.
func TestAllowCanNameDeploymentMethod(t *testing.T) {
	policy := `package blastdoor

allow contains {"resource": rc.address, "reason": "fine", "deployment_method": {"int": "auto", "prd": "auto", "stg": "manual"}} if {
	some rc in input.resource_changes
}
`
	res := judge(t, policy, planDoc(change("x.y", "x", "create")))
	c := verdictFor(t, res, "x.y")
	want := map[string]string{"int": "auto", "prd": "auto"}
	if !stringsMapEqual(c.DeploymentMethod, want) {
		t.Errorf("DeploymentMethod = %v, want %v — stg was named \"manual\", so it should not appear", c.DeploymentMethod, want)
	}
}

// A rule silent on automation is the default: it passes the change but
// vouches for nothing.
func TestDeploymentMethodIsEmptyWithoutAnnotation(t *testing.T) {
	res := judge(t, allowEverything, planDoc(change("x.y", "x", "create")))
	c := verdictFor(t, res, "x.y")
	if len(c.DeploymentMethod) != 0 {
		t.Errorf("DeploymentMethod = %v, want none: the rule never mentioned it", c.DeploymentMethod)
	}
}

// Two allow rules can match the same change, and both have to say "auto" for
// an environment before it counts — the same way one denying rule is enough
// to keep the whole change from passing.
func TestDeploymentMethodIsTheIntersectionOfEveryMatchingRule(t *testing.T) {
	policy := `package blastdoor

allow contains {"resource": rc.address, "reason": "narrow rule", "deployment_method": {"int": "auto", "stg": "auto", "prd": "auto"}} if {
	some rc in input.resource_changes
	rc.type == "x"
}

allow contains {"resource": rc.address, "reason": "wide rule, silent on automation"} if {
	some rc in input.resource_changes
}
`
	res := judge(t, policy, planDoc(change("x.y", "x", "create")))
	c := verdictFor(t, res, "x.y")
	if len(c.DeploymentMethod) != 0 {
		t.Errorf("DeploymentMethod = %v, want none: one matching rule named no environment at all", c.DeploymentMethod)
	}
}

// A change sent to review is never a candidate for automation, whatever an
// allow rule matching the same resource said.
func TestDeploymentMethodIsClearedWhenTheVerdictIsNotPass(t *testing.T) {
	policy := `package blastdoor

allow contains {"resource": rc.address, "reason": "fine", "deployment_method": {"int": "auto"}} if {
	some rc in input.resource_changes
}

review contains {"resource": rc.address, "reason": "look at it"} if {
	some rc in input.resource_changes
}
`
	res := judge(t, policy, planDoc(change("x.y", "x", "create")))
	c := verdictFor(t, res, "x.y")
	if c.Verdict != Review {
		t.Fatalf("verdict = %q, want %q", c.Verdict, Review)
	}
	if len(c.DeploymentMethod) != 0 {
		t.Errorf("DeploymentMethod = %v, want none: the verdict is review, not pass", c.DeploymentMethod)
	}
}

// A rule naming a method other than "auto" or "manual" is a mistake to
// report, not a value to guess about.
func TestDeploymentMethodRejectsAnUnknownValue(t *testing.T) {
	policy := `package blastdoor

allow contains {"resource": rc.address, "reason": "fine", "deployment_method": {"int": "sometimes"}} if {
	some rc in input.resource_changes
}
`
	dir := writePolicy(t, policy)
	e, err := New(context.Background(), Options{PolicyPaths: []string{dir}})
	if err != nil {
		t.Fatalf("compiling policies: %v", err)
	}
	if _, err := e.Evaluate(context.Background(), planDoc(change("x.y", "x", "create"))); err == nil {
		t.Error("Evaluate = nil error, want one: \"sometimes\" is not \"auto\" or \"manual\"")
	}
}

func stringsMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestWorse(t *testing.T) {
	tests := []struct {
		a, b, want Verdict
	}{
		{Pass, Pass, Pass},
		{Pass, Review, Review},
		{Review, Pass, Review},
		{Review, Deny, Deny},
		{Deny, Pass, Deny},
		{Deny, Deny, Deny},
	}
	for _, tc := range tests {
		if got := Worse(tc.a, tc.b); got != tc.want {
			t.Errorf("Worse(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}
