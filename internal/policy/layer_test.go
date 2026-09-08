package policy

import (
	"context"
	"testing"
)

// layerOf builds one layer from an inline policy.
func layerOf(t *testing.T, name string, weight int, body string) Layer {
	t.Helper()
	return Layer{Name: name, Weight: weight, Paths: []string{writePolicy(t, body)}}
}

// judgeLayered evaluates one kafka_acl create against the given layers.
func judgeLayered(t *testing.T, layers ...Layer) Change {
	t.Helper()

	e, err := New(context.Background(), Options{Layers: layers})
	if err != nil {
		t.Fatalf("compiling policies: %v", err)
	}
	res, err := e.Evaluate(context.Background(), planDoc(change("kafka_acl.x", "kafka_acl", "create")))
	if err != nil {
		t.Fatalf("evaluating: %v", err)
	}
	if len(res.Changes) != 1 {
		t.Fatalf("want 1 change, got %d", len(res.Changes))
	}
	return res.Changes[0]
}

const denyACL = `package blastdoor
deny contains {"resource": rc.address, "reason": "company: no new ACLs"} if {
	some rc in input.resource_changes
	rc.type == "kafka_acl"
}`

const allowACL = `package blastdoor
allow contains {"resource": rc.address, "reason": "local: this one is fine"} if {
	some rc in input.resource_changes
	rc.type == "kafka_acl"
}`

const reviewACL = `package blastdoor
review contains {"resource": rc.address, "reason": "domain: have a look"} if {
	some rc in input.resource_changes
	rc.type == "kafka_acl"
}`

// The point of the whole mechanism: a repository's own layer has the last
// word over the company's, even to loosen it.
func TestHigherWeightOverridesLower(t *testing.T) {
	got := judgeLayered(t,
		layerOf(t, "company", 0, denyACL),
		layerOf(t, "local", 99, allowACL),
	)

	if got.Verdict != Pass {
		t.Errorf("verdict = %s, want %s: the local layer decides", got.Verdict, Pass)
	}
	if got.Layer != "local" {
		t.Errorf("layer = %q, want %q", got.Layer, "local")
	}
	if got.Reasons[0] != "local: this one is fine" {
		t.Errorf("reasons = %v, want the deciding layer's", got.Reasons)
	}
}

// Silence is not consent: a layer that says nothing lets the next one down
// decide, which is what lets a tier add rules without restating the rest.
func TestSilentLayerFallsThrough(t *testing.T) {
	silent := `package blastdoor
allow contains {"resource": rc.address, "reason": "local: only about topics"} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
}`

	got := judgeLayered(t,
		layerOf(t, "company", 0, denyACL),
		layerOf(t, "local", 99, silent),
	)

	if got.Verdict != Deny {
		t.Errorf("verdict = %s, want %s: the company layer still decides", got.Verdict, Deny)
	}
	if got.Layer != "company" {
		t.Errorf("layer = %q, want %q", got.Layer, "company")
	}
}

// Three tiers, and the middle one is the highest that judged it.
func TestMiddleLayerDecidesWhenTheTopIsSilent(t *testing.T) {
	silent := `package blastdoor
allow contains {"resource": rc.address, "reason": "local: topics only"} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
}`

	got := judgeLayered(t,
		layerOf(t, "company", 0, denyACL),
		layerOf(t, "domain", 1, reviewACL),
		layerOf(t, "local", 99, silent),
	)

	if got.Verdict != Review {
		t.Errorf("verdict = %s, want %s", got.Verdict, Review)
	}
	if got.Layer != "domain" {
		t.Errorf("layer = %q, want %q", got.Layer, "domain")
	}
}

// Within one layer nothing changed: the most severe rule still wins, so a
// rule added to a layer cannot weaken that layer.
func TestSeverityStillWinsInsideALayer(t *testing.T) {
	both := allowACL + "\n" + `
deny contains {"resource": rc.address, "reason": "company: not this one"} if {
	some rc in input.resource_changes
	rc.type == "kafka_acl"
}`

	got := judgeLayered(t, layerOf(t, "company", 0, both))

	if got.Verdict != Deny {
		t.Errorf("verdict = %s, want %s: severity decides within a layer", got.Verdict, Deny)
	}
}

// A change no layer judged still needs a person, and Go still decides that.
func TestUnjudgedByEveryLayerIsSentToReview(t *testing.T) {
	topicsOnly := `package blastdoor
allow contains {"resource": rc.address, "reason": "topics only"} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
}`

	got := judgeLayered(t,
		layerOf(t, "company", 0, topicsOnly),
		layerOf(t, "local", 99, topicsOnly),
	)

	if got.Verdict != Review {
		t.Errorf("verdict = %s, want %s", got.Verdict, Review)
	}
	if got.Reasons[0] != ReasonUnjudged {
		t.Errorf("reasons = %v, want %q", got.Reasons, ReasonUnjudged)
	}
}

// What a lower layer said is kept for the record, so an override can be
// audited even though it did not decide.
func TestOverriddenJudgementsAreRecorded(t *testing.T) {
	got := judgeLayered(t,
		layerOf(t, "company", 0, denyACL),
		layerOf(t, "local", 99, allowACL),
	)

	if len(got.Overridden) != 1 {
		t.Fatalf("overridden = %v, want the company layer's judgement", got.Overridden)
	}
	o := got.Overridden[0]
	if o.Layer != "company" || o.Verdict != Deny {
		t.Errorf("overridden = %+v, want company/deny", o)
	}
	if len(o.Reasons) != 1 || o.Reasons[0] != "company: no new ACLs" {
		t.Errorf("overridden reasons = %v", o.Reasons)
	}
}

// Two layers at the same weight have no order between them, so a verdict
// would depend on which one happened to be looked at first.
func TestDuplicateWeightsAreAnError(t *testing.T) {
	_, err := New(context.Background(), Options{Layers: []Layer{
		layerOf(t, "company", 5, denyACL),
		layerOf(t, "domain", 5, allowACL),
	}})
	if err == nil {
		t.Fatal("want an error for two layers at the same weight")
	}
}

// The flag path still works: policy paths with no layers behave as one layer.
func TestPolicyPathsBecomeASingleLayer(t *testing.T) {
	e, err := New(context.Background(), Options{PolicyPaths: []string{writePolicy(t, allowACL)}})
	if err != nil {
		t.Fatalf("compiling policies: %v", err)
	}
	res, err := e.Evaluate(context.Background(), planDoc(change("kafka_acl.x", "kafka_acl", "create")))
	if err != nil {
		t.Fatalf("evaluating: %v", err)
	}
	if res.Changes[0].Verdict != Pass {
		t.Errorf("verdict = %s, want %s", res.Changes[0].Verdict, Pass)
	}
}
