package baseline

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWriteThenRead(t *testing.T) {
	dir := t.TempDir()
	want := Result{
		Ref:       "origin/main",
		Commit:    "2907a6899eda3dfb5a06647963bf5b5759eca6e6",
		State:     Dirty,
		Addresses: []string{`kafka_topic.topics["scp.example.v1"]`},
	}
	if err := Write(dir, want); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, found, err := Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !found {
		t.Fatal("want found, got not found")
	}
	if got.Ref != want.Ref || got.Commit != want.Commit || got.State != want.State {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if !slices.Equal(got.Addresses, want.Addresses) {
		t.Errorf("addresses: got %v, want %v", got.Addresses, want.Addresses)
	}
}

func TestReadMissingIsNotAnError(t *testing.T) {
	// A missing sidecar is a fact the caller has to weigh, not a broken run:
	// a plan handed straight to --plan has no baseline, and neither does one
	// from a blastdoor old enough not to write them. eval is what decides that
	// a unit with changes and no baseline denies.
	_, found, err := Read(t.TempDir())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if found {
		t.Fatal("want not found, got found")
	}
}

func TestReadRejectsMalformed(t *testing.T) {
	tests := map[string]string{
		"truncated":     `{"state":`,
		"unknown state": `{"ref":"origin/main","commit":"abc","state":"probably-fine"}`,
		"empty state":   `{"ref":"origin/main","commit":"abc"}`,
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			// A sidecar that cannot be understood is not a clean one.
			if _, _, err := Read(dir); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
}
