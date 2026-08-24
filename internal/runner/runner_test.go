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
