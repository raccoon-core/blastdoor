package report

import (
	"strings"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/policy"
)

// summaryFor renders a one-unit report holding one change.
func summaryFor(t *testing.T, v policy.Verdict) string {
	t.Helper()
	rep := Build([]Unit{{Path: "terraform/kafka/stg", Changes: []policy.Change{change("kafka_topic.x", v, "because")}}})

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	return b.String()
}

// The verdict column carries a symbol, so a long table can be skimmed for the
// rows that need attention instead of read word by word.
func TestVerdictColumnCarriesAnEmoji(t *testing.T) {
	tests := []struct {
		verdict policy.Verdict
		emoji   string
	}{
		{policy.Pass, "✅"},
		{policy.Review, "👀"},
		{policy.Deny, "❌"},
	}

	for _, tc := range tests {
		t.Run(string(tc.verdict), func(t *testing.T) {
			summary := summaryFor(t, tc.verdict)

			row := ""
			for _, line := range strings.Split(summary, "\n") {
				if strings.Contains(line, "kafka_topic.x") {
					row = line
				}
			}
			if row == "" {
				t.Fatalf("no table row in:\n%s", summary)
			}
			if !strings.Contains(row, tc.emoji) {
				t.Errorf("row %q lacks %s", row, tc.emoji)
			}
			// The word stays: a symbol alone is lost on anyone reading with
			// a screen reader, and in a plain-text copy of the note.
			if !strings.Contains(row, string(tc.verdict)) {
				t.Errorf("row %q dropped the word %q", row, tc.verdict)
			}
		})
	}
}

// The overall verdict leads with the same symbol, so the note can be judged
// from the first line in an activity feed.
func TestHeadlineLeadsWithTheEmoji(t *testing.T) {
	for verdict, emoji := range map[policy.Verdict]string{
		policy.Pass:   "✅",
		policy.Review: "👀",
		policy.Deny:   "❌",
	} {
		t.Run(string(verdict), func(t *testing.T) {
			summary := summaryFor(t, verdict)

			// Line 0 is the heading, line 1 blank, line 2 the headline.
			lines := strings.Split(summary, "\n")
			if len(lines) < 3 {
				t.Fatalf("summary too short:\n%s", summary)
			}
			if !strings.HasPrefix(lines[2], emoji+" ") {
				t.Errorf("headline %q should start with %s", lines[2], emoji)
			}
		})
	}
}

// A review forced by a guarded path, with nothing scored, still leads with
// the symbol for the verdict it reached.
func TestForcedReviewHeadlineHasTheEmoji(t *testing.T) {
	rep := Build(nil)
	rep.RequireReview([]string{"policy/kafka.rego"})

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	lines := strings.Split(b.String(), "\n")
	if !strings.HasPrefix(lines[2], "👀 ") {
		t.Errorf("headline %q should start with the review emoji", lines[2])
	}
}
