package runner

import (
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
