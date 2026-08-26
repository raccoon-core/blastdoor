package report

import (
	"strings"
	"testing"
)

func summaryWithLayers(t *testing.T, layers ...Layer) string {
	t.Helper()
	rep := Build(nil)
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
	got := summaryWithLayers(t, Layer{Name: "local", Repository: ".", Weight: 0})

	if !strings.Contains(got, "Judged by: local") {
		t.Errorf("summary does not name the layer:\n%s", got)
	}
	// With one layer there is no order to explain.
	if strings.Contains(got, "highest weight first") {
		t.Errorf("a single layer needs no ordering note:\n%s", got)
	}
}

// A remote layer carries the ref and the commit it resolved to, because a ref
// like "main" moves and a verdict has to be explainable afterwards.
func TestRemoteLayerShowsRefAndCommit(t *testing.T) {
	got := summaryWithLayers(t,
		Layer{Name: "local", Repository: ".", Weight: 99},
		Layer{Name: "company", Repository: "https://git.example.com/p", Ref: "main", Commit: "470da522ecbd3dda4c3866f698b289b56a564811", Weight: 0},
	)

	for _, want := range []string{"Judged by:", "local", "company (main@470da52)", "highest weight first"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
}

// Nothing recorded means nothing claimed.
func TestNoLayersSaysNothing(t *testing.T) {
	if got := summaryWithLayers(t); strings.Contains(got, "Judged by") {
		t.Errorf("summary should not claim layers it does not have:\n%s", got)
	}
}
