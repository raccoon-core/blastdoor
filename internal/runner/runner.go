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
	"iter"
	"os"
	"path/filepath"
	"strings"
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
	if tool, ok := LockedTool(unitDir); ok {
		return tool, nil
	}

	tf, err := filepath.Glob(filepath.Join(unitDir, "*.tf"))
	if err != nil {
		return "", err
	}
	if len(tf) > 0 {
		// Nothing pinned or locked, so OpenTofu.
		return ToolTofu, nil
	}
	return "", fmt.Errorf("%s has no terragrunt.hcl and no .tf files — not a unit", unitDir)
}

// lockFileTofuMarker is the line OpenTofu writes at the top of
// .terraform.lock.hcl ("# This file is maintained automatically by \"tofu
// init\"."); Terraform's says "terraform init" instead.
const lockFileTofuMarker = `"tofu init"`

// LockedTool reports which of OpenTofu/Terraform actually wrote a unit's own
// .terraform.lock.hcl — unlike PinnedTool, unitDir only, no walking upward:
// a lock file is never inherited, it's a record of what really ran here.
// Checked after PinnedTool, not before: an explicit pin is cheaper to read
// and is the repository saying what it wants, not just what happened last.
func LockedTool(unitDir string) (tool Tool, ok bool) {
	raw, err := os.ReadFile(filepath.Join(unitDir, ".terraform.lock.hcl"))
	if err != nil {
		return "", false
	}
	if strings.Contains(string(raw), lockFileTofuMarker) {
		return ToolTofu, true
	}
	return ToolTerraform, true
}

// pinFiles are the version files that name a tool, in the order a single
// directory is searched. A directory holding both is ambiguous, and OpenTofu
// wins there.
var pinFiles = []struct {
	name string
	tool Tool
}{
	{".opentofu-version", ToolTofu},
	{".terraform-version", ToolTerraform},
}

// PinnedTool reports which of OpenTofu/Terraform the repository pins for a
// unit, and the file that says so.
//
// The nearest file wins, which is what searchUpward yields first.
func PinnedTool(unitDir string) (tool Tool, file string, ok bool) {
	for dir := range searchUpward(unitDir) {
		for _, candidate := range pinFiles {
			path := filepath.Join(dir, candidate.name)
			if _, err := os.Stat(path); err == nil {
				return candidate.tool, path, true
			}
		}
	}
	return "", "", false
}

// searchUpward yields the unit's own directory and each parent above it,
// nearest first, stopping once it has yielded a repository root.
//
// Configuration is resolved by walking up because that is how the version
// managers resolve it: a .terraform-version several directories above a
// Terragrunt unit still governs it, and a mise.toml at the repository root
// still makes the unit a mise project. Looking only inside the unit would miss
// both — and for PinnedTool that means quietly planning a Terraform repository
// with OpenTofu. A repository root is where it stops, because that is as far
// as a unit's configuration reaches.
func searchUpward(unitDir string) iter.Seq[string] {
	return func(yield func(string) bool) {
		dir, err := filepath.Abs(unitDir)
		if err != nil {
			return
		}
		for {
			if !yield(dir) {
				return
			}
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				return
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				return
			}
			dir = parent
		}
	}
}
