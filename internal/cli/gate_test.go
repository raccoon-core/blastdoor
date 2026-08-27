package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/policy"
	"github.com/raccoon-core/blastdoor/internal/report"
)

// gitlab is a stand-in GitLab that records what the gate asked it to do.
type gitlab struct {
	mu    sync.Mutex
	calls []string
}

func (g *gitlab) record(method, path string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, method+" "+path)
}

func (g *gitlab) called(method, path string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.calls {
		if c == method+" "+path {
			return true
		}
	}
	return false
}

// gateRun runs `blastdoor gate` against a fake GitLab, with a report holding
// one change at the given verdict.
func gateRun(t *testing.T, verdict policy.Verdict, unitCount int, args ...string) *gitlab {
	t.Helper()

	fake := &gitlab{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.record(r.Method, r.URL.Path)
		switch {
		// The branch lookup that finds the merge request.
		case strings.HasSuffix(r.URL.Path, "/merge_requests") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"iid":7,"state":"opened"}]`))
		case strings.HasSuffix(r.URL.Path, "/approval_rules") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)

	rep := report.Report{Verdict: verdict, UnitCount: unitCount, Counts: map[policy.Verdict]int{verdict: 1}}
	if unitCount > 0 {
		rep.Units = []report.Unit{{Path: "u", Verdict: verdict,
			Changes: []policy.Change{{Address: "kafka_topic.x", Verdict: verdict, Reasons: []string{"because"}}}}}
	}

	dir := t.TempDir()
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(dir, "report.json")
	if err := os.WriteFile(reportPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(dir)
	restoreConfigState(t)
	t.Setenv("BLASTDOOR_CONFIG", "")
	configPath = ""

	base := []string{"gate",
		"--report", reportPath,
		"--skip-note",
		"--api-url", srv.URL,
		"--project-id", "42",
		"--token", "t",
		"--branch", "feature",
	}
	_ = run(t, append(base, args...)...)
	return fake
}

const mergePath = "/projects/42/merge_requests/7/merge"

// The whole point of the switch: everything passed, so queue the merge.
func TestAutoMergeOnPass(t *testing.T) {
	fake := gateRun(t, policy.Pass, 1, "--auto-merge")

	if !fake.called(http.MethodPut, mergePath) {
		t.Errorf("a passing change with --auto-merge should be queued to merge: %v", fake.calls)
	}
}

// Opt-in means off by default. A project that never asked for auto-merge must
// not get it because it happened to pass.
func TestNoAutoMergeUnlessAskedFor(t *testing.T) {
	fake := gateRun(t, policy.Pass, 1)

	if fake.called(http.MethodPut, mergePath) {
		t.Errorf("auto-merge fired without being asked for: %v", fake.calls)
	}
}

// The safety property. A verdict that is not a pass must never merge itself,
// however loudly the flag was passed — this is also what stops a merge request
// that trips a guard, or edits files no plan covers, from merging unseen,
// because both of those come back as review.
func TestAutoMergeOnlyOnPass(t *testing.T) {
	for _, verdict := range []policy.Verdict{policy.Review, policy.Deny} {
		t.Run(string(verdict), func(t *testing.T) {
			fake := gateRun(t, verdict, 1, "--auto-merge", "--approval-rule")

			if fake.called(http.MethodPut, mergePath) {
				t.Errorf("%s merged itself: %v", verdict, fake.calls)
			}
		})
	}
}

// Zero units is what a misconfigured root looks like as well as an empty
// change. Merging on the strength of having read nothing is the worst case.
func TestAutoMergeAbstainsWhenNothingWasScored(t *testing.T) {
	fake := gateRun(t, policy.Pass, 0, "--auto-merge")

	if fake.called(http.MethodPut, mergePath) {
		t.Errorf("merged without having scored anything: %v", fake.calls)
	}
	if fake.called(http.MethodPost, "/projects/42/merge_requests/7/approve") {
		t.Errorf("approved without having scored anything: %v", fake.calls)
	}
}
