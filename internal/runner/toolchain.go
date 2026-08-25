package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Manager is the version manager that resolves and installs the binaries a
// unit needs.
type Manager string

const (
	// ManagerAuto picks a manager by looking at the unit and what is
	// installed.
	ManagerAuto Manager = "auto"
	// ManagerTenv uses tenv, which reads .opentofu-version,
	// .terraform-version, .terragrunt-version and terragrunt.hcl
	// constraints, and provides shims on PATH.
	ManagerTenv Manager = "tenv"
	// ManagerMise uses mise, which reads mise.toml or .tool-versions.
	ManagerMise Manager = "mise"
	// ManagerNone runs whatever is already on PATH.
	ManagerNone Manager = "none"
)

// miseConfigNames are the files that make a directory a mise project.
var miseConfigNames = []string{
	"mise.toml",
	".mise.toml",
	"mise.local.toml",
	".mise/config.toml",
	".tool-versions",
}

// DetectManager picks a version manager for a unit.
//
// mise wins when the unit (or an ancestor) is a mise project and mise is
// installed, since that configuration is the more specific statement of
// intent. Otherwise tenv, which the image ships. Failing both, whatever is on
// PATH already.
func DetectManager(unitDir string) Manager {
	if hasMiseConfig(unitDir) && onPath("mise") {
		return ManagerMise
	}
	if onPath("tenv") {
		return ManagerTenv
	}
	return ManagerNone
}

// hasMiseConfig walks up from the unit looking for a mise project file,
// stopping at a repository root.
func hasMiseConfig(unitDir string) bool {
	dir, err := filepath.Abs(unitDir)
	if err != nil {
		return false
	}

	for {
		for _, name := range miseConfigNames {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return true
			}
		}
		// A repository root is as far as a unit's configuration reaches.
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return false
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func onPath(binary string) bool {
	_, err := exec.LookPath(binary)
	return err == nil
}

// Prepare installs the binaries a unit needs, ahead of planning it.
//
// Doing this as its own step keeps a toolchain that will not install from
// looking like a plan that will not run, and gives the download somewhere of
// its own to fail.
func Prepare(ctx context.Context, unitDir string, opts Options) error {
	manager := opts.Manager
	if manager == "" || manager == ManagerAuto {
		manager = DetectManager(unitDir)
	}

	log := opts.Log
	if log == nil {
		log = os.Stderr
	}

	switch manager {
	case ManagerMise:
		return prepareMise(ctx, unitDir, log)
	case ManagerTenv:
		return prepareTenv(ctx, unitDir, opts, log)
	default:
		fmt.Fprintf(log, "no version manager for %s — using whatever is on PATH\n", unitDir)
		return nil
	}
}

// prepareMise installs everything the unit's mise config asks for.
func prepareMise(ctx context.Context, unitDir string, log io.Writer) error {
	cmd := exec.CommandContext(ctx, "mise", "install", "--yes")
	cmd.Dir = unitDir
	cmd.Env = miseEnv()
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mise install in %s: %w", unitDir, err)
	}
	return nil
}

// prepareTenv installs the binaries this unit will actually be planned with.
// tenv resolves the version from the files in the unit itself, so it runs
// there rather than at the repository root.
func prepareTenv(ctx context.Context, unitDir string, opts Options, log io.Writer) error {
	tool := opts.Tool
	if tool == "" || tool == ToolAuto {
		var err error
		if tool, err = Detect(unitDir); err != nil {
			return err
		}
	}

	wanted := []Tool{tool}
	if tool == ToolTerragrunt {
		// Terragrunt is only half of it; the binary it wraps has to exist
		// too, and its version comes from a different file.
		wrapped := Tool(opts.TerragruntTFPath)
		if wrapped == "" {
			wrapped = ToolTofu
		}
		wanted = append(wanted, wrapped)
	}

	for _, t := range wanted {
		cmd := exec.CommandContext(ctx, "tenv", string(t), "install")
		cmd.Dir = unitDir
		cmd.Env = os.Environ()
		cmd.Stdout = log
		cmd.Stderr = log
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("tenv %s install in %s: %w", t, unitDir, err)
		}
	}
	return nil
}

// miseEnv runs mise against configuration blastdoor did not write.
//
// MISE_SAFE stops a repository's own mise.toml executing code during version
// resolution — no hooks, no tasks, no `exec()` in templates, no `[env]`
// injection. Blastdoor judges merge requests it does not trust, so this is
// not optional.
func miseEnv() []string {
	return append(os.Environ(),
		"MISE_SAFE=1",
		"MISE_YES=1",
	)
}

// toolCommand builds the command that runs a tool for a unit, routed through
// the version manager when there is one.
func toolCommand(ctx context.Context, manager Manager, unitDir, binary string, extraEnv []string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd

	if manager == ManagerMise {
		// mise has no shims on PATH in a plain CI shell, so go through
		// exec, which also picks up the unit's own configuration.
		cmd = exec.CommandContext(ctx, "mise", append([]string{"exec", "--", binary}, args...)...)
		cmd.Env = append(miseEnv(), extraEnv...)
	} else {
		// tenv installs shims, so the bare name resolves to the right
		// version already.
		cmd = exec.CommandContext(ctx, binary, args...)
		cmd.Env = append(os.Environ(), extraEnv...)
	}

	cmd.Dir = unitDir
	return cmd
}
