// Package report aggregates per-unit policy findings into a single risk
// verdict, and renders it as JSON, Markdown and dotenv.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/raccoon-core/blastdoor/internal/policy"
)

// Decision is the verdict for a whole run.
type Decision string

const (
	// DecisionPass means the total score stayed under the threshold.
	DecisionPass Decision = "pass"
	// DecisionReviewRequired means a human has to approve.
	DecisionReviewRequired Decision = "review-required"
)

// Unit is one planned directory and everything the policies said about it.
type Unit struct {
	Path     string           `json:"path"`
	Score    int              `json:"score"`
	Findings []policy.Finding `json:"findings"`
}

// Report is the complete result of an evaluation run.
type Report struct {
	TotalScore int      `json:"total_score"`
	Threshold  int      `json:"threshold"`
	Decision   Decision `json:"decision"`
	UnitCount  int      `json:"unit_count"`
	Units      []Unit   `json:"units"`
}

// Build totals the findings and decides whether review is required.
func Build(units []Unit, threshold int) Report {
	sort.Slice(units, func(i, j int) bool { return units[i].Path < units[j].Path })

	total := 0
	for i := range units {
		unitScore := 0
		for _, f := range units[i].Findings {
			unitScore += f.Score
		}
		units[i].Score = unitScore
		total += unitScore
	}

	decision := DecisionPass
	if total >= threshold {
		decision = DecisionReviewRequired
	}

	return Report{
		TotalScore: total,
		Threshold:  threshold,
		Decision:   decision,
		UnitCount:  len(units),
		Units:      units,
	}
}

// WriteJSON writes the machine-readable report.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteEnv writes a dotenv file, for GitLab's `artifacts:reports:dotenv` to
// pass the verdict to later jobs.
func (r Report) WriteEnv(w io.Writer) error {
	_, err := fmt.Fprintf(w,
		"BLASTDOOR_TOTAL_SCORE=%d\nBLASTDOOR_THRESHOLD=%d\nBLASTDOOR_DECISION=%s\nBLASTDOOR_UNIT_COUNT=%d\n",
		r.TotalScore, r.Threshold, r.Decision, r.UnitCount)
	return err
}

// WriteMarkdown writes the human-readable summary posted to the merge request.
func (r Report) WriteMarkdown(w io.Writer) error {
	var b strings.Builder

	b.WriteString("## Blastdoor risk assessment\n\n")
	if r.Decision == DecisionReviewRequired {
		b.WriteString(fmt.Sprintf("**Review required** — total risk score **%d** is at or above the threshold of %d.\n\n", r.TotalScore, r.Threshold))
	} else {
		b.WriteString(fmt.Sprintf("**Pass** — total risk score **%d** is below the threshold of %d.\n\n", r.TotalScore, r.Threshold))
	}

	hasFindings := false
	for _, u := range r.Units {
		if len(u.Findings) > 0 {
			hasFindings = true
			break
		}
	}

	// Say so plainly: zero units is also what a misconfigured root or a
	// failed detection looks like, and that should not read as approval.
	if r.UnitCount == 0 {
		b.WriteString("No units were scored, so nothing here has been checked.\n")
		_, err := io.WriteString(w, b.String())
		return err
	}

	if !hasFindings {
		b.WriteString(fmt.Sprintf("No findings across %d unit(s).\n", r.UnitCount))
		_, err := io.WriteString(w, b.String())
		return err
	}

	b.WriteString("| Unit | Resource | Score | Finding |\n|---|---|---|---|\n")
	for _, u := range r.Units {
		for _, f := range u.Findings {
			b.WriteString(fmt.Sprintf("| %s | %s | %d | %s |\n",
				escapePipes(u.Path), escapePipes(f.Resource), f.Score, escapePipes(f.Msg)))
		}
	}
	b.WriteString(fmt.Sprintf("\nScored %d unit(s).\n", r.UnitCount))

	_, err := io.WriteString(w, b.String())
	return err
}

// escapePipes keeps a value containing "|" from breaking the Markdown table.
func escapePipes(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}
