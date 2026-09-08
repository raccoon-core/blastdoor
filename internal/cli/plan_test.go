package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/baseline"
)

const planWithACreate = `{"format_version":"1.2","resource_changes":[
	{"address":"kafka_topic.a","mode":"managed","type":"kafka_topic",
	 "change":{"actions":["create"]}}]}`

const planWithNothingToDo = `{"format_version":"1.2","resource_changes":[
	{"address":"kafka_topic.a","mode":"managed","type":"kafka_topic",
	 "change":{"actions":["no-op"]}}]}`

func TestBaselineSkippedWhenTheHeadPlanAppliesNothing(t *testing.T) {
	dest := t.TempDir()

	// The revert case: a merge request that clears the backlog has nothing of
	// its own to apply, so it needs no baseline and is not denied by one.
	planned, err := recordBaseline(t.Context(), nil, "units/a", []byte(planWithNothingToDo), dest, planFn(t, "unused"))
	if err != nil {
		t.Fatalf("recordBaseline: %v", err)
	}
	if planned {
		t.Error("planned a baseline for a unit that applies nothing")
	}
	if _, found, _ := baseline.Read(dest); found {
		t.Error("wrote a baseline sidecar for a unit that applies nothing")
	}
}

func TestBaselineRecordsDirtyWhenTheBaselineHasChanges(t *testing.T) {
	dest := t.TempDir()

	planned, err := recordBaseline(t.Context(), stubWorktree(t), "units/a", []byte(planWithACreate), dest, planFn(t, planWithACreate))
	if err != nil {
		t.Fatalf("recordBaseline: %v", err)
	}
	if !planned {
		t.Fatal("did not plan a baseline for a unit that applies something")
	}

	got, found, err := baseline.Read(dest)
	if err != nil || !found {
		t.Fatalf("Read: %v, found=%v", err, found)
	}
	if got.State != baseline.Dirty {
		t.Errorf("state = %q, want dirty", got.State)
	}
	if len(got.Addresses) != 1 || got.Addresses[0] != "kafka_topic.a" {
		t.Errorf("addresses = %v, want [kafka_topic.a]", got.Addresses)
	}
}

func TestBaselineRecordsCleanWhenTheBaselineHasNothing(t *testing.T) {
	dest := t.TempDir()

	if _, err := recordBaseline(t.Context(), stubWorktree(t), "units/a", []byte(planWithACreate), dest, planFn(t, planWithNothingToDo)); err != nil {
		t.Fatalf("recordBaseline: %v", err)
	}

	got, _, err := baseline.Read(dest)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.State != baseline.Clean {
		t.Errorf("state = %q, want clean", got.State)
	}
}

func TestBaselineRecordsAbsentForANewUnit(t *testing.T) {
	dest := t.TempDir()

	// A unit this merge request creates is not at the baseline. Nothing can be
	// pending on a unit that does not exist, so absent is clean.
	wt := stubWorktree(t)
	wt.units = nil

	if _, err := recordBaseline(t.Context(), wt, "units/a", []byte(planWithACreate), dest, planFn(t, "must not be called")); err != nil {
		t.Fatalf("recordBaseline: %v", err)
	}

	got, _, err := baseline.Read(dest)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.State != baseline.Absent {
		t.Errorf("state = %q, want absent", got.State)
	}
}

func TestBaselineFailsOnAnUnreadablePlan(t *testing.T) {
	dest := t.TempDir()

	// A plan that cannot be read must not come back as a clean baseline.
	if _, err := recordBaseline(t.Context(), stubWorktree(t), "units/a", []byte(planWithACreate), dest, planFn(t, `{"truncated":`)); err == nil {
		t.Fatal("want an error, got nil")
	}
}

// --- helpers ---

// stubWorktree is a baselineTree that reports one unit present and never
// touches git.
func stubWorktree(t *testing.T) *fakeTree {
	t.Helper()
	return &fakeTree{units: map[string]string{"units/a": filepath.Join(t.TempDir(), "units", "a")}}
}

type fakeTree struct {
	units map[string]string
}

func (f *fakeTree) Ref() string    { return "origin/main" }
func (f *fakeTree) Commit() string { return "2907a6899eda3dfb5a06647963bf5b5759eca6e6" }
func (f *fakeTree) UnitDir(unit string) (string, bool) {
	dir, ok := f.units[unit]
	return dir, ok
}

// planFn returns a planner that yields the given JSON, and fails the test if
// it is called when it should not have been.
func planFn(t *testing.T, out string) planner {
	t.Helper()
	return func(_ context.Context, _ string) ([]byte, error) {
		if out == "must not be called" || out == "unused" {
			t.Fatalf("planner called when it should not have been")
		}
		return []byte(out), nil
	}
}
