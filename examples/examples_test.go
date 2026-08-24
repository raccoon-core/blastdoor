// Package examples_test keeps the shipped examples honest: every plan in
// examples/plans/ must still come back with the verdict examples/README.md
// says it does. Change a policy and forget the docs, and this fails.
package examples_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/policy"
)

func TestExamplePlansJudgeAsDocumented(t *testing.T) {
	want := map[string]policy.Verdict{
		"kafka-topic-create.json":              policy.Pass,
		"data-source-read.json":                policy.Pass,
		"no-op.json":                           policy.Pass,
		"kafka-topic-delete.json":              policy.Review,
		"kafka-acl-wildcard.json":              policy.Deny,
		"unclassified-resource.json":           policy.Deny,
		"managed-resource-read-lookalike.json": policy.Deny,
	}

	evaluator, err := policy.New(context.Background(), policy.Options{
		PolicyPaths: []string{"policies"},
	})
	if err != nil {
		t.Fatalf("compiling example policies: %v", err)
	}

	plans, err := filepath.Glob(filepath.Join("plans", "*.json"))
	if err != nil {
		t.Fatalf("listing plans: %v", err)
	}
	if len(plans) != len(want) {
		t.Fatalf("found %d example plans but %d expected verdicts — add the new plan to this test and to examples/README.md", len(plans), len(want))
	}

	for _, path := range plans {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			expected, ok := want[name]
			if !ok {
				t.Fatalf("no expected verdict for %s", name)
			}

			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			var decoded any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("parsing: %v", err)
			}

			res, err := evaluator.Evaluate(context.Background(), decoded)
			if err != nil {
				t.Fatalf("evaluating: %v", err)
			}

			if res.Verdict != expected {
				t.Errorf("verdict = %q, want %q (changes: %+v)", res.Verdict, expected, res.Changes)
			}
		})
	}
}
