package report

import (
	"strings"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/policy"
)

func summaryWithLayers(t *testing.T, layers ...Layer) string {
	t.Helper()
	rep := Build([]Unit{{Path: "u", Changes: []policy.Change{change("kafka_topic.x", policy.Pass, "fine")}}})
	rep.Layers = layers

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	return b.String()
}

// One layer is still worth naming: a reader has to know what judged the
// change without opening the config.
func TestOneLayerIsNamed(t *testing.T) {
	got := summaryWithLayers(t, Layer{Name: "local", Repository: ".", Directory: "policy", Weight: 0})

	if !strings.Contains(got, "Judged by") || !strings.Contains(got, "local") {
		t.Errorf("summary does not name the layer:\n%s", got)
	}
	// With one layer there is no order to explain.
	if strings.Contains(got, "highest weight first") {
		t.Errorf("a single layer needs no ordering note:\n%s", got)
	}
}

// The name is chosen by whoever wrote the config, so it cannot be the only
// thing said: a reader asking "what judged this?" needs somewhere to go and
// read it.
func TestLayerNamesRepositoryAndDirectory(t *testing.T) {
	got := summaryWithLayers(t,
		Layer{Name: "company", Repository: "https://git.example.com/policies", Directory: "rules/company", Ref: "main", Commit: "470da522ecbd3dda4c3866f698b289b56a564811", Weight: 0},
	)

	for _, want := range []string{
		"company",
		"https://git.example.com/policies",
		"rules/company",
		"main",
		"470da52", // the ref moves; the commit is what makes this good later
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
}

// A local layer says so rather than printing a bare dot.
func TestLocalLayerSaysThisRepository(t *testing.T) {
	got := summaryWithLayers(t, Layer{Name: "local", Repository: ".", Directory: "policy", Weight: 99})

	if !strings.Contains(got, "this repository") {
		t.Errorf("summary should say the layer is local:\n%s", got)
	}
}

// It goes last: a verdict and the changes come first, and what judged them
// answers a question rather than delaying one.
func TestLayerBlockComesLast(t *testing.T) {
	got := summaryWithLayers(t,
		Layer{Name: "local", Repository: ".", Directory: "policy", Weight: 99},
		Layer{Name: "company", Repository: "https://git.example.com/p", Directory: "rules", Ref: "v1", Commit: "abcdef1234567", Weight: 0},
	)

	judged := strings.Index(got, "Judged by")
	table := strings.Index(got, "| Verdict |")
	if judged < 0 || table < 0 {
		t.Fatalf("summary is missing a section:\n%s", got)
	}
	if judged < table {
		t.Errorf("the layer list should follow the table, not precede it:\n%s", got)
	}
	if !strings.Contains(got, "highest weight first") {
		t.Errorf("several layers need their order explained:\n%s", got)
	}
}

// The layer list survives the paths that used to return early — a run with
// no units still has to say what judged it.
func TestLayerBlockSurvivesAnEmptyRun(t *testing.T) {
	rep := Build(nil)
	rep.Layers = []Layer{{Name: "company", Repository: "https://git.example.com/p", Directory: "rules", Ref: "v1", Commit: "abcdef1234567"}}

	var b strings.Builder
	if err := rep.WriteMarkdown(&b); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	if !strings.Contains(b.String(), "Judged by") {
		t.Errorf("a run with no units still has policies:\n%s", b.String())
	}
}

// Nothing recorded means nothing claimed.
func TestNoLayersSaysNothing(t *testing.T) {
	if got := summaryWithLayers(t); strings.Contains(got, "Judged by") {
		t.Errorf("summary should not claim layers it does not have:\n%s", got)
	}
}
