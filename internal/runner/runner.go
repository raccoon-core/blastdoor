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

// Plan runs init, plan and show for a unit and returns the plan as JSON.
func Plan(ctx context.Context, unitDir string, opts Options) ([]byte, error) {
	tool := opts.Tool
	if tool == "" || tool == ToolAuto {
		var err error
		if tool, err = Detect(unitDir); err != nil {
			return nil, err
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

	var extraEnv []string
	if tool == ToolTerragrunt {
		tfPath := opts.TerragruntTFPath
		if tfPath == "" {
			tfPath = string(ToolTofu)
		}
		// TG_TF_PATH is Terragrunt v0.73+; TERRAGRUNT_TFPATH is the older
		// name. Setting both keeps either version working.
		extraEnv = append(extraEnv, "TG_TF_PATH="+tfPath, "TERRAGRUNT_TFPATH="+tfPath)
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
		return nil, err
	}
	if err := run("plan", "-input=false", "-out="+planFile); err != nil {
		return nil, err
	}
	defer os.Remove(filepath.Join(unitDir, planFile))

	// show writes the JSON to stdout, so it does not share the log writer.
	show := toolCommand(ctx, manager, unitDir, binary, extraEnv, "show", "-json", planFile)
	show.Stderr = log
	out, err := show.Output()
	if err != nil {
		return nil, fmt.Errorf("%s show -json in %s: %w", binary, unitDir, err)
	}
	return out, nil
}

// Detect picks a tool for a unit: Terragrunt if it is a Terragrunt unit,
// otherwise whichever of OpenTofu/Terraform the version files point at,
// defaulting to OpenTofu.
func Detect(unitDir string) (Tool, error) {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(unitDir, name))
		return err == nil
	}

	switch {
	case exists("terragrunt.hcl"):
		return ToolTerragrunt, nil
	case exists(".opentofu-version"):
		return ToolTofu, nil
	case exists(".terraform-version"):
		return ToolTerraform, nil
	}

	tf, err := filepath.Glob(filepath.Join(unitDir, "*.tf"))
	if err != nil {
		return "", err
	}
	if len(tf) > 0 {
		return ToolTofu, nil
	}
	return "", fmt.Errorf("%s has no terragrunt.hcl and no .tf files — not a unit", unitDir)
}
