package report

import (
	"strings"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/policy"
)

func TestRequireCoverage(t *testing.T) {
	passing := Build([]Unit{{Path: "a", Changes: []policy.Change{change("x", policy.Pass, "fine")}}})
	passing.RequireCoverage([]string{"terraform/.terragrunt-version"})
	if passing.Verdict != policy.Review {
		t.Errorf("verdict = %q, want %q", passing.Verdict, policy.Review)
	}

	denied := Build([]Unit{{Path: "a", Changes: []policy.Change{change("x", policy.Deny, "never")}}})
	denied.RequireCoverage([]string{"terraform/.terragrunt-version"})
	if denied.Verdict != policy.Deny {
		t.Errorf("a denial was softened to %q", denied.Verdict)
	}

	untouched := Build([]Unit{{Path: "a", Changes: []policy.Change{change("x", policy.Pass, "fine")}}})
	untouched.RequireCoverage(nil)
	if untouched.Verdict != policy.Pass {
		t.Errorf("verdict = %q, want %q", untouched.Verdict, policy.Pass)
	}
}

// The uncovered files must reach the summary even when nothing was planned —
// that is the case the check exists for.
func TestWriteMarkdownListsUncoveredWithNoUnits(t *testing.T) {
	rep := Build(nil)
	rep.RequireCoverage([]string{"terraform/.terragrunt-version", "terraform/kafka/stg/topics.yaml"})

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}

	for _, want := range []string{
		"terraform/.terragrunt-version",
		"terraform/kafka/stg/topics.yaml",
		"no plan covers",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("summary does not mention %q:\n%s", want, b.String())
		}
	}
}

// A review forced by paths alone must not announce "0 change(s) need a person
// to approve" immediately above the list of what to approve.
func TestHeadlineForAReviewWithNothingScored(t *testing.T) {
	rep := Build(nil)
	rep.RequireCoverage([]string{"terraform/.terragrunt-version"})

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}

	if strings.Contains(b.String(), "0 change(s)") {
		t.Errorf("headline counts changes that were never scored:\n%s", b.String())
	}
	if !strings.Contains(b.String(), "a person has to look") {
		t.Errorf("summary:\n%s", b.String())
	}
}

func TestUncoveredIsSortedAndDeduplicated(t *testing.T) {
	rep := Build(nil)
	rep.RequireCoverage([]string{"b.yaml", "a.yaml"})
	rep.RequireCoverage([]string{"a.yaml"})

	want := []string{"a.yaml", "b.yaml"}
	if len(rep.Uncovered) != len(want) {
		t.Fatalf("uncovered = %v, want %v", rep.Uncovered, want)
	}
	for i := range want {
		if rep.Uncovered[i] != want[i] {
			t.Fatalf("uncovered = %v, want %v", rep.Uncovered, want)
		}
	}
}
