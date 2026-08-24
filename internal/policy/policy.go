// Package policy judges Terraform/OpenTofu plan JSON against Rego policies.
//
// Every resource change gets one of three verdicts. There is no score and no
// threshold: a policy author answers "is this fine, does a person need to look,
// or is it not allowed?" — a question they can actually answer — rather than
// inventing a number and hoping it lands the right side of a cutoff.
package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/open-policy-agent/opa/v1/rego"
)

// Verdict is what a policy decided about a change.
type Verdict string

const (
	// Pass means a policy looked at the change and is content with it.
	Pass Verdict = "pass"
	// Review means a policy wants a person to approve it.
	Review Verdict = "review"
	// Deny means a policy forbids it, or no policy judged it at all.
	// Approving does not clear a Deny — the plan or the policy has to change.
	Deny Verdict = "deny"
)

// severity orders verdicts. The worst verdict anywhere decides the plan.
func severity(v Verdict) int {
	switch v {
	case Pass:
		return 1
	case Review:
		return 2
	case Deny:
		return 3
	}
	return 0
}

// Worse returns the more severe of two verdicts.
func Worse(a, b Verdict) Verdict {
	if severity(b) > severity(a) {
		return b
	}
	return a
}

// Queries are the three rule sets a policy contributes to.
var Queries = map[Verdict]string{
	Pass:   "data.blastdoor.allow",
	Review: "data.blastdoor.review",
	Deny:   "data.blastdoor.deny",
}

// ReasonUnjudged is given to a change no policy matched.
const ReasonUnjudged = "no policy judges this change"

// Change is one resource change and what the policies made of it.
type Change struct {
	Address string   `json:"address"`
	Type    string   `json:"type"`
	Actions []string `json:"actions"`
	Verdict Verdict  `json:"verdict"`
	// Reasons holds every matching rule's explanation, most severe first.
	Reasons []string `json:"reasons,omitempty"`
}

// Result is the verdict for a whole plan.
type Result struct {
	Verdict Verdict  `json:"verdict"`
	Changes []Change `json:"changes"`
}

// Counts tallies the changes by verdict.
func (r Result) Counts() map[Verdict]int {
	out := map[Verdict]int{}
	for _, c := range r.Changes {
		out[c.Verdict]++
	}
	return out
}

// Unjudged returns the changes no policy matched.
func (r Result) Unjudged() []Change {
	var out []Change
	for _, c := range r.Changes {
		for _, reason := range c.Reasons {
			if reason == ReasonUnjudged {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// ValidatePlan checks that a document really is Terraform/OpenTofu plan JSON
// before it is judged.
//
// Without this, anything that fails to parse as a plan — a truncated file, an
// error message, `{}` — has no changes to judge and passes. Failing to read a
// plan must never look like reading a safe one.
func ValidatePlan(plan any) error {
	doc, ok := plan.(map[string]any)
	if !ok {
		return fmt.Errorf("not a plan document: got %T, want a JSON object", plan)
	}

	// Every `terraform show -json` / `tofu show -json` document carries
	// format_version. Its absence means this is not plan JSON at all.
	if _, ok := doc["format_version"]; !ok {
		return errors.New(`not plan JSON: no "format_version" field. Produce it with 'tofu show -json <planfile>'`)
	}

	rc, hasChanges := doc["resource_changes"]
	if hasChanges {
		if _, ok := rc.([]any); !ok {
			return fmt.Errorf(`"resource_changes" is %T, want an array`, rc)
		}
	}

	// A plan carries planned_values even when it changes nothing, so a
	// document with neither field is not a plan. This is what catches state
	// output — `tofu show -json` with no plan file — which would otherwise
	// look like a plan with nothing in it.
	if _, hasPlanned := doc["planned_values"]; !hasChanges && !hasPlanned {
		return errors.New(`not a plan: no "resource_changes" and no "planned_values". This looks like state output — pass a plan file: 'tofu show -json <planfile>'`)
	}
	return nil
}

// Options configures an Evaluator.
type Options struct {
	// PolicyPaths are directories or .rego files to load.
	PolicyPaths []string
}

// Evaluator holds a compiled set of policies, ready to judge many plans.
type Evaluator struct {
	queries map[Verdict]rego.PreparedEvalQuery
}

// New compiles the policies described by opts.
func New(ctx context.Context, opts Options) (*Evaluator, error) {
	e := &Evaluator{queries: map[Verdict]rego.PreparedEvalQuery{}}

	for verdict, query := range Queries {
		args := []func(*rego.Rego){rego.Query(query)}
		if len(opts.PolicyPaths) > 0 {
			args = append(args, rego.Load(opts.PolicyPaths, nil))
		}

		prepared, err := rego.New(args...).PrepareForEval(ctx)
		if err != nil {
			return nil, fmt.Errorf("compiling policies for %s: %w", query, err)
		}
		e.queries[verdict] = prepared
	}
	return e, nil
}

// Evaluate judges every real change in a plan.
//
// A change matched by no rule is denied. That is decided here, from the plan
// itself, rather than by a policy rule that has to fire — a rule that does not
// run must not be the difference between a change being judged and being waved
// through.
func (e *Evaluator) Evaluate(ctx context.Context, plan any) (Result, error) {
	// Gather each rule set's judgements, keyed by resource address.
	matched := map[string]map[Verdict][]string{}
	for verdict := range Queries {
		judgements, err := e.judgements(ctx, verdict, plan)
		if err != nil {
			return Result{}, err
		}
		for address, reasons := range judgements {
			if matched[address] == nil {
				matched[address] = map[Verdict][]string{}
			}
			matched[address][verdict] = append(matched[address][verdict], reasons...)
		}
	}

	var changes []Change
	overall := Pass

	for _, rc := range resourceChanges(plan) {
		address, _ := rc["address"].(string)
		if address == "" || isNoOp(rc) {
			continue
		}

		change := Change{
			Address: address,
			Type:    stringField(rc, "type"),
			Actions: actions(rc),
			Verdict: Deny,
		}

		byVerdict, judged := matched[address]
		if !judged || len(byVerdict) == 0 {
			// Nothing matched it, so nobody has decided what it is.
			change.Reasons = []string{ReasonUnjudged}
		} else {
			// The most severe matching rule wins, so adding a rule can only
			// ever make a change stricter, never weaker.
			change.Verdict = Pass
			for verdict := range byVerdict {
				change.Verdict = Worse(change.Verdict, verdict)
			}
			change.Reasons = orderedReasons(byVerdict)
		}

		overall = Worse(overall, change.Verdict)
		changes = append(changes, change)
	}

	sort.Slice(changes, func(i, j int) bool {
		if severity(changes[i].Verdict) != severity(changes[j].Verdict) {
			return severity(changes[i].Verdict) > severity(changes[j].Verdict)
		}
		return changes[i].Address < changes[j].Address
	})

	return Result{Verdict: overall, Changes: changes}, nil
}

// judgements evaluates one rule set, returning reasons by resource address.
func (e *Evaluator) judgements(ctx context.Context, verdict Verdict, plan any) (map[string][]string, error) {
	rs, err := e.queries[verdict].Eval(ctx, rego.EvalInput(plan))
	if err != nil {
		return nil, fmt.Errorf("evaluating %s: %w", Queries[verdict], err)
	}

	out := map[string][]string{}
	for _, result := range rs {
		for _, expr := range result.Expressions {
			values, ok := expr.Value.([]any)
			if !ok {
				return nil, fmt.Errorf("%s returned %T, want a set of judgements", Queries[verdict], expr.Value)
			}
			for _, v := range values {
				address, reason, err := decodeJudgement(v, Queries[verdict])
				if err != nil {
					return nil, err
				}
				out[address] = append(out[address], reason)
			}
		}
	}
	return out, nil
}

// decodeJudgement reads one entry from a rule set.
func decodeJudgement(v any, query string) (address, reason string, err error) {
	raw, marshalErr := json.Marshal(v)
	if marshalErr != nil {
		return "", "", fmt.Errorf("re-encoding a judgement from %s: %w", query, marshalErr)
	}

	var decoded struct {
		Resource string `json:"resource"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", "", fmt.Errorf("%s produced %s, want an object with \"resource\" and \"reason\"", query, raw)
	}
	if decoded.Resource == "" {
		return "", "", fmt.Errorf("%s produced a judgement with no \"resource\", so it cannot be attached to a change: %s", query, raw)
	}
	if decoded.Reason == "" {
		return "", "", fmt.Errorf("%s produced a judgement with no \"reason\" for %s — say why, it goes in the summary", query, decoded.Resource)
	}
	return decoded.Resource, decoded.Reason, nil
}

// orderedReasons lists reasons most severe first, so the summary leads with
// what actually decided the change.
func orderedReasons(byVerdict map[Verdict][]string) []string {
	var out []string
	for _, verdict := range []Verdict{Deny, Review, Pass} {
		reasons := append([]string(nil), byVerdict[verdict]...)
		sort.Strings(reasons)
		out = append(out, reasons...)
	}
	return out
}

// resourceChanges pulls the resource_changes array out of a plan, returning
// nothing when it is absent or the wrong shape.
func resourceChanges(plan any) []map[string]any {
	doc, ok := plan.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := doc["resource_changes"].([]any)
	if !ok {
		return nil
	}

	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		if rc, ok := entry.(map[string]any); ok {
			out = append(out, rc)
		}
	}
	return out
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func actions(rc map[string]any) []string {
	change, ok := rc["change"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := change["actions"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, a := range raw {
		if s, ok := a.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// isNoOp reports whether a resource change does nothing at all.
func isNoOp(rc map[string]any) bool {
	acts := actions(rc)
	return len(acts) == 1 && acts[0] == "no-op"
}
