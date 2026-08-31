package report

import (
	"fmt"
	"regexp"
	"strings"
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
		if !dotenvKey.MatchString(strings.ToUpper(name)) {
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
