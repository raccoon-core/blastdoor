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
	// Uncovered lists changed files that no plan accounts for.
	Uncovered []string `json:"uncovered,omitempty"`
	// Layers records the policy tiers that judged this run, highest weight
	// first, with the commit each resolved to. A ref like "main" moves, so
	// without the commit a verdict cannot be explained afterwards.
	Layers []Layer `json:"layers,omitempty"`
	// Engines names what produced the plans — terraform, tofu, or both while
	// a repository is moving between them. Empty when nothing recorded it.
	Engines []string `json:"engines,omitempty"`
}

// Layer is one policy tier and where it came from.
type Layer struct {
	Name       string `json:"name"`
	Repository string `json:"repository,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Weight     int    `json:"weight"`
}

// overrideNote says which lower layers were overruled, and to what.
func overrideNote(c policy.Change) string {
	if len(c.Overridden) == 0 {
		return ""
	}
	parts := make([]string, 0, len(c.Overridden))
	for _, o := range c.Overridden {
		parts = append(parts, fmt.Sprintf("%s said %s", o.Layer, o.Verdict))
	}
	return fmt.Sprintf("(%s overrides: %s)", c.Layer, strings.Join(parts, ", "))
}

// engineNames are the products behind the binary names.
var engineNames = map[string]string{
	"terraform": "Terraform",
	"tofu":      "OpenTofu",
}

// heading titles the note with the engine that produced the plans, so a
// reader can see what ran without opening the job log.
//
// An engine with no name here is printed as it came, because a wrong name is
// worse than an unfamiliar one, and nothing at all is printed when no engine
// was recorded — plans passed straight to --plan, or written by a blastdoor
// old enough not to have said.
func (r Report) heading() string {
	seen := map[string]bool{}
	var engines []string
	for _, e := range r.Engines {
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		engines = append(engines, e)
	}
	if len(engines) == 0 {
		return "## Blastdoor\n\n"
	}

	// Sorted by binary name rather than by product name, so the order does
	// not depend on which unit happened to be planned first, and "terraform"
	// still sorts before "tofu".
	sort.Strings(engines)

	names := make([]string, 0, len(engines))
	for _, e := range engines {
		if name, ok := engineNames[e]; ok {
			names = append(names, name)
			continue
		}
		names = append(names, e)
	}
	return "## " + strings.Join(names, " + ") + " Blastdoor\n\n"
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

// RequireCoverage forces at least review, recording the changed files that no
// plan accounts for.
//
// A file nothing plans is a file no policy judges. Left alone it is not a
// lenient verdict but the absence of one: the change is applied by whatever
// runs next, having been read by nobody. Sending it to a person is the only
// honest answer, because blastdoor genuinely does not know what it does.
//
// Like RequireReview, it never softens a denial.
func (r *Report) RequireCoverage(paths []string) {
	if len(paths) == 0 {
		return
	}
	seen := map[string]bool{}
	for _, p := range r.Uncovered {
		seen[p] = true
	}
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		r.Uncovered = append(r.Uncovered, p)
	}
	sort.Strings(r.Uncovered)
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

	b.WriteString(r.heading())
	b.WriteString(r.headline())

	if len(r.Guarded) > 0 {
		b.WriteString("\nThis change also edits the rules that judge it, so a person has to look regardless:\n\n")
		for _, p := range r.Guarded {
			b.WriteString(fmt.Sprintf("- `%s`\n", escapePipes(p)))
		}
	}

	if line := r.layerLine(); line != "" {
		b.WriteString("\n" + line + "\n")
	}

	if len(r.Uncovered) > 0 {
		b.WriteString("\nThis change edits files that no plan covers, so what they do has not been judged:\n\n")
		for _, p := range r.Uncovered {
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
			why := strings.Join(c.Reasons, "; ")
			// "This passed" and "this passed because the repository overrode
			// the company rule" are different facts, and the reviewer needs
			// the second one. Without it an override is effective but
			// invisible.
			if note := overrideNote(c); note != "" {
				why += " " + note
			}
			b.WriteString(fmt.Sprintf("| %s | %s | `%s` (%s) | %s |\n",
				marker(c.Verdict),
				escapePipes(u.Path),
				escapePipes(c.Address),
				escapePipes(strings.Join(c.Actions, "+")),
				escapePipes(why)))
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// layerLine names the tiers that judged the run, so a reader knows what the
// verdict was measured against without opening the config.
func (r Report) layerLine() string {
	if len(r.Layers) < 2 {
		// One layer is the ordinary case and needs no explaining.
		return ""
	}
	parts := make([]string, 0, len(r.Layers))
	for _, l := range r.Layers {
		if l.Commit != "" {
			parts = append(parts, fmt.Sprintf("%s (%s@%.7s)", l.Name, l.Ref, l.Commit))
			continue
		}
		parts = append(parts, l.Name)
	}
	return "Judged by: " + strings.Join(parts, ", ") + " — highest weight first."
}

func (r Report) headline() string {
	pass, review, deny := r.Counts[policy.Pass], r.Counts[policy.Review], r.Counts[policy.Deny]

	return emoji(r.Verdict) + " " + r.verdictSentence(pass, review, deny)
}

// verdictSentence says what the verdict means, in words.
func (r Report) verdictSentence(pass, review, deny int) string {
	switch r.Verdict {
	case policy.Deny:
		unjudged := r.unjudgedCount()
		line := fmt.Sprintf("**Denied** — %d change(s) a policy does not allow.", deny)
		if unjudged > 0 {
			line += fmt.Sprintf(" %d of those have no policy at all; write a rule for them, or drop them from this change.", unjudged)
		}
		return line + " Approving does not clear this.\n"
	case policy.Review:
		// A review can be forced by paths rather than by scored changes —
		// a guarded file, or one no plan covers. Counting changes then
		// reports "0 change(s) need a person to approve", which reads like
		// there is nothing to look at, directly above the list of what to
		// look at.
		if review == 0 && pass == 0 && deny == 0 {
			return "**Review required** — a person has to look at this change. Nothing in it was scored.\n"
		}
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
		return emoji(v) + " **deny**"
	case policy.Review:
		return emoji(v) + " review"
	default:
		return emoji(v) + " pass"
	}
}

// emoji is the symbol for a verdict.
//
// It leads the overall verdict and the verdict column, so a note can be read
// at a glance and a long table skimmed for the rows that need attention. The
// word is always kept alongside it: a symbol on its own is lost on anyone
// using a screen reader, and in a plain-text copy of the note.
func emoji(v policy.Verdict) string {
	switch v {
	case policy.Deny:
		return "❌"
	case policy.Review:
		return "👀"
	default:
		return "✅"
	}
}

// escapePipes keeps a value containing "|" from breaking the Markdown table.
func escapePipes(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}
