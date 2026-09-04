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
	"path/filepath"
	"sort"
	"strings"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/loader"
	"github.com/open-policy-agent/opa/v1/rego"
	"github.com/open-policy-agent/opa/v1/storage"
	"github.com/open-policy-agent/opa/v1/storage/inmem"
)

// Verdict is what a policy decided about a change.
type Verdict string

const (
	// Pass means a policy looked at the change and is content with it.
	Pass Verdict = "pass"
	// Review means a policy wants a person to approve it, or no policy
	// judged it at all — silence is not consent, but it is not an outright
	// block either: it needs a person, the same as an explicit review rule.
	Review Verdict = "review"
	// Deny means a policy forbids it.
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

// ReasonUnjudged is given to a change no policy matched. Such a change is
// sent to Review, never waved through as Pass — see Evaluate.
const ReasonUnjudged = "no policy judges this change"

// Change is one resource change and what the policies made of it.
type Change struct {
	Address string   `json:"address"`
	Type    string   `json:"type"`
	Actions []string `json:"actions"`
	Verdict Verdict  `json:"verdict"`
	// Reasons holds every matching rule's explanation, most severe first.
	Reasons []string `json:"reasons,omitempty"`
	// Layer is the layer whose judgement decided this change: the
	// highest-weight one that judged it at all. Empty when none did.
	Layer string `json:"layer,omitempty"`
	// Overridden is what the layers below the deciding one said. They did
	// not decide, but a repository overriding its company's rules has to be
	// auditable rather than merely effective.
	Overridden []Judgement `json:"overridden,omitempty"`
	// DeploymentMethod names, per environment, whether an `allow` rule
	// considers this change safe to apply unattended — only ever "auto" is
	// stored here; an environment a rule marked "manual", or never named at
	// all, is simply absent, since absent and manual mean the same thing to
	// everything downstream. Only ever set when Verdict is Pass — a change a
	// policy sends to review or denies is never a candidate for automation,
	// whatever an allow entry elsewhere said. Empty means no matching allow
	// rule vouched for automating this change in any environment, which is
	// the default for every rule that has not stated an opinion.
	DeploymentMethod map[string]string `json:"deployment_method,omitempty"`
}

// Judgement is one layer's answer about one change.
type Judgement struct {
	Layer   string   `json:"layer"`
	Verdict Verdict  `json:"verdict"`
	Reasons []string `json:"reasons,omitempty"`
	// DeploymentMethod is what this layer's allow rules agreed is safe to
	// apply unattended. See Change.DeploymentMethod — same rule: only
	// meaningful when Verdict is Pass.
	DeploymentMethod map[string]string `json:"deployment_method,omitempty"`
}

// Layer is one source of policies, with the weight that orders it.
//
// Layers let an organisation tier its rules: a company layer everything is
// judged by, a domain layer refining it, a repository with the last word. The
// highest-weight layer that judges a change decides it — which means a layer
// can loosen a lower one, deliberately. See docs/hardening.md.
type Layer struct {
	Name   string
	Weight int
	// Paths are directories or .rego files on disk holding this layer.
	Paths []string
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
	// Layers are the tiers of policy, ordered by weight. When empty,
	// PolicyPaths is used as a single unnamed layer.
	Layers []Layer
	// PolicyPaths are directories or .rego files to load as one layer.
	PolicyPaths []string
	// Vars are values a repository sets for the policies to read, reachable
	// as data.variables. They let a shared rule carry a default that a repository
	// can move:
	//
	//	default max_partitions := 10
	//	max_partitions := data.variables.max_partitions if { data.variables.max_partitions }
	//
	// Mounted at data.variables, never at the root, so a variable cannot land on
	// data.blastdoor and displace the rule sets themselves. That is the
	// difference between this and letting the loader read .json and .yaml
	// out of a policy directory, which it deliberately does not.
	Vars map[string]any
}

// VarsRoot is where Vars are mounted. Anything but "blastdoor".
const VarsRoot = "variables"

// Evaluator holds a compiled set of policies, ready to judge many plans.
type Evaluator struct {
	// layers are ordered by descending weight: the first to judge a change
	// decides it.
	layers []compiledLayer
}

// compiledLayer is one layer's three prepared queries.
type compiledLayer struct {
	name    string
	weight  int
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

// New compiles the policies described by opts.
func New(ctx context.Context, opts Options) (*Evaluator, error) {
	layers := opts.Layers
	if len(layers) == 0 {
		if len(opts.PolicyPaths) == 0 {
			// No policies at all: every change is unjudged, which Evaluate
			// sends to review on its own.
			return &Evaluator{}, nil
		}
		layers = []Layer{{Name: "policy", Paths: opts.PolicyPaths}}
	}

	if err := checkWeights(layers); err != nil {
		return nil, err
	}

	// One store for all layers, holding the repository's variables.
	var store storage.Store
	if len(opts.Vars) > 0 {
		store = inmem.NewFromObject(map[string]any{VarsRoot: opts.Vars})
	}

	// Highest weight first: Evaluate takes the first layer that judged a
	// change, so the order here is the override order.
	ordered := append([]Layer{}, layers...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Weight > ordered[j].Weight })

	e := &Evaluator{}
	for _, layer := range ordered {
		compiled, err := compileLayer(ctx, layer, store)
		if err != nil {
			return nil, err
		}
		e.layers = append(e.layers, compiled)
	}
	return e, nil
}

// checkWeights refuses two layers that cannot be ordered.
//
// Equal weights leave no answer to which one overrides the other, and picking
// one would make a verdict depend on the order a map was walked in.
func checkWeights(layers []Layer) error {
	seen := map[int]string{}
	for _, layer := range layers {
		if other, clash := seen[layer.Weight]; clash {
			return fmt.Errorf("policy layers %q and %q both have weight %d, so neither can override the other: give them different weights",
				other, layer.Name, layer.Weight)
		}
		seen[layer.Weight] = layer.Name
	}
	return nil
}

// compileLayer prepares one layer's three queries.
//
// A layer's modules are written as `package blastdoor`, whichever tier they
// come from — an author should not have to know their file's weight. They are
// moved to data.layers.<name> here so each layer can be asked on its own,
// which is what makes one able to override another.
func compileLayer(ctx context.Context, layer Layer, store storage.Store) (compiledLayer, error) {
	modules, err := loadModules(layer)
	if err != nil {
		return compiledLayer{}, err
	}
	// A path holding no .rego at all is a mistyped path or the wrong
	// subdirectory, not an empty rule set. Compiling nothing would deny every
	// change for want of a rule, which reads as a verdict on the plan rather
	// than as the mistake it is. The loader has already walked the paths under
	// keepRego, so what it found is the count — asking the filesystem a second
	// time would only invite the two answers to drift apart.
	if len(modules) == 0 {
		return compiledLayer{}, fmt.Errorf("policy layer %q: no .rego files found under %s", layer.Name, strings.Join(layer.Paths, ", "))
	}

	out := compiledLayer{name: layer.Name, weight: layer.Weight, queries: map[Verdict]rego.PreparedEvalQuery{}}
	for verdict := range Queries {
		args := []func(*rego.Rego){rego.Query(layerQuery(layer.Name, verdict))}
		for _, mod := range modules {
			args = append(args, rego.ParsedModule(mod))
		}

		prepared, err := prepare(ctx, store, args)
		if err != nil {
			return compiledLayer{}, fmt.Errorf("compiling policy layer %q: %w", layer.Name, err)
		}
		out.queries[verdict] = prepared
	}
	return out, nil
}

// layerQuery is where a layer's rule set lives once it has been moved.
func layerQuery(name string, verdict Verdict) string {
	return "data.layers." + ast.VarTerm(name).String() + "." + strings.TrimPrefix(Queries[verdict], "data.blastdoor.")
}

// loadModules parses a layer's .rego and moves it into the layer's package.
func loadModules(layer Layer) ([]*ast.Module, error) {
	loaded, err := loader.NewFileLoader().Filtered(layer.Paths, keepRego)
	if err != nil {
		return nil, fmt.Errorf("reading policy layer %q: %w", layer.Name, err)
	}

	pkg, err := ast.ParseRef("data.layers." + ast.VarTerm(layer.Name).String())
	if err != nil {
		return nil, fmt.Errorf("policy layer %q is not a usable name: %w", layer.Name, err)
	}

	out := make([]*ast.Module, 0, len(loaded.Modules))
	for _, mod := range loaded.Modules {
		mod.Parsed.Package.Path = pkg
		out = append(out, mod.Parsed)
	}
	return out, nil
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
// A change matched by no rule is sent to review, never passed. That is
// decided here, from the plan itself, rather than by a policy rule that has
// to fire — a rule that does not run must not be the difference between a
// change being judged and being waved through unattended.
//
// With more than one layer, the highest-weight layer that judged a change at
// all decides it, and the layers below are recorded but do not contribute.
// That lets a repository loosen its company's rules, deliberately: see
// docs/hardening.md for what stands in the way of that becoming
// self-approval.
func (e *Evaluator) Evaluate(ctx context.Context, plan any) (Result, error) {
	// What each layer said, by resource address. Layers are in descending
	// weight order already.
	byLayer := make([]map[string]Judgement, len(e.layers))
	for i, layer := range e.layers {
		judged, err := e.layerJudgements(ctx, layer, plan)
		if err != nil {
			return Result{}, err
		}
		byLayer[i] = judged
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
			Verdict: Review,
		}

		decided := false
		for i, judged := range byLayer {
			j, ok := judged[address]
			if !ok {
				// Silence is not consent: it falls through to the layer
				// below, which is what lets a tier add rules without
				// restating the ones beneath it.
				continue
			}
			if !decided {
				change.Verdict = j.Verdict
				change.Reasons = j.Reasons
				change.Layer = e.layers[i].name
				change.DeploymentMethod = j.DeploymentMethod
				decided = true
				continue
			}
			change.Overridden = append(change.Overridden, j)
		}

		if !decided {
			// Nothing matched it, so nobody has decided what it is.
			change.Reasons = []string{ReasonUnjudged}
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

// layerJudgements asks one layer about every change, folding its three rule
// sets into one answer per address.
//
// Within a layer the most severe rule still wins, so adding a rule to a layer
// can only ever make that layer stricter. Only the layer boundary overrides.
func (e *Evaluator) layerJudgements(ctx context.Context, layer compiledLayer, plan any) (map[string]Judgement, error) {
	matched := map[string]map[Verdict][]ruleMatch{}
	for verdict := range Queries {
		judgements, err := e.judgements(ctx, layer, verdict, plan)
		if err != nil {
			return nil, err
		}
		for address, matches := range judgements {
			if matched[address] == nil {
				matched[address] = map[Verdict][]ruleMatch{}
			}
			matched[address][verdict] = append(matched[address][verdict], matches...)
		}
	}

	out := make(map[string]Judgement, len(matched))
	for address, byVerdict := range matched {
		verdict := Pass
		for v := range byVerdict {
			verdict = Worse(verdict, v)
		}

		reasons := map[Verdict][]string{}
		for v, matches := range byVerdict {
			for _, m := range matches {
				reasons[v] = append(reasons[v], m.reason)
			}
		}

		j := Judgement{Layer: layer.name, Verdict: verdict, Reasons: orderedReasons(reasons)}
		// DeploymentMethod is only meaningful once nothing here needs a
		// person to look — a rule cannot vouch for automation and be
		// overruled by review or deny in the same breath.
		if verdict == Pass {
			j.DeploymentMethod = intersectDeploymentMethod(byVerdict[Pass])
		}
		out[address] = j
	}
	return out, nil
}

// ruleMatch is one decoded entry from a rule set: what one matching rule said
// about one address.
type ruleMatch struct {
	reason string
	// deploymentMethod is what this rule said about each environment it named
	// — "auto" or "manual". Only ever populated from the allow set — see
	// Change.DeploymentMethod. An environment this rule did not name at all
	// carries no opinion, which intersectDeploymentMethod treats the same as
	// an explicit "manual": either way, this rule did not vouch for auto.
	deploymentMethod map[string]string
}

// intersectDeploymentMethod folds every allow rule that matched one change
// into the environments all of them agree are safe to automate.
//
// Intersection, not union: two rules matching the same resource both have to
// say "auto" for an environment before it counts, the same way one denying
// rule is enough to keep the whole change from passing. A rule that named no
// environments at all, or named this one "manual" — every rule except the
// ones written for this — still fails to contribute "auto" for it, which is
// why leaving deployment_method off a rule, or naming an environment
// "manual" there, is enough to keep every change it matches manual for that
// environment.
func intersectDeploymentMethod(matches []ruleMatch) map[string]string {
	if len(matches) == 0 {
		return nil
	}
	counts := map[string]int{}
	for _, m := range matches {
		for env, method := range m.deploymentMethod {
			if method == "auto" {
				counts[env]++
			}
		}
	}
	out := map[string]string{}
	for env, n := range counts {
		if n == len(matches) {
			out[env] = "auto"
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// judgements evaluates one rule set, returning what matched by resource
// address.
func (e *Evaluator) judgements(ctx context.Context, layer compiledLayer, verdict Verdict, plan any) (map[string][]ruleMatch, error) {
	rs, err := layer.queries[verdict].Eval(ctx, rego.EvalInput(plan))
	if err != nil {
		return nil, fmt.Errorf("evaluating %s: %w", Queries[verdict], err)
	}

	out := map[string][]ruleMatch{}
	for _, result := range rs {
		for _, expr := range result.Expressions {
			values, ok := expr.Value.([]any)
			if !ok {
				return nil, fmt.Errorf("%s returned %T, want a set of judgements", Queries[verdict], expr.Value)
			}
			for _, v := range values {
				address, m, err := decodeJudgement(v, Queries[verdict])
				if err != nil {
					return nil, err
				}
				out[address] = append(out[address], m)
			}
		}
	}
	return out, nil
}

// decodeJudgement reads one entry from a rule set.
func decodeJudgement(v any, query string) (address string, m ruleMatch, err error) {
	raw, marshalErr := json.Marshal(v)
	if marshalErr != nil {
		return "", ruleMatch{}, fmt.Errorf("re-encoding a judgement from %s: %w", query, marshalErr)
	}

	var decoded struct {
		Resource         string            `json:"resource"`
		Reason           string            `json:"reason"`
		DeploymentMethod map[string]string `json:"deployment_method,omitempty"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", ruleMatch{}, fmt.Errorf("%s produced %s, want an object with \"resource\" and \"reason\"", query, raw)
	}
	if decoded.Resource == "" {
		return "", ruleMatch{}, fmt.Errorf("%s produced a judgement with no \"resource\", so it cannot be attached to a change: %s", query, raw)
	}
	if decoded.Reason == "" {
		return "", ruleMatch{}, fmt.Errorf("%s produced a judgement with no \"reason\" for %s — say why, it goes in the summary", query, decoded.Resource)
	}
	for env, method := range decoded.DeploymentMethod {
		if method != "auto" && method != "manual" {
			return "", ruleMatch{}, fmt.Errorf(
				"%s named %s deployment_method %q for environment %q: it has to be \"auto\" or \"manual\"",
				query, decoded.Resource, method, env)
		}
	}
	return decoded.Resource, ruleMatch{reason: decoded.Reason, deploymentMethod: decoded.DeploymentMethod}, nil
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
