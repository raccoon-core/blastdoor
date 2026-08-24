package policy

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// plan builds plan JSON with a single resource change.
func plan(t *testing.T, address, resourceType string, actions ...string) any {
	t.Helper()
	raw := map[string]any{
		"resource_changes": []any{
			map[string]any{
				"address": address,
				"type":    resourceType,
				"change":  map[string]any{"actions": toAny(actions)},
			},
		},
	}
	// Round-trip so the input matches what a real plan file decodes to.
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("encoding plan: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decoding plan: %v", err)
	}
	return decoded
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func evaluate(t *testing.T, opts Options, input any) []Finding {
	t.Helper()
	e, err := New(context.Background(), opts)
	if err != nil {
		t.Fatalf("compiling policies: %v", err)
	}
	findings, err := e.Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluating: %v", err)
	}
	return findings
}

// The whole point of the tool: with no policies at all, nothing is waved
// through.
func TestBasePolicyScoresUnclassifiedAtMaximum(t *testing.T) {
	findings := evaluate(t, Options{}, plan(t, "aws_s3_bucket.x", "aws_s3_bucket", "create"))

	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
	}
	if findings[0].Score != DefaultScore {
		t.Errorf("score = %d, want %d", findings[0].Score, DefaultScore)
	}
	if findings[0].Resource != "aws_s3_bucket.x" {
		t.Errorf("resource = %q, want aws_s3_bucket.x", findings[0].Resource)
	}
}

// A no-op is not a change, so the backstop must stay quiet about it.
func TestBasePolicyIgnoresNoOp(t *testing.T) {
	findings := evaluate(t, Options{}, plan(t, "aws_s3_bucket.x", "aws_s3_bucket", "no-op"))

	if len(findings) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(findings), findings)
	}
}

// Once a policy claims a resource, the backstop defers to its score.
func TestClassifiedResourceSuppressesBackstop(t *testing.T) {
	dir := writePolicy(t, `package blastdoor

deny contains {"msg": "topic create", "score": 5, "resource": rc.address} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
}

classified contains rc.address if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
}
`)

	findings := evaluate(t, Options{PolicyPaths: []string{dir}}, plan(t, "kafka_topic.a", "kafka_topic", "create"))

	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
	}
	if findings[0].Score != 5 {
		t.Errorf("score = %d, want 5 (the backstop should not have fired)", findings[0].Score)
	}
}

// Claiming one resource must not silence the backstop for a different one.
func TestBackstopStillFiresForUnclaimedResource(t *testing.T) {
	dir := writePolicy(t, `package blastdoor

deny contains {"msg": "topic", "score": 5, "resource": rc.address} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
}

classified contains rc.address if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
}
`)

	input := map[string]any{
		"resource_changes": []any{
			map[string]any{"address": "kafka_topic.a", "type": "kafka_topic", "change": map[string]any{"actions": []any{"create"}}},
			map[string]any{"address": "aws_s3_bucket.b", "type": "aws_s3_bucket", "change": map[string]any{"actions": []any{"create"}}},
		},
	}

	findings := evaluate(t, Options{PolicyPaths: []string{dir}}, input)

	byResource := map[string]int{}
	for _, f := range findings {
		byResource[f.Resource] = f.Score
	}
	if byResource["kafka_topic.a"] != 5 {
		t.Errorf("kafka_topic.a score = %d, want 5", byResource["kafka_topic.a"])
	}
	if byResource["aws_s3_bucket.b"] != DefaultScore {
		t.Errorf("aws_s3_bucket.b score = %d, want %d", byResource["aws_s3_bucket.b"], DefaultScore)
	}
}

// --no-base-policy leaves only the author's own rules.
func TestNoBasePolicy(t *testing.T) {
	findings := evaluate(t, Options{NoBasePolicy: true}, plan(t, "aws_s3_bucket.x", "aws_s3_bucket", "create"))

	if len(findings) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(findings), findings)
	}
}

// A rule that omits a score must fail closed, not score zero.
func TestFindingWithoutScoreDefaultsToMaximum(t *testing.T) {
	dir := writePolicy(t, `package blastdoor

deny contains {"msg": "no score given", "resource": rc.address} if {
	some rc in input.resource_changes
}

classified contains rc.address if {
	some rc in input.resource_changes
}
`)

	findings := evaluate(t, Options{PolicyPaths: []string{dir}}, plan(t, "kafka_topic.a", "kafka_topic", "create"))

	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
	}
	if findings[0].Score != DefaultScore {
		t.Errorf("score = %d, want %d", findings[0].Score, DefaultScore)
	}
}

// Plain-string denies are the common conftest idiom, so they keep working.
func TestStringFindingIsAccepted(t *testing.T) {
	dir := writePolicy(t, `package blastdoor

deny contains msg if {
	some rc in input.resource_changes
	msg := sprintf("%s is not allowed", [rc.address])
}

classified contains rc.address if {
	some rc in input.resource_changes
}
`)

	findings := evaluate(t, Options{PolicyPaths: []string{dir}}, plan(t, "kafka_topic.a", "kafka_topic", "create"))

	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
	}
	if findings[0].Msg != "kafka_topic.a is not allowed" {
		t.Errorf("msg = %q", findings[0].Msg)
	}
	if findings[0].Score != DefaultScore {
		t.Errorf("score = %d, want %d", findings[0].Score, DefaultScore)
	}
}

func TestBrokenPolicyReportsCompileError(t *testing.T) {
	dir := writePolicy(t, "package blastdoor\n\nthis is not rego\n")

	if _, err := New(context.Background(), Options{PolicyPaths: []string{dir}}); err == nil {
		t.Fatal("expected a compile error")
	}
}

func writePolicy(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/policy.rego", []byte(body), 0o600); err != nil {
		t.Fatalf("writing policy: %v", err)
	}
	return dir
}
