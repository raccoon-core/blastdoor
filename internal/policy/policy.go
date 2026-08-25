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
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/open-policy-agent/opa/v1/rego"
	"github.com/open-policy-agent/opa/v1/storage"
	"github.com/open-policy-agent/opa/v1/storage/inmem"
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
	// Vars are values a repository sets for the policies to read, reachable
	// as data.vars. They let a shared rule carry a default that a repository
	// can move:
	//
	//	default max_partitions := 10
	//	max_partitions := data.vars.max_partitions if { data.vars.max_partitions }
	//
	// Mounted at data.vars, never at the root, so a variable cannot land on
	// data.blastdoor and displace the rule sets themselves. That is the
	// difference between this and letting the loader read .json and .yaml
	// out of a policy directory, which it deliberately does not.
	Vars map[string]any
}

// VarsRoot is where Vars are mounted. Anything but "blastdoor".
const VarsRoot = "vars"

// Evaluator holds a compiled set of policies, ready to judge many plans.
type Evaluator struct {
	queries map[Verdict]rego.PreparedEvalQuery
}

// keepRego tells the loader to walk into every directory and take the .rego
// files it finds there, and nothing else.
//
// Left to itself the loader also reads .json and .yaml under a policy path as
// data documents. A policy repository carries fixtures, test plans and its own
// configuration, so that turns files nobody thinks of as policy into inputs:
// one malformed fixture fails the whole evaluation, and a data.json landing on
// data.blastdoor collides with the rule sets themselves — a way to disable
// policies with a file that never looks like one. Policies are Rego. Nothing
// else in the tree is ours to load.
//
// The loader excludes what the filter returns true for.
func keepRego(_ string, info fs.FileInfo, _ int) bool {
	if info.IsDir() {
		return false
	}
	return filepath.Ext(info.Name()) != ".rego"
}

// countRego reports how many .rego files the given paths hold, walking
// directories the way the loader does.
func countRego(paths []string) (int, error) {
	n := 0
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return 0, fmt.Errorf("reading policy path: %w", err)
		}
		if !info.IsDir() {
			if filepath.Ext(info.Name()) == ".rego" {
				n++
			}
			continue
		}
		err = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && filepath.Ext(d.Name()) == ".rego" {
				n++
			}
			return nil
		})
		if err != nil {
			return 0, fmt.Errorf("scanning %s for policies: %w", p, err)
		}
	}
	return n, nil
}

// New compiles the policies described by opts.
func New(ctx context.Context, opts Options) (*Evaluator, error) {
	e := &Evaluator{queries: map[Verdict]rego.PreparedEvalQuery{}}

	// Policy paths that hold no policies are a mistake — a mistyped path, a
	// subdirectory that moved. Saying so beats letting it surface as every
	// change denied for want of a rule, which reads like a verdict.
	if len(opts.PolicyPaths) > 0 {
		found, err := countRego(opts.PolicyPaths)
		if err != nil {
			return nil, err
		}
		if found == 0 {
			return nil, fmt.Errorf("no .rego files found under %s", strings.Join(opts.PolicyPaths, ", "))
		}
	}

	// One store for all three queries, holding the repository's variables.
	var store storage.Store
	if len(opts.Vars) > 0 {
		store = inmem.NewFromObject(map[string]any{VarsRoot: opts.Vars})
	}

	for verdict, query := range Queries {
		args := []func(*rego.Rego){rego.Query(query)}
		if len(opts.PolicyPaths) > 0 {
			args = append(args, rego.Load(opts.PolicyPaths, keepRego))
		}

		prepared, err := prepare(ctx, store, args)
		if err != nil {
			return nil, fmt.Errorf("compiling policies for %s: %w", query, err)
		}
		e.queries[verdict] = prepared
	}
	return e, nil
}

// prepare compiles one query, against the variables store when there is one.
//
// Loading policies is a write to the store, so it needs a transaction of its
// own, committed before anything is evaluated — an evaluation opens its own
// read transaction, and would not see data left in an open write.
func prepare(ctx context.Context, store storage.Store, args []func(*rego.Rego)) (rego.PreparedEvalQuery, error) {
	if store == nil {
		return rego.New(args...).PrepareForEval(ctx)
	}

	txn, err := store.NewTransaction(ctx, storage.WriteParams)
	if err != nil {
		return rego.PreparedEvalQuery{}, fmt.Errorf("opening a transaction for the variables: %w", err)
	}

	args = append(args, rego.Store(store), rego.Transaction(txn))
	prepared, err := rego.New(args...).PrepareForEval(ctx)
	if err != nil {
		store.Abort(ctx, txn)
		return rego.PreparedEvalQuery{}, err
	}
	if err := store.Commit(ctx, txn); err != nil {
		return rego.PreparedEvalQuery{}, fmt.Errorf("storing the variables: %w", err)
	}
	return prepared, nil
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
