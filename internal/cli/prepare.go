package cli

import (
	"fmt"

	"github.com/raccoon-core/blastdoor/internal/runner"
	"github.com/spf13/cobra"
)

func newPrepareCmd() *cobra.Command {
	var (
		units     []string
		unitsFile string
		root      string
		baseRef   string
		headRef   string
		manager   string
		tool      string
		tgTFPath  string
	)

	cmd := &cobra.Command{
		Use:   "prepare",
		Short: "Install the tool versions each unit needs",
		Long: `Installs the OpenTofu, Terraform or Terragrunt versions each unit asks for,
before anything is planned.

Run it in the same job as 'blastdoor plan'. Doing it as its own step keeps a
toolchain that will not install from looking like a plan that will not run, and
gives the download somewhere of its own to fail and be timed.

  tenv  reads .opentofu-version, .terraform-version, .terragrunt-version and
        terragrunt.hcl constraints
  mise  reads mise.toml or .tool-versions, and runs with MISE_SAFE=1 so a
        repository's own config cannot execute code during resolution

The default --manager auto picks mise when the unit is a mise project and mise
is installed, otherwise tenv.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root = pickString(cmd, "root", root, cfg().Root)
			tool = pickString(cmd, "tool", tool, cfg().Tool)
			manager = pickString(cmd, "manager", manager, cfg().Manager)
			tgTFPath = pickString(cmd, "terragrunt-tf-path", tgTFPath, cfg().TerragruntTFPath)

			resolved, err := resolveUnits(units, unitsFile, root, baseRef, headRef)
			if err != nil {
				return err
			}
			if len(resolved) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "no units to prepare")
				return nil
			}

			opts := runner.Options{
				Tool:             runner.Tool(tool),
				TerragruntTFPath: tgTFPath,
				Manager:          runner.Manager(manager),
				Log:              cmd.ErrOrStderr(),
			}

			for _, unit := range resolved {
				chosen := opts.Manager
				if chosen == "" || chosen == runner.ManagerAuto {
					chosen = runner.DetectManager(unit)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "\n=== preparing %s (%s) ===\n", unit, chosen)

				if err := runner.Prepare(cmd.Context(), unit, opts); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), unit)
			}
			return nil
		},
	}

	cmd.Flags().StringArrayVar(&units, "unit", nil, "unit directory to prepare (repeatable)")
	cmd.Flags().StringVar(&unitsFile, "units-file", "", "file listing unit directories, one per line")
	cmd.Flags().StringVar(&root, "root", ".", "directory to scan when detecting units")
	cmd.Flags().StringVar(&baseRef, "base-ref", "", "git ref to diff from when detecting units (default: auto)")
	cmd.Flags().StringVar(&headRef, "head-ref", "HEAD", "git ref to diff to when detecting units")
	cmd.Flags().StringVar(&manager, "manager", "auto", "auto, tenv, mise or none")
	cmd.Flags().StringVar(&tool, "tool", "auto", "auto, tofu, terraform or terragrunt")
	cmd.Flags().StringVar(&tgTFPath, "terragrunt-tf-path", "auto", "binary Terragrunt wraps: auto, tofu or terraform")

	return cmd
}
