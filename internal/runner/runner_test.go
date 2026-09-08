package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func unitWith(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# test\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  Tool
	}{
		{"terragrunt unit", []string{"terragrunt.hcl"}, ToolTerragrunt},
		// Terragrunt wins even next to .tf files: it is what drives the plan.
		{"terragrunt beside tf", []string{"terragrunt.hcl", "main.tf"}, ToolTerragrunt},
		{"opentofu version file", []string{".opentofu-version", "main.tf"}, ToolTofu},
		{"terraform version file", []string{".terraform-version", "main.tf"}, ToolTerraform},
		// OpenTofu is the default for a bare Terraform module.
		{"bare tf module", []string{"main.tf"}, ToolTofu},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Detect(unitWith(t, tc.files...))
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDetectRejectsNonUnit(t *testing.T) {
	if _, err := Detect(unitWith(t, "README.md")); err == nil {
		t.Fatal("expected an error for a directory that is not a unit")
	}
}

func TestHasMiseConfig(t *testing.T) {
	tests := []struct {
		name  string
		files []string // relative to the repo root created below
		unit  string
		want  bool
	}{
		{"config in the unit", []string{"a/b/mise.toml"}, "a/b", true},
		{"dotted config in the unit", []string{"a/b/.mise.toml"}, "a/b", true},
		{"asdf-style file in the unit", []string{"a/b/.tool-versions"}, "a/b", true},
		{"config at the repo root", []string{"mise.toml"}, "a/b", true},
		{"config in an intermediate directory", []string{"a/mise.toml"}, "a/b", true},
		{"no config anywhere", []string{"a/b/main.tf"}, "a/b", false},
		// tenv's files are not mise's, and must not be mistaken for them.
		{"only tenv version files", []string{"a/b/.terraform-version"}, "a/b", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			// A .git marks the repo root, which is as far as the walk goes.
			if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			for _, f := range tc.files {
				p := filepath.Join(root, f)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(p, []byte("# test\n"), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			unit := filepath.Join(root, tc.unit)
			if err := os.MkdirAll(unit, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}

			if got := hasMiseConfig(unit); got != tc.want {
				t.Errorf("hasMiseConfig = %v, want %v", got, tc.want)
			}
		})
	}
}

// A mise config above the repository root belongs to some other project, so
// the walk must stop at .git rather than wander up to the home directory.
func TestHasMiseConfigStopsAtRepoRoot(t *testing.T) {
	outer := t.TempDir()
	if err := os.WriteFile(filepath.Join(outer, "mise.toml"), []byte("[tools]\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	repo := filepath.Join(outer, "repo")
	unit := filepath.Join(repo, "unit")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(unit, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if hasMiseConfig(unit) {
		t.Error("a mise config outside the repository was picked up")
	}
}

// mise needs both a config and the binary; without the binary the unit is not
// a mise unit whatever its files say.
func TestDetectManagerNeedsTheBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mise.toml"), []byte("[tools]\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := DetectManager(dir)
	if onPath("mise") {
		if got != ManagerMise {
			t.Errorf("mise is installed, so DetectManager = %q, want %q", got, ManagerMise)
		}
		return
	}
	if got == ManagerMise {
		t.Error("chose mise even though it is not installed")
	}
}

// repoWith builds a repository containing the given files, and returns the
// root plus the unit directory.
func repoWith(t *testing.T, unit string, files ...string) (root, unitDir string) {
	t.Helper()
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, f := range files {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("1.2.3\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	unitDir = filepath.Join(root, unit)
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return root, unitDir
}

// A .terraform-version above the unit still governs it — that is how tenv
// resolves, and missing it plans a Terraform repository with OpenTofu.
func TestPinnedToolWalksUp(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  Tool
		found bool
	}{
		{"in the unit", []string{"tf/a/stg/.terraform-version"}, ToolTerraform, true},
		{"one level up", []string{"tf/a/.terraform-version"}, ToolTerraform, true},
		{"at the repository root", []string{".terraform-version"}, ToolTerraform, true},
		{"opentofu above the unit", []string{"tf/.opentofu-version"}, ToolTofu, true},
		{"nothing pinned", []string{"tf/a/stg/main.tf"}, "", false},
		// The nearest file wins, so a unit can differ from the rest.
		{
			"nearest wins",
			[]string{".opentofu-version", "tf/a/stg/.terraform-version"},
			ToolTerraform, true,
		},
		// Both in one directory is ambiguous; OpenTofu wins there.
		{
			"both in the same directory",
			[]string{"tf/a/.terraform-version", "tf/a/.opentofu-version"},
			ToolTofu, true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, unit := repoWith(t, "tf/a/stg", tc.files...)
			got, file, ok := PinnedTool(unit)
			if ok != tc.found {
				t.Fatalf("found = %v, want %v", ok, tc.found)
			}
			if ok && got != tc.want {
				t.Errorf("got %q (from %s), want %q", got, file, tc.want)
			}
		})
	}
}

// A pin belonging to some other project above the repository is not ours.
func TestPinnedToolStopsAtRepoRoot(t *testing.T) {
	outer := t.TempDir()
	if err := os.WriteFile(filepath.Join(outer, ".terraform-version"), []byte("1.0.0\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	unit := filepath.Join(outer, "repo", "unit")
	if err := os.MkdirAll(filepath.Join(outer, "repo", ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(unit, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, _, ok := PinnedTool(unit); ok {
		t.Error("a pin outside the repository was picked up")
	}
}

// The reported bug: a Terragrunt unit in a Terraform repository must wrap
// terraform, not tofu.
func TestTerragruntWrapsThePinnedTool(t *testing.T) {
	_, unit := repoWith(t, "tf/a/stg", "tf/a/.terraform-version", "tf/a/stg/terragrunt.hcl")

	tool, err := Detect(unit)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if tool != ToolTerragrunt {
		t.Fatalf("Detect = %q, want %q", tool, ToolTerragrunt)
	}

	wrapped, why := TerragruntTF(context.Background(), unit, ManagerTenv)
	if wrapped != ToolTerraform {
		t.Errorf("terragrunt would wrap %q (%s), want %q", wrapped, why, ToolTerraform)
	}
}

// With nothing pinned, OpenTofu.
func TestTerragruntDefaultsToTofu(t *testing.T) {
	_, unit := repoWith(t, "tf/a/stg", "tf/a/stg/terragrunt.hcl")

	wrapped, why := TerragruntTF(context.Background(), unit, ManagerTenv)
	if wrapped != ToolTofu {
		t.Errorf("got %q (%s), want %q", wrapped, why, ToolTofu)
	}
}

// An explicit flag beats whatever the repository says.
func TestExplicitTerragruntTFPathWins(t *testing.T) {
	_, unit := repoWith(t, "tf/a/stg", "tf/a/.terraform-version", "tf/a/stg/terragrunt.hcl")

	wrapped, why := resolveTerragruntTF(context.Background(), unit, Options{TerragruntTFPath: "tofu"}, ManagerTenv)
	if wrapped != ToolTofu {
		t.Errorf("got %q (%s), want the flag to win with %q", wrapped, why, ToolTofu)
	}
}

// A plain unit picks up an ancestor pin too, rather than defaulting to tofu.
func TestDetectUsesAnAncestorPin(t *testing.T) {
	_, unit := repoWith(t, "tf/a/stg", "tf/.terraform-version", "tf/a/stg/main.tf")

	got, err := Detect(unit)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got != ToolTerraform {
		t.Errorf("Detect = %q, want %q", got, ToolTerraform)
	}
}

const (
	tofuLockHeader      = "# This file is maintained automatically by \"tofu init\".\n"
	terraformLockHeader = "# This file is maintained automatically by \"terraform init\".\n"
)

func TestLockedTool(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    Tool
		found   bool
	}{
		{"opentofu wrote it", tofuLockHeader, ToolTofu, true},
		{"terraform wrote it", terraformLockHeader, ToolTerraform, true},
		{"no lock file", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.content != "" {
				p := filepath.Join(dir, ".terraform.lock.hcl")
				if err := os.WriteFile(p, []byte(tc.content), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			got, ok := LockedTool(dir)
			if ok != tc.found {
				t.Fatalf("found = %v, want %v", ok, tc.found)
			}
			if ok && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// No pin anywhere, but the lock file records what really ran here — trust
// that over guessing OpenTofu.
func TestDetectUsesTheLockFileWhenNothingIsPinned(t *testing.T) {
	unit := t.TempDir()
	if err := os.WriteFile(filepath.Join(unit, "main.tf"), []byte("# test\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unit, ".terraform.lock.hcl"), []byte(terraformLockHeader), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := Detect(unit)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got != ToolTerraform {
		t.Errorf("Detect = %q, want %q", got, ToolTerraform)
	}
}

// An explicit pin is cheaper to check and is what the repository asked for,
// so it wins even when the lock file disagrees — e.g. mid-migration, before
// the unit has been re-initialised with the newly pinned tool.
func TestPinnedToolWinsOverTheLockFile(t *testing.T) {
	unit := t.TempDir()
	if err := os.WriteFile(filepath.Join(unit, ".terraform-version"), []byte("1.2.3\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unit, ".terraform.lock.hcl"), []byte(tofuLockHeader), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unit, "main.tf"), []byte("# test\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := Detect(unit)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got != ToolTerraform {
		t.Errorf("Detect = %q, want %q: the pin file should win over a tofu-authored lock file", got, ToolTerraform)
	}
}

// Terragrunt wraps whichever binary the unit's own lock file names, when
// nothing above it is pinned.
func TestTerragruntWrapsTheLockedTool(t *testing.T) {
	_, unit := repoWith(t, "tf/a/stg", "tf/a/stg/terragrunt.hcl")
	if err := os.WriteFile(filepath.Join(unit, ".terraform.lock.hcl"), []byte(tofuLockHeader), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	wrapped, why := TerragruntTF(context.Background(), unit, ManagerTenv)
	if wrapped != ToolTofu {
		t.Errorf("terragrunt would wrap %q (%s), want %q", wrapped, why, ToolTofu)
	}
}
