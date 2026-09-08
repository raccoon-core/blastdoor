// Package baseline records what the branch a merge request targets still has
// waiting to be applied, per unit.
//
// A plan is desired state against live state, not "what this merge request
// changed", so anything already pending on a unit turns up in the plan of the
// next merge request that touches it — and in the apply that follows its
// approval. This package holds the second plan that makes the difference
// visible.
package baseline

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileName is the sidecar's name, written beside the unit's plan.json.
//
// Per unit rather than once per run, for the same reason engine.txt and
// environment.txt are: eval runs in another job, and when plans are split
// across a parallel matrix their artifacts are merged. One file per unit
// merges; one file per run collides, and the survivor is whichever leg
// finished last.
const FileName = "baseline.json"

// State is what the baseline plan found for one unit.
type State string

const (
	// Clean means the baseline plan applies nothing.
	Clean State = "clean"
	// Dirty means the baseline still has changes waiting to be applied.
	Dirty State = "dirty"
	// Absent means the unit does not exist at the baseline at all, which is
	// what a unit this merge request creates looks like. Nothing can be
	// pending on a unit that is not there, so this is clean, not an error.
	Absent State = "absent"
)

// Result is one unit's baseline.
type Result struct {
	// Ref is the ref that was planned, as it was asked for.
	Ref string `json:"ref"`
	// Commit is what that ref resolved to. A ref moves; without the commit a
	// verdict cannot be explained afterwards. Same reasoning as report.Layer.
	Commit string `json:"commit"`
	State  State  `json:"state"`
	// Addresses are the pending changes, when State is Dirty.
	Addresses []string `json:"addresses,omitempty"`
}

// Write records a unit's baseline beside its plan.
func Write(dir string, r Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding baseline for %s: %w", dir, err)
	}
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// Read reads back a unit's baseline. The bool reports whether one was recorded.
//
// A missing file is not an error — it is a fact for the caller to weigh, and
// eval is where it becomes a denial. A file that is present but cannot be
// understood *is* an error: a sidecar nobody can read must never come out
// looking like a clean one.
func Read(dir string) (Result, bool, error) {
	path := filepath.Join(dir, FileName)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, fmt.Errorf("reading %s: %w", path, err)
	}

	var r Result
	if err := json.Unmarshal(raw, &r); err != nil {
		return Result{}, false, fmt.Errorf("parsing %s: %w", path, err)
	}
	switch r.State {
	case Clean, Dirty, Absent:
		return r, true, nil
	default:
		return Result{}, false, fmt.Errorf("%s: state is %q, want one of clean, dirty or absent", path, r.State)
	}
}
