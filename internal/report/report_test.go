package report

import (
	"strings"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/policy"
)

func TestBuildSumsScoresAcrossUnits(t *testing.T) {
	rep := Build([]Unit{
		{Path: "b", Findings: []policy.Finding{{Score: 10}, {Score: 20}}},
		{Path: "a", Findings: []policy.Finding{{Score: 5}}},
	}, 50)

	if rep.TotalScore != 35 {
		t.Errorf("TotalScore = %d, want 35", rep.TotalScore)
	}
	if rep.UnitCount != 2 {
		t.Errorf("UnitCount = %d, want 2", rep.UnitCount)
	}
	// Units are sorted, so reports do not churn between runs.
	if rep.Units[0].Path != "a" {
		t.Errorf("units are not sorted: %v", rep.Units)
	}
	if rep.Units[0].Score != 5 || rep.Units[1].Score != 30 {
		t.Errorf("per-unit scores = %d, %d; want 5, 30", rep.Units[0].Score, rep.Units[1].Score)
	}
}

// The threshold is inclusive: scoring exactly the threshold needs review.
func TestDecisionThresholdIsInclusive(t *testing.T) {
	tests := []struct {
		score int
		want  Decision
	}{
		{49, DecisionPass},
		{50, DecisionReviewRequired},
		{51, DecisionReviewRequired},
	}

	for _, tc := range tests {
		rep := Build([]Unit{{Path: "u", Findings: []policy.Finding{{Score: tc.score}}}}, 50)
		if rep.Decision != tc.want {
			t.Errorf("score %d: decision = %q, want %q", tc.score, rep.Decision, tc.want)
		}
	}
}

func TestBuildWithNoUnits(t *testing.T) {
	rep := Build(nil, 50)

	if rep.TotalScore != 0 {
		t.Errorf("TotalScore = %d, want 0", rep.TotalScore)
	}
	if rep.Decision != DecisionPass {
		t.Errorf("Decision = %q, want %q", rep.Decision, DecisionPass)
	}
}

func TestWriteEnv(t *testing.T) {
	rep := Build([]Unit{{Path: "u", Findings: []policy.Finding{{Score: 80}}}}, 50)

	var b strings.Builder
	if err := rep.WriteEnv(&b); err != nil {
		t.Fatalf("WriteEnv: %v", err)
	}

	for _, want := range []string{
		"BLASTDOOR_TOTAL_SCORE=80",
		"BLASTDOOR_THRESHOLD=50",
		"BLASTDOOR_DECISION=review-required",
		"BLASTDOOR_UNIT_COUNT=1",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("dotenv is missing %q:\n%s", want, b.String())
		}
	}
}

func TestWriteMarkdownIncludesFindings(t *testing.T) {
	rep := Build([]Unit{{
		Path:     "terraform/kafka/prd",
		Findings: []policy.Finding{{Resource: "kafka_topic.a", Score: 80, Msg: "deleting a topic"}},
	}}, 50)

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}

	for _, want := range []string{"Review required", "terraform/kafka/prd", "kafka_topic.a", "80", "deleting a topic"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("markdown is missing %q:\n%s", want, b.String())
		}
	}
}

func TestWriteMarkdownWithNoFindings(t *testing.T) {
	rep := Build([]Unit{{Path: "terraform/kafka/prd"}}, 50)

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}

	if !strings.Contains(b.String(), "No findings") {
		t.Errorf("expected a no-findings note:\n%s", b.String())
	}
	if strings.Contains(b.String(), "|---|") {
		t.Errorf("expected no table when there is nothing to show:\n%s", b.String())
	}
}

// A pipe in a message must not split the Markdown table into extra columns.
func TestWriteMarkdownEscapesPipes(t *testing.T) {
	rep := Build([]Unit{{
		Path:     "u",
		Findings: []policy.Finding{{Resource: "r", Score: 1, Msg: "a | b"}},
	}}, 50)

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}

	if !strings.Contains(b.String(), `a \| b`) {
		t.Errorf("pipe was not escaped:\n%s", b.String())
	}
}
