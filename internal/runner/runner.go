// Package runner produces plan JSON for a unit by shelling out to OpenTofu,
// Terraform or Terragrunt.
//
// Which version runs is left to a version manager — tenv or mise, whichever
// the unit is set up for. See toolchain.go.
package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Tool selects which binary plans a unit.
type Tool string

const (
	// ToolAuto picks a tool by looking at the files in the unit.
	ToolAuto Tool = "auto"
	// ToolTofu runs OpenTofu.
	ToolTofu Tool = "tofu"
	// ToolTerraform runs Terraform.
	ToolTerraform Tool = "terraform"
	// ToolTerragrunt runs Terragrunt.
	ToolTerragrunt Tool = "terragrunt"
)

// Options configures a plan run.
type Options struct {
	// Tool is the binary to run, or ToolAuto to detect one.
	Tool Tool
	// TerragruntTFPath is the binary Terragrunt wraps. Defaults to tofu.
	TerragruntTFPath string
	// Manager is the version manager, or ManagerAuto to detect one.
	Manager Manager
	// Log receives the tool's stdout/stderr. Defaults to os.Stderr.
	Log io.Writer
}

// Result is one unit's plan and what produced it.
type Result struct {
	// JSON is the plan, as 'show -json' wrote it.
	JSON []byte
	// Engine is the binary that actually planned: tofu or terraform. When
	// Terragrunt runs the unit it is the binary Terragrunt wrapped, not
	// Terragrunt itself, because that is what read the configuration and
	// decided what would change.
	Engine string
}

// Plan runs init, plan and show for a unit and returns the plan as JSON.
func Plan(ctx context.Context, unitDir string, opts Options) (Result, error) {
	tool := opts.Tool
	if tool == "" || tool == ToolAuto {
		var err error
		if tool, err = Detect(unitDir); err != nil {
			return Result{}, err
		}
	}

	manager := opts.Manager
	if manager == "" || manager == ManagerAuto {
		manager = DetectManager(unitDir)
	}

	log := opts.Log
	if log == nil {
		log = os.Stderr
	}

	// Terragrunt is a wrapper: the plan is produced by whichever binary it
	// wraps, and that is what the report should name.
	engine := tool

	var extraEnv []string
	if tool == ToolTerragrunt {
		wrapped, why := resolveTerragruntTF(ctx, unitDir, opts, manager)
		engine = wrapped
		fmt.Fprintf(log, "terragrunt wrapping %s (%s)\n", wrapped, why)
		// TG_TF_PATH is Terragrunt v0.73+; TERRAGRUNT_TFPATH is the older
		// name. Setting both keeps either version working.
		extraEnv = append(extraEnv,
			"TG_TF_PATH="+string(wrapped),
			"TERRAGRUNT_TFPATH="+string(wrapped))
	}

	binary := string(tool)
	planFile := "blastdoor.tfplan"

	run := func(args ...string) error {
		cmd := toolCommand(ctx, manager, unitDir, binary, extraEnv, args...)
		cmd.Stdout = log
		cmd.Stderr = log
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s %s in %s: %w", binary, args[0], unitDir, err)
		}
		return nil
	}

	if err := run("init", "-input=false"); err != nil {
		return Result{}, err
	}
	if err := run("plan", "-input=false", "-out="+planFile); err != nil {
		return Result{}, err
	}
	defer os.Remove(filepath.Join(unitDir, planFile))

	// show writes the JSON to stdout, so it does not share the log writer.
	show := toolCommand(ctx, manager, unitDir, binary, extraEnv, "show", "-json", planFile)
	show.Stderr = log
	out, err := show.Output()
	if err != nil {
		return Result{}, fmt.Errorf("%s show -json in %s: %w", binary, unitDir, err)
	}
	return Result{JSON: out, Engine: string(engine)}, nil
}

// Detect picks a tool for a unit: Terragrunt if it is a Terragrunt unit,
// otherwise whichever of OpenTofu/Terraform the repository pins.
func Detect(unitDir string) (Tool, error) {
	if _, err := os.Stat(filepath.Join(unitDir, "terragrunt.hcl")); err == nil {
		return ToolTerragrunt, nil
	}

	if pinned, _, ok := PinnedTool(unitDir); ok {
		return pinned, nil
	}

	tf, err := filepath.Glob(filepath.Join(unitDir, "*.tf"))
	if err != nil {
		return "", err
	}
	if len(tf) > 0 {
		// Nothing pinned, so OpenTofu.
		return ToolTofu, nil
	}
	return "", fmt.Errorf("%s has no terragrunt.hcl and no .tf files — not a unit", unitDir)
}

// PinnedTool reports which of OpenTofu/Terraform the repository pins for a
// unit, and the file that says so.
//
// The search walks up from the unit, because that is how tenv resolves a
// version: a .terraform-version several directories above a Terragrunt unit
// still governs it. Looking only inside the unit would miss it and quietly
// fall back to OpenTofu against a Terraform repository.
//
// The nearest file wins. A directory holding both is ambiguous, and OpenTofu
// wins there.
func PinnedTool(unitDir string) (tool Tool, file string, ok bool) {
	dir, err := filepath.Abs(unitDir)
	if err != nil {
		return "", "", false
	}

	for {
		for _, candidate := range []struct {
			name string
			tool Tool
		}{
			{".opentofu-version", ToolTofu},
			{".terraform-version", ToolTerraform},
		} {
			path := filepath.Join(dir, candidate.name)
			if _, err := os.Stat(path); err == nil {
				return candidate.tool, path, true
			}
		}

		// A repository root is as far as a unit's configuration reaches.
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return "", "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false
		}
		dir = parent
	}
}
