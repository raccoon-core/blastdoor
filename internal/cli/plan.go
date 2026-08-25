package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/raccoon-core/blastdoor/internal/detect"
	"github.com/raccoon-core/blastdoor/internal/runner"
	"github.com/spf13/cobra"
)

func newPlanCmd() *cobra.Command {
	var (
		units     []string
		unitsFile string
		root      string
		baseRef   string
		headRef   string
		outDir    string
		tool      string
		tgTFPath  string
		manager   string
	)

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Run a plan for each unit and save it as JSON",
		Long: `Runs init, plan and show -json for each unit, writing the plan JSON to
<out-dir>/<unit>/plan.json for 'blastdoor eval' to score.

Units come from --unit, --units-file, or are detected from the git diff when
neither is given. Binary versions are resolved by tenv from the version files
in each unit.`,
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

			// Own the output directory outright. A plan.json committed to
			// the repository at this path would otherwise be scored as if
			// this job had produced it — a free way to pad the report with
			// a harmless-looking unit.
			if err := os.RemoveAll(outDir); err != nil {
				return fmt.Errorf("clearing %s: %w", outDir, err)
			}
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				return fmt.Errorf("creating %s: %w", outDir, err)
			}
			if len(resolved) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "no units to plan")
				return nil
			}

			opts := runner.Options{
				Tool:             runner.Tool(tool),
				TerragruntTFPath: tgTFPath,
				Manager:          runner.Manager(manager),
				Log:              cmd.ErrOrStderr(),
			}

			for _, unit := range resolved {
				fmt.Fprintf(cmd.ErrOrStderr(), "\n=== planning %s ===\n", unit)
				planJSON, err := runner.Plan(cmd.Context(), unit, opts)
				if err != nil {
					return err
				}

				dest := filepath.Join(outDir, unit)
				if err := os.MkdirAll(dest, 0o755); err != nil {
					return fmt.Errorf("creating %s: %w", dest, err)
				}
				out := filepath.Join(dest, "plan.json")
				if err := os.WriteFile(out, planJSON, 0o644); err != nil {
					return fmt.Errorf("writing %s: %w", out, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", out)
			}
			return nil
		},
	}

	cmd.Flags().StringArrayVar(&units, "unit", nil, "unit directory to plan (repeatable)")
	cmd.Flags().StringVar(&unitsFile, "units-file", "", "file listing unit directories, one per line")
	cmd.Flags().StringVar(&root, "root", ".", "directory to scan when detecting units")
	cmd.Flags().StringVar(&baseRef, "base-ref", "", "git ref to diff from when detecting units (default: auto)")
	cmd.Flags().StringVar(&headRef, "head-ref", "HEAD", "git ref to diff to when detecting units")
	cmd.Flags().StringVar(&outDir, "out-dir", ".blastdoor", "directory to write plan JSON into")
	cmd.Flags().StringVar(&tool, "tool", "auto", "auto, tofu, terraform or terragrunt")
	cmd.Flags().StringVar(&tgTFPath, "terragrunt-tf-path", "auto", "binary Terragrunt wraps: auto, tofu or terraform")
	cmd.Flags().StringVar(&manager, "manager", "auto", "version manager: auto, tenv, mise or none")

	return cmd
}

// resolveUnits picks units from explicit flags, a file, or the git diff.
func resolveUnits(units []string, unitsFile, root, baseRef, headRef string) ([]string, error) {
	if len(units) > 0 {
		return units, nil
	}

	if unitsFile != "" {
		f, err := os.Open(unitsFile)
		if err != nil {
			return nil, fmt.Errorf("reading units file: %w", err)
		}
		defer f.Close()

		var out []string
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			if line := strings.TrimSpace(scanner.Text()); line != "" {
				out = append(out, line)
			}
		}
		return out, scanner.Err()
	}

	return detect.Changed(detect.Options{Root: root, BaseRef: baseRef, HeadRef: headRef})
}
