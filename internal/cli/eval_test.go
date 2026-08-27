package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// A merge request that changes no unit leaves the plan directory empty or
// absent. That is a pass with nothing to say, not a broken pipeline.
func TestCollectPlansToleratesMissingPlanDir(t *testing.T) {
	got, err := collectPlans(nil, filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatalf("collectPlans: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

func TestCollectPlansFindsPlansAndNamesThemByUnit(t *testing.T) {
	dir := t.TempDir()
	for _, unit := range []string{"terraform/kafka/prd", "terraform/kafka/stg"} {
		path := filepath.Join(dir, unit)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(path, "plan.json"), []byte(`{}`), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// Not a plan file, so it must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := collectPlans(nil, dir)
	if err != nil {
		t.Fatalf("collectPlans: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d plans, want 2: %+v", len(got), got)
	}
	// Sorted, and named by unit path rather than by temp directory.
	if got[0].name != "terraform/kafka/prd" || got[1].name != "terraform/kafka/stg" {
		t.Errorf("names = %q, %q", got[0].name, got[1].name)
	}
}

func TestParseGroupIDs(t *testing.T) {
	got, err := parseGroupIDs([]string{"12", " 34 ", ""})
	if err != nil {
		t.Fatalf("parseGroupIDs: %v", err)
	}
	if len(got) != 2 || got[0] != 12 || got[1] != 34 {
		t.Errorf("got %v, want [12 34]", got)
	}

	if _, err := parseGroupIDs([]string{"not-a-number"}); err == nil {
		t.Error("expected an error for a non-numeric group id")
	}
}

func TestTrippedGuardsMatching(t *testing.T) {
	tests := []struct {
		name    string
		changed string
		guard   string
		want    bool
	}{
		{"file inside a guarded directory", "policy/rules.rego", "policy", true},
		{"nested inside a guarded directory", "policy/aws/s3.rego", "policy", true},
		{"the guarded file itself", ".gitlab-ci.yml", ".gitlab-ci.yml", true},
		{"unrelated file", "terraform/main.tf", "policy", false},
		// "policyholder" starts with "policy" but is a different directory.
		{"directory with a shared prefix", "policyholder/x.rego", "policy", false},

		// Pattern forms, for ignore lists that cannot be written out by hand.
		{"same name in any directory", "terraform/kafka/README.md", "**/README.md", true},
		{"that name at the top level too", "README.md", "**/README.md", true},
		{"a different name", "terraform/kafka/CHANGELOG.md", "**/README.md", false},
		{"glob under **", "terraform/kafka/notes.md", "**/*.md", true},
		{"glob on the whole path", "docs/index.md", "docs/*.md", true},
		// path.Match's * does not cross a separator, so this stays a miss.
		{"glob does not cross directories", "docs/api/index.md", "docs/*.md", false},
		// A pattern is not a prefix: naming a glob does not sweep in a tree.
		{"literal path is still a prefix", "policy/aws/s3.rego", "policy", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesPath(tc.changed, []string{tc.guard})
			if got != tc.want {
				t.Errorf("matchesPath(%q, %q) = %v, want %v", tc.changed, tc.guard, got, tc.want)
			}
		})
	}
}
