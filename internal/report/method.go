package report

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/raccoon-core/blastdoor/internal/policy"
)

// Method is how an environment's changes reach the infrastructure.
type Method string

const (
	// Auto applies with nobody watching.
	Auto Method = "auto"
	// Manual waits for a person to start the job.
	Manual Method = "manual"
	// None is an environment with nothing to apply, which is not the same as
	// one that is safe to apply automatically.
	None Method = "none"
)

// Wish is the ceiling the pipeline states, in the order it named the
// environments.
//
// The order is kept rather than sorted because "int, stg, prd" is a promotion
// order and reads as one. Sorting gives "int, prd, stg", which puts production
// in the middle of the summary table where a reader skims past it.
type Wish struct {
	order []string
	byEnv map[string]Method
}

// dotenvKey is what GitLab accepts as a variable name. An environment name
// becomes one, so a name that is not one has to fail while it can still be
// attributed to the wish that stated it.
var dotenvKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseWish reads "int=auto,stg=auto,prd=manual".
//
// Comma-separated rather than space-separated on purpose: the package doc of
// internal/config records what happened the last time a list travelled through
// a CI variable with a chance for the shell to expand it first.
func ParseWish(s string) (Wish, error) {
	w := Wish{byEnv: map[string]Method{}}

	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			return Wish{}, fmt.Errorf("deployment method wish %q is not <environment>=<method>", entry)
		}
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)

		if name == "" {
			return Wish{}, fmt.Errorf("deployment method wish %q names no environment", entry)
		}
		// Validated raw, not uppercased first: strings.ToUpper folds some
		// non-ASCII runes into ASCII (ı, U+0131, becomes I; ſ, U+017F,
		// becomes S), which would let a name that fails this regex as
		// written pass it once folded — and then get stored, and matched
		// for duplicates, under the raw name it never actually validated.
		if !dotenvKey.MatchString(name) {
			return Wish{}, fmt.Errorf(
				"environment %q cannot become a dotenv variable name: use letters, digits and underscores, not starting with a digit", name)
		}
		if _, dup := w.byEnv[name]; dup {
			return Wish{}, fmt.Errorf("environment %q is named twice in the deployment method wish", name)
		}

		// None is not something a pipeline asks for. It is what the fold says
		// about an environment nothing changed in.
		switch Method(value) {
		case Auto, Manual:
		default:
			return Wish{}, fmt.Errorf("environment %q wishes for %q: it has to be auto or manual", name, value)
		}

		w.order = append(w.order, name)
		w.byEnv[name] = Method(value)
	}
	return w, nil
}

// Stated reports whether any wish was given. Without one the whole feature is
// off and nothing new is written, so an existing pipeline is untouched.
func (w Wish) Stated() bool { return len(w.order) > 0 }

// Names lists the environments in the order the wish named them.
func (w Wish) Names() []string { return w.order }

// Method returns the ceiling for one environment.
func (w Wish) Method(env string) (Method, bool) {
	m, ok := w.byEnv[env]
	return m, ok
}

// EnvDecision is one environment's answer, and why.
type EnvDecision struct {
	Name      string         `json:"name"`
	Wish      Method         `json:"wish"`
	Method    Method         `json:"method"`
	Verdict   policy.Verdict `json:"verdict"`
	UnitCount int            `json:"unit_count"`
	// Reasons says why this is not auto, most specific first, and is empty when
	// it is. "prd is manual" and "prd is manual because a topic is being
	// deleted" are different facts, and the second is the one that says whether
	// the pipeline is working or misconfigured.
	Reasons []string `json:"reasons,omitempty"`
}

// Decide folds the units into one deployment method per environment.
//
// It must run AFTER RequireReview and RequireCoverage. Both force review across
// the whole repository, and an environment cannot apply unattended while the
// change is rewriting the rules that judge it — but Decide can only see that
// once those have been recorded.
//
// Two independent things can push an environment towards manual, and neither
// can push the other way: the wish is a ceiling a pipeline states (nothing
// turns a manual wish into an auto apply), and a policy's own allow rules are
// a floor (nothing applies unattended that no rule vouched for — see
// policy.Change.Auto). A wish is optional; the environments considered come
// from whatever 'blastdoor plan --environment' recorded on the units
// themselves, not from the wish naming them. When a wish IS stated, though,
// it still has to name every environment a unit was placed in — see the
// per-unit check below — so a pipeline that opts in gets the same strict
// promise it always did.
func (r *Report) Decide(w Wish) error {
	envs := environmentNames(r.Units)

	if w.Stated() {
		// A unit nobody can place would be applied by a job nobody
		// configured, or by no job at all — and "silently skipped" is
		// indistinguishable from "applied fine" in every artefact
		// downstream. This promise only makes sense once a pipeline has
		// opted in by stating a wish; without one, a unit with no
		// environment recorded just never joins any environment's rollup
		// below, which fails towards "not automated" rather than towards
		// a silent gap in a ceiling nobody asked for.
		for _, u := range r.Units {
			if u.Environment == "" {
				return fmt.Errorf(
					"unit %q has no environment recorded: pass --environment to 'blastdoor plan' so it writes one beside the plan", u.Path)
			}
			if _, ok := w.Method(u.Environment); !ok {
				return fmt.Errorf("unit %q is in environment %q, which the deployment method wish does not name (it names %s)",
					u.Path, u.Environment, strings.Join(w.Names(), ", "))
			}
		}
		envs = w.Names()
	}

	if len(envs) == 0 {
		return nil
	}

	// Repository-wide, so they cannot be attributed to any one environment.
	// Both already force review, so the verdict test below would catch them
	// anyway; they are named separately so they survive someone later deciding
	// that a review verdict alone should not block an unattended apply.
	var wide []string
	if len(r.Guarded) > 0 {
		wide = append(wide, "the change edits guarded paths")
	}
	if len(r.Uncovered) > 0 {
		wide = append(wide, "the change edits files no plan covers")
	}

	for _, name := range envs {
		wish, _ := w.Method(name)
		d := EnvDecision{Name: name, Wish: wish, Method: Manual, Verdict: policy.Pass}
		policyAuto := true

		for _, u := range r.Units {
			if u.Environment != name {
				continue
			}
			d.UnitCount++
			d.Verdict = policy.Worse(d.Verdict, u.Verdict)
			if !unitAutoFor(u, name) {
				policyAuto = false
			}
		}

		// None is tested first, and the order is load-bearing: an environment
		// with no changed units has a vacuously passing verdict, so asking
		// about auto first resolves every untouched environment to auto and
		// generates an apply job for it.
		if d.UnitCount == 0 {
			d.Method = None
			d.Reasons = []string{"no unit changed in this environment"}
			r.Environments = append(r.Environments, d)
			continue
		}

		d.Reasons = manualReasons(d, policyAuto, wide)
		if len(d.Reasons) == 0 {
			d.Method = Auto
		}
		r.Environments = append(r.Environments, d)
	}
	return nil
}

// environmentNames lists the distinct, non-empty environments recorded on the
// units, in the order they were first seen. A unit with none recorded never
// contributes a name here — see the comment in Decide about what that means
// when no wish was stated.
func environmentNames(units []Unit) []string {
	var out []string
	seen := map[string]bool{}
	for _, u := range units {
		if u.Environment == "" || seen[u.Environment] {
			continue
		}
		seen[u.Environment] = true
		out = append(out, u.Environment)
	}
	return out
}

// unitAutoFor reports whether every change in a unit named an environment as
// safe to automate. A unit with no changes at all — nothing for this
// environment's plan to apply — is vacuously fine, the same way an empty verdict
// fold is vacuously Pass.
func unitAutoFor(u Unit, env string) bool {
	for _, c := range u.Changes {
		if c.DeploymentMethod[env] != "auto" {
			return false
		}
	}
	return true
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// manualReasons lists why an environment cannot apply unattended. Empty means
// nothing objects, which is the only way to auto.
func manualReasons(d EnvDecision, policyAuto bool, wide []string) []string {
	var out []string
	if d.Wish == Manual {
		out = append(out, "the pipeline asks for a manual apply in this environment")
	}
	if d.Verdict != policy.Pass {
		out = append(out, fmt.Sprintf("the verdict here is %s", d.Verdict))
	} else if !policyAuto {
		out = append(out, "no policy rule marked this environment safe to automate")
	}
	return append(out, wide...)
}
