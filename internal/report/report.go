// Package report aggregates per-unit verdicts into one decision, and renders
// it as JSON, Markdown and dotenv.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/raccoon-core/blastdoor/internal/policy"
)

// Unit is one planned directory and what the policies made of it.
type Unit struct {
	Path    string          `json:"path"`
	Verdict policy.Verdict  `json:"verdict"`
	Changes []policy.Change `json:"changes"`
}

// Report is the complete result of an evaluation run.
type Report struct {
	// Verdict is the most severe verdict anywhere in the run.
	Verdict   policy.Verdict `json:"verdict"`
	UnitCount int            `json:"unit_count"`
	Units     []Unit         `json:"units"`
	// Counts tallies changes by verdict.
	Counts map[policy.Verdict]int `json:"counts"`
	// Guarded lists changed paths that force review on their own.
	Guarded []string `json:"guarded,omitempty"`
}

// Build folds the units into one verdict: the worst one anywhere.
//
// There is no arithmetic here on purpose. Ten changes a policy is happy with
// do not add up to a problem, and one a policy forbids is not offset by nine
// that are fine.
func Build(units []Unit) Report {
	sort.Slice(units, func(i, j int) bool { return units[i].Path < units[j].Path })

	overall := policy.Pass
	counts := map[policy.Verdict]int{}

	for i := range units {
		unitVerdict := policy.Pass
		for _, c := range units[i].Changes {
			unitVerdict = policy.Worse(unitVerdict, c.Verdict)
			counts[c.Verdict]++
		}
		units[i].Verdict = unitVerdict
		overall = policy.Worse(overall, unitVerdict)
	}

	return Report{
		Verdict:   overall,
		UnitCount: len(units),
		Units:     units,
		Counts:    counts,
	}
}

// RequireReview forces at least review, recording which paths caused it.
//
// This is what stops a change from rewriting the rules that judge it: an edit
// to the policies or to the pipeline is looked at by a person, not judged by
// the very policies it edits. It never softens a denial.
func (r *Report) RequireReview(paths []string) {
	if len(paths) == 0 {
		return
	}
	r.Guarded = append(r.Guarded, paths...)
	sort.Strings(r.Guarded)
	r.Verdict = policy.Worse(r.Verdict, policy.Review)
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
		"BLASTDOOR_VERDICT=%s\nBLASTDOOR_UNIT_COUNT=%d\nBLASTDOOR_PASS_COUNT=%d\nBLASTDOOR_REVIEW_COUNT=%d\nBLASTDOOR_DENY_COUNT=%d\n",
		r.Verdict, r.UnitCount, r.Counts[policy.Pass], r.Counts[policy.Review], r.Counts[policy.Deny])
	return err
}

// WriteMarkdown writes the human-readable summary posted to the merge request.
func (r Report) WriteMarkdown(w io.Writer) error {
	var b strings.Builder

	b.WriteString("## Blastdoor\n\n")
	b.WriteString(r.headline())

	if len(r.Guarded) > 0 {
		b.WriteString("\nThis change also edits the rules that judge it, so a person has to look regardless:\n\n")
		for _, p := range r.Guarded {
			b.WriteString(fmt.Sprintf("- `%s`\n", escapePipes(p)))
		}
	}

	// Zero units is also what a misconfigured root or a failed detection
	// looks like, so say it rather than letting it read as approval.
	if r.UnitCount == 0 {
		b.WriteString("\nNo units were scored, so nothing here has been checked.\n")
		_, err := io.WriteString(w, b.String())
		return err
	}

	hasChanges := false
	for _, u := range r.Units {
		if len(u.Changes) > 0 {
			hasChanges = true
			break
		}
	}
	if !hasChanges {
		b.WriteString(fmt.Sprintf("\nNo changes across %d unit(s).\n", r.UnitCount))
		_, err := io.WriteString(w, b.String())
		return err
	}

	b.WriteString("\n| Verdict | Unit | Change | Why |\n|---|---|---|---|\n")
	for _, u := range r.Units {
		for _, c := range u.Changes {
			b.WriteString(fmt.Sprintf("| %s | %s | `%s` (%s) | %s |\n",
				marker(c.Verdict),
				escapePipes(u.Path),
				escapePipes(c.Address),
				escapePipes(strings.Join(c.Actions, "+")),
				escapePipes(strings.Join(c.Reasons, "; "))))
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

func (r Report) headline() string {
	pass, review, deny := r.Counts[policy.Pass], r.Counts[policy.Review], r.Counts[policy.Deny]

	switch r.Verdict {
	case policy.Deny:
		unjudged := r.unjudgedCount()
		line := fmt.Sprintf("**Denied** — %d change(s) a policy does not allow.", deny)
		if unjudged > 0 {
			line += fmt.Sprintf(" %d of those have no policy at all; write a rule for them, or drop them from this change.", unjudged)
		}
		return line + " Approving does not clear this.\n"
	case policy.Review:
		return fmt.Sprintf("**Review required** — %d change(s) need a person to approve, %d passed.\n", review, pass)
	default:
		return fmt.Sprintf("**Pass** — every one of the %d change(s) is allowed by policy.\n", pass)
	}
}

func (r Report) unjudgedCount() int {
	n := 0
	for _, u := range r.Units {
		for _, c := range u.Changes {
			for _, reason := range c.Reasons {
				if reason == policy.ReasonUnjudged {
					n++
					break
				}
			}
		}
	}
	return n
}

func marker(v policy.Verdict) string {
	switch v {
	case policy.Deny:
		return "**deny**"
	case policy.Review:
		return "review"
	default:
		return "pass"
	}
}

// escapePipes keeps a value containing "|" from breaking the Markdown table.
func escapePipes(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}
