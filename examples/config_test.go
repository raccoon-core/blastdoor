// This file keeps examples/blastdoor.yml complete: it is documented as every
// setting blastdoor understands, so a key added to config.Config that the
// example does not show makes this fail. A reference config that quietly goes
// out of date is worse than none — it reads as a statement that the missing
// key does not exist.
package examples_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/raccoon-core/blastdoor/internal/config"
)

const exampleConfig = "blastdoor.yml"

// TestExampleConfigLoads runs the example through the real loader, which
// rejects unknown keys and validates the policy layers. A weight left off a
// layer, or a remote source with no ref, fails here rather than in a pipeline.
func TestExampleConfigLoads(t *testing.T) {
	cfg, err := config.Load(exampleConfig)
	if err != nil {
		t.Fatalf("loading %s: %v", exampleConfig, err)
	}
	if len(cfg.Policies) == 0 {
		t.Errorf("%s defines no policy layers, so it does not show what one looks like", exampleConfig)
	}
}

// TestExampleConfigShowsEverySetting pins the file to the struct: every yaml
// key on config.Config has to appear at the top level of the example.
func TestExampleConfigShowsEverySetting(t *testing.T) {
	var doc map[string]any
	readYAML(t, &doc)

	for _, key := range yamlKeys(reflect.TypeOf(config.Config{})) {
		if _, ok := doc[key]; !ok {
			t.Errorf("%s does not set %q — add it, with a comment saying what it does and what it defaults to", exampleConfig, key)
		}
	}
}

// TestExampleConfigShowsEverySourceField does the same for a policy layer.
// The layers differ on purpose — a local source takes no ref — so the fields
// are looked for across all of them rather than in any single one.
func TestExampleConfigShowsEverySourceField(t *testing.T) {
	var doc struct {
		Policies map[string]map[string]any `yaml:"policies"`
	}
	readYAML(t, &doc)

	for _, key := range yamlKeys(reflect.TypeOf(config.Source{})) {
		found := false
		for _, layer := range doc.Policies {
			if _, ok := layer[key]; ok {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no policy layer in %s sets %q — one of them should show it", exampleConfig, key)
		}
	}
}

func readYAML(t *testing.T, into any) {
	t.Helper()
	raw, err := os.ReadFile(exampleConfig)
	if err != nil {
		t.Fatalf("reading %s: %v", exampleConfig, err)
	}
	if err := yaml.Unmarshal(raw, into); err != nil {
		t.Fatalf("parsing %s: %v", exampleConfig, err)
	}
}

// yamlKeys lists the yaml names of a struct's fields, skipping the ones tagged
// "-" — Path is filled in by the loader, not written by hand.
func yamlKeys(typ reflect.Type) []string {
	var keys []string
	for i := range typ.NumField() {
		tag, _, _ := strings.Cut(typ.Field(i).Tag.Get("yaml"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		keys = append(keys, tag)
	}
	return keys
}
