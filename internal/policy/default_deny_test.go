package policy

import (
	"context"
	"testing"
)

// change builds one resource_changes entry.
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
	return map[string]any{
		"format_version":   "1.2",
		"resource_changes": entries,
	}
}

// The core promise: with no policies loaded, every shape of real change is
// scored at the maximum. If any row here starts passing, the door is open.
func TestDefaultDenyCoversEveryAction(t *testing.T) {
	actions := [][]string{
		{"create"},
		{"update"},
		{"delete"},
		{"create", "delete"}, // replace
		{"delete", "create"}, // replace, the other ordering
		{"read"},             // a data source read
	}

	for _, acts := range actions {
		t.Run(joined(acts), func(t *testing.T) {
			findings := evaluate(t, Options{}, planDoc(change("some_resource.x", "some_resource", acts...)))

			if len(findings) != 1 {
				t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
			}
			if findings[0].Score != DefaultScore {
				t.Errorf("score = %d, want %d — this action slips past the backstop", findings[0].Score, DefaultScore)
			}
			if findings[0].Allowed() {
				t.Error("an unclassified change reports as allowed")
			}
		})
	}
}

// The one exception, and it must stay the only one: a no-op changes nothing.
func TestDefaultDenyAllowsOnlyNoOp(t *testing.T) {
	findings := evaluate(t, Options{}, planDoc(change("some_resource.x", "some_resource", "no-op")))

	if len(findings) != 0 {
		t.Fatalf("a no-op should produce no findings, got %+v", findings)
	}
}

// Scores add up, so a plan doing many unclassified things scores higher than
// one doing a single unclassified thing.
func TestDefaultDenyAccumulatesAcrossResources(t *testing.T) {
	findings := evaluate(t, Options{}, planDoc(
		change("a.one", "a", "create"),
		change("b.two", "b", "delete"),
		change("c.three", "c", "update"),
	))

	total := 0
	for _, f := range findings {
		total += f.Score
	}
	if want := 3 * DefaultScore; total != want {
		t.Errorf("total = %d, want %d", total, want)
	}
}

// Green-flagging is the intended way through: a policy scores a change 0 and
// claims it.
func TestPolicyCanGreenFlag(t *testing.T) {
	dir := writePolicy(t, `package blastdoor

deny contains {"msg": "creating a topic is fine", "score": 0, "resource": rc.address} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["create"]
}

classified contains rc.address if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["create"]
}
`)

	findings := evaluate(t, Options{PolicyPaths: []string{dir}}, planDoc(change("kafka_topic.a", "kafka_topic", "create")))

	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
	}
	if !findings[0].Allowed() {
		t.Errorf("score = %d, want 0 — the policy should have green-flagged this", findings[0].Score)
	}
}

// Green-flagging one shape must not green-flag a neighbouring one. The policy
// above claims only creates, so a delete of the same type still scores 100.
func TestGreenFlagIsNarrow(t *testing.T) {
	dir := writePolicy(t, `package blastdoor

deny contains {"msg": "creating a topic is fine", "score": 0, "resource": rc.address} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["create"]
}

classified contains rc.address if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["create"]
}
`)

	findings := evaluate(t, Options{PolicyPaths: []string{dir}}, planDoc(change("kafka_topic.a", "kafka_topic", "delete")))

	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
	}
	if findings[0].Score != DefaultScore {
		t.Errorf("score = %d, want %d — deleting was never classified", findings[0].Score, DefaultScore)
	}
}

// A policy cannot subtract risk found elsewhere in the plan.
func TestNegativeScoreIsRejected(t *testing.T) {
	dir := writePolicy(t, `package blastdoor

deny contains {"msg": "cancel it out", "score": -1000, "resource": "x"} if {
	true
}
`)

	e, err := New(context.Background(), Options{PolicyPaths: []string{dir}})
	if err != nil {
		t.Fatalf("compiling: %v", err)
	}
	if _, err := e.Evaluate(context.Background(), planDoc(change("a.b", "a", "delete"))); err == nil {
		t.Fatal("a negative score was accepted")
	}
}

// 0 is the one score that means "allowed", so a fraction must not truncate
// into it.
func TestFractionalScoreRoundsAwayFromAllowed(t *testing.T) {
	dir := writePolicy(t, `package blastdoor

deny contains {"msg": "small but real", "score": 0.6, "resource": rc.address} if {
	some rc in input.resource_changes
}

classified contains rc.address if {
	some rc in input.resource_changes
}
`)

	findings := evaluate(t, Options{PolicyPaths: []string{dir}}, planDoc(change("a.b", "a", "create")))

	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
	}
	if findings[0].Score != 1 {
		t.Errorf("score = %d, want 1", findings[0].Score)
	}
	if findings[0].Allowed() {
		t.Error("0.6 was rounded down into 'allowed'")
	}
}

// Anything blastdoor cannot read as a plan must be an error, never a score of
// zero. These are the shapes that would otherwise sail through.
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
		{
			// `tofu show -json` with no plan file dumps state, which has
			// neither field and would otherwise look like an empty plan.
			"state output",
			map[string]any{"format_version": "1.0", "values": map[string]any{}},
		},
		{
			"resource_changes of the wrong type",
			map[string]any{"format_version": "1.2", "resource_changes": map[string]any{}},
		},
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
		{
			// A plan that changes nothing omits resource_changes but still
			// carries planned_values.
			"empty plan keeping only planned_values",
			map[string]any{"format_version": "1.2", "planned_values": map[string]any{}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePlan(tc.doc); err != nil {
				t.Errorf("a real plan was rejected: %v", err)
			}
		})
	}
}

func joined(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "+"
		}
		out += p
	}
	return out
}
