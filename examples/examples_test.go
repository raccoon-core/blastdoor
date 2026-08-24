// Package examples_test keeps the shipped examples honest: every plan in
// examples/plans/ must still score what examples/README.md says it does.
// Change a policy and forget the docs, and this fails.
package examples_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/policy"
)

func TestExamplePlansScoreAsDocumented(t *testing.T) {
	want := map[string]int{
		"data-source-read.json":                0,
		"kafka-topic-create.json":              0,
		"kafka-topic-delete.json":              80,
		"kafka-acl-wildcard.json":              90,
		"unclassified-resource.json":           100,
		"managed-resource-read-lookalike.json": 100,
		"no-op.json":                           0,
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
		t.Fatalf("found %d example plans but %d expected scores — add the new plan to this test and to examples/README.md", len(plans), len(want))
	}

	for _, path := range plans {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			expected, ok := want[name]
			if !ok {
				t.Fatalf("no expected score for %s", name)
			}

			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			var decoded any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("parsing: %v", err)
			}

			findings, err := evaluator.Evaluate(context.Background(), decoded)
			if err != nil {
				t.Fatalf("evaluating: %v", err)
			}

			total := 0
			for _, f := range findings {
				total += f.Score
			}
			if total != expected {
				t.Errorf("score = %d, want %d (findings: %+v)", total, expected, findings)
			}
		})
	}
}
