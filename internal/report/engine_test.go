package report

import (
	"strings"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/policy"
)

func headingOf(t *testing.T, rep Report) string {
	t.Helper()
	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	return strings.SplitN(b.String(), "\n", 2)[0]
}

// The note says which engine produced the plans it is reporting on, so a
// reader knows what actually ran without opening the job log.
func TestHeadingNamesTheEngine(t *testing.T) {
	tests := []struct {
		name    string
		engines []string
		want    string
	}{
		{"terraform", []string{"terraform"}, "## Terraform Blastdoor"},
		{"opentofu", []string{"tofu"}, "## OpenTofu Blastdoor"},
		// A repository part-way through a migration plans with both.
		{"both", []string{"terraform", "tofu"}, "## Terraform + OpenTofu Blastdoor"},
		// Nothing recorded: plans passed with --plan, or produced by an
		// older blastdoor. Saying nothing beats guessing wrong.
		{"unknown", nil, "## Blastdoor"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := Build([]Unit{{Path: "a", Changes: []policy.Change{change("x", policy.Pass, "fine")}}})
			rep.Engines = tc.engines

			if got := headingOf(t, rep); got != tc.want {
				t.Errorf("heading = %q, want %q", got, tc.want)
			}
		})
	}
}

// The same engine reported by twelve units is still one engine.
func TestHeadingDeduplicatesEngines(t *testing.T) {
	rep := Build(nil)
	rep.Engines = []string{"terraform", "terraform", "terraform"}

	if got := headingOf(t, rep); got != "## Terraform Blastdoor" {
		t.Errorf("heading = %q", got)
	}
}

// An engine blastdoor does not have a name for is passed through rather than
// dropped: a wrong name is worse than an unfamiliar one.
func TestHeadingKeepsAnUnknownEngineName(t *testing.T) {
	rep := Build(nil)
	rep.Engines = []string{"terraform-fork"}

	if got := headingOf(t, rep); !strings.Contains(got, "terraform-fork") {
		t.Errorf("heading = %q, want it to name the engine", got)
	}
}
