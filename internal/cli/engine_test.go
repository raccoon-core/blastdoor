package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// planTree lays out a plan directory the way 'blastdoor plan' writes one:
// a plan.json per unit, and the engine that produced it beside it.
func planTree(t *testing.T, units map[string]string) []planInput {
	t.Helper()
	dir := t.TempDir()

	var plans []planInput
	for unit, engine := range units {
		unitDir := filepath.Join(dir, unit)
		if err := os.MkdirAll(unitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		planFile := filepath.Join(unitDir, "plan.json")
		if err := os.WriteFile(planFile, []byte(`{"format_version":"1.2"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if engine != "" {
			if err := os.WriteFile(filepath.Join(unitDir, "engine.txt"), []byte(engine+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		plans = append(plans, planInput{name: unit, file: planFile})
	}
	return plans
}

func TestEnginesForReadsWhatPlanRecorded(t *testing.T) {
	got := enginesFor(planTree(t, map[string]string{"a": "terraform"}))

	if !reflect.DeepEqual(got, []string{"terraform"}) {
		t.Errorf("got %v, want [terraform]", got)
	}
}

// Twelve units on the same engine are one engine in the heading.
func TestEnginesForDeduplicates(t *testing.T) {
	got := enginesFor(planTree(t, map[string]string{"a": "tofu", "b": "tofu", "c": "tofu"}))

	if !reflect.DeepEqual(got, []string{"tofu"}) {
		t.Errorf("got %v, want [tofu]", got)
	}
}

// Plans passed straight to --plan, or written by an older blastdoor, have no
// engine recorded. That is silence, not an error: the note then says nothing
// about the engine rather than guessing.
func TestEnginesForToleratesMissingFiles(t *testing.T) {
	got := enginesFor(planTree(t, map[string]string{"a": ""}))

	if len(got) != 0 {
		t.Errorf("got %v, want nothing", got)
	}
}

// A repository part-way through a migration reports both, and the units
// without a record do not erase the ones that have it.
func TestEnginesForReportsAMixedRun(t *testing.T) {
	got := enginesFor(planTree(t, map[string]string{"a": "terraform", "b": "tofu", "c": ""}))

	want := map[string]bool{"terraform": true, "tofu": true}
	if len(got) != 2 {
		t.Fatalf("got %v, want both engines", got)
	}
	for _, e := range got {
		if !want[e] {
			t.Errorf("unexpected engine %q in %v", e, got)
		}
	}
}
