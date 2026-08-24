// Package policy evaluates Terraform/OpenTofu plan JSON against Rego policies
// and turns the resulting findings into risk scores.
package policy

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/open-policy-agent/opa/v1/rego"
)

//go:embed base.rego
var basePolicy string

const (
	// DefaultQuery is the rule blastdoor evaluates. Policies declare
	// `package blastdoor` and add findings to `deny`.
	DefaultQuery = "data.blastdoor.deny"

	// DefaultScore is used for a finding that carries no explicit score.
	// It is the maximum, so an under-specified rule fails closed.
	DefaultScore = 100
)

// Finding is one scored observation about one resource change.
type Finding struct {
	Resource string `json:"resource"`
	Score    int    `json:"score"`
	Msg      string `json:"msg"`
}

// Allowed reports whether this finding green-flags its resource: a policy
// looked at the change and scored it as carrying no risk.
func (f Finding) Allowed() bool { return f.Score == 0 }

// ValidatePlan checks that a document really is Terraform/OpenTofu plan JSON
// before it is scored.
//
// Without this, anything that fails to parse as a plan — a truncated file, an
// error message, `{}` — evaluates to zero findings and sails through as a
// score of 0. Scoring something blastdoor cannot read must never look like
// scoring something safe.
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
	// look like a plan that changes nothing and score 0.
	if _, hasPlanned := doc["planned_values"]; !hasChanges && !hasPlanned {
		return errors.New(`not a plan: no "resource_changes" and no "planned_values". This looks like state output — pass a plan file: 'tofu show -json <planfile>'`)
	}
	return nil
}

// Options configures an Evaluator.
type Options struct {
	// PolicyPaths are directories or .rego files to load.
	PolicyPaths []string
	// Query overrides DefaultQuery.
	Query string
	// NoBasePolicy disables the embedded default-deny backstop.
	NoBasePolicy bool
}

// Evaluator holds a compiled set of policies, ready to evaluate many plans.
type Evaluator struct {
	prepared rego.PreparedEvalQuery
	query    string
}

// New compiles the policies described by opts.
func New(ctx context.Context, opts Options) (*Evaluator, error) {
	query := opts.Query
	if query == "" {
		query = DefaultQuery
	}

	args := []func(*rego.Rego){rego.Query(query)}
	if !opts.NoBasePolicy {
		args = append(args, rego.Module("blastdoor/base.rego", basePolicy))
	}
	if len(opts.PolicyPaths) > 0 {
		args = append(args, rego.Load(opts.PolicyPaths, nil))
	}

	prepared, err := rego.New(args...).PrepareForEval(ctx)
	if err != nil {
		return nil, fmt.Errorf("compiling policies: %w", err)
	}
	return &Evaluator{prepared: prepared, query: query}, nil
}

// Evaluate scores a single plan. The plan is the decoded JSON produced by
// `tofu show -json` (or the terraform/terragrunt equivalent).
func (e *Evaluator) Evaluate(ctx context.Context, plan any) ([]Finding, error) {
	rs, err := e.prepared.Eval(ctx, rego.EvalInput(plan))
	if err != nil {
		return nil, fmt.Errorf("evaluating policies: %w", err)
	}
	if len(rs) == 0 {
		// The query was undefined for this input: no rule matched, so
		// there is nothing to report.
		return nil, nil
	}

	var findings []Finding
	for _, result := range rs {
		for _, expr := range result.Expressions {
			values, ok := expr.Value.([]any)
			if !ok {
				return nil, fmt.Errorf("policy query %q returned %T, want a set of findings", e.query, expr.Value)
			}
			for _, v := range values {
				f, err := decodeFinding(v)
				if err != nil {
					return nil, err
				}
				findings = append(findings, f)
			}
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Resource != findings[j].Resource {
			return findings[i].Resource < findings[j].Resource
		}
		return findings[i].Msg < findings[j].Msg
	})
	return findings, nil
}

// decodeFinding accepts either a bare string (the conftest idiom, scored at
// DefaultScore) or an object with msg/score/resource fields.
func decodeFinding(v any) (Finding, error) {
	if msg, ok := v.(string); ok {
		return Finding{Msg: msg, Score: DefaultScore}, nil
	}

	raw, err := json.Marshal(v)
	if err != nil {
		return Finding{}, fmt.Errorf("re-encoding finding: %w", err)
	}
	var decoded struct {
		Resource string   `json:"resource"`
		Score    *float64 `json:"score"`
		Msg      string   `json:"msg"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Finding{}, fmt.Errorf("finding is neither a string nor an object with msg/score/resource: %s", raw)
	}
	if decoded.Msg == "" {
		return Finding{}, fmt.Errorf("finding is missing the required \"msg\" field: %s", raw)
	}

	score := DefaultScore
	if decoded.Score != nil {
		// A negative score would let one rule cancel out the risk another
		// rule found, so refuse it rather than quietly summing it in.
		if *decoded.Score < 0 {
			return Finding{}, fmt.Errorf("finding scores %v: a score cannot be negative, because scores add up and a negative one would mask real risk elsewhere in the plan: %s", *decoded.Score, raw)
		}
		// Round rather than truncate: a computed 0.6 must not become 0, the
		// one value that means "allowed".
		score = int(math.Round(*decoded.Score))
	}
	return Finding{Resource: decoded.Resource, Score: score, Msg: decoded.Msg}, nil
}
