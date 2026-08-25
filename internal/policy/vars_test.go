package policy

import (
	"context"
	"testing"
)

// limitPolicy answers with whatever limit is in force, so a test can see
// which one the evaluator used.
const limitPolicy = `package blastdoor

default max_partitions := 10

max_partitions := data.vars.max_partitions if {
	data.vars.max_partitions
}

allow contains {"resource": rc.address, "reason": sprintf("limit is %v", [max_partitions])} if {
	some rc in input.resource_changes
	rc.change.after.partitions <= max_partitions
}

review contains {"resource": rc.address, "reason": sprintf("over the limit of %v", [max_partitions])} if {
	some rc in input.resource_changes
	rc.change.after.partitions > max_partitions
}
`

// judgeWithVars evaluates one topic change against the policy above.
func judgeWithVars(t *testing.T, vars map[string]any, partitions int) Change {
	t.Helper()

	e, err := New(context.Background(), Options{
		PolicyPaths: []string{writePolicy(t, limitPolicy)},
		Vars:        vars,
	})
	if err != nil {
		t.Fatalf("compiling policies: %v", err)
	}

	plan := planDoc(map[string]any{
		"address": "kafka_topic.x",
		"type":    "kafka_topic",
		"change":  map[string]any{"actions": []any{"update"}, "after": map[string]any{"partitions": partitions}},
	})
	res, err := e.Evaluate(context.Background(), plan)
	if err != nil {
		t.Fatalf("evaluating: %v", err)
	}
	if len(res.Changes) != 1 {
		t.Fatalf("want 1 change, got %d", len(res.Changes))
	}
	return res.Changes[0]
}

// With no variables the policy's own default stands.
func TestVarsAbsentLeavesTheDefault(t *testing.T) {
	if got := judgeWithVars(t, nil, 12); got.Verdict != Review {
		t.Errorf("verdict = %s, want %s: 12 is over the default of 10", got.Verdict, Review)
	}
	if got := judgeWithVars(t, nil, 8); got.Verdict != Pass {
		t.Errorf("verdict = %s, want %s", got.Verdict, Pass)
	}
}

// A repository raises its own limit.
func TestVarsOverrideTheDefault(t *testing.T) {
	got := judgeWithVars(t, map[string]any{"max_partitions": 25}, 12)

	if got.Verdict != Pass {
		t.Errorf("verdict = %s, want %s: 12 is under the override of 25", got.Verdict, Pass)
	}
	if got.Reasons[0] != "limit is 25" {
		t.Errorf("reason = %q, want the overridden limit", got.Reasons[0])
	}
}

// A repository lowers it.
func TestVarsCanTightenToo(t *testing.T) {
	if got := judgeWithVars(t, map[string]any{"max_partitions": 4}, 8); got.Verdict != Review {
		t.Errorf("verdict = %s, want %s: 8 is over the override of 4", got.Verdict, Review)
	}
}

// Zero is a value, not an absence.
func TestVarsZeroIsAValue(t *testing.T) {
	if got := judgeWithVars(t, map[string]any{"max_partitions": 0}, 1); got.Verdict != Review {
		t.Errorf("verdict = %s, want %s: a limit of 0 allows nothing", got.Verdict, Review)
	}
}

// The evaluator is reused across plans, so the variables have to survive
// more than the first evaluation.
func TestVarsSurviveRepeatedEvaluation(t *testing.T) {
	e, err := New(context.Background(), Options{
		PolicyPaths: []string{writePolicy(t, limitPolicy)},
		Vars:        map[string]any{"max_partitions": 25},
	})
	if err != nil {
		t.Fatalf("compiling policies: %v", err)
	}

	plan := planDoc(map[string]any{
		"address": "kafka_topic.x",
		"type":    "kafka_topic",
		"change":  map[string]any{"actions": []any{"update"}, "after": map[string]any{"partitions": 20}},
	})

	for i := range 3 {
		res, err := e.Evaluate(context.Background(), plan)
		if err != nil {
			t.Fatalf("evaluating (round %d): %v", i, err)
		}
		if res.Changes[0].Verdict != Pass {
			t.Fatalf("round %d: verdict = %s, want %s", i, res.Changes[0].Verdict, Pass)
		}
	}
}

// Variables must not be able to land on the namespace the rules live in.
// data.vars is a fixed root chosen so that it cannot be data.blastdoor.
func TestVarsCannotShadowRules(t *testing.T) {
	e, err := New(context.Background(), Options{
		PolicyPaths: []string{writePolicy(t, limitPolicy)},
		Vars:        map[string]any{"blastdoor": map[string]any{"allow": "hijacked"}},
	})
	if err != nil {
		t.Fatalf("compiling policies: %v", err)
	}

	plan := planDoc(map[string]any{
		"address": "kafka_topic.x",
		"type":    "kafka_topic",
		"change":  map[string]any{"actions": []any{"update"}, "after": map[string]any{"partitions": 8}},
	})
	res, err := e.Evaluate(context.Background(), plan)
	if err != nil {
		t.Fatalf("evaluating: %v", err)
	}
	if res.Changes[0].Verdict != Pass {
		t.Errorf("verdict = %s: a variable displaced the rules", res.Changes[0].Verdict)
	}
}
