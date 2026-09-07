package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/raccoon-core/blastdoor/internal/baseline"
	"github.com/raccoon-core/blastdoor/internal/detect"
	"github.com/raccoon-core/blastdoor/internal/policy"
	"github.com/raccoon-core/blastdoor/internal/runner"
	"github.com/spf13/cobra"
)

func newPlanCmd() *cobra.Command {
	var (
		units       []string
		unitsFile   string
		root        string
		baseRef     string
		headRef     string
		outDir      string
		tool        string
		tgTFPath    string
		manager     string
		environment string
		baselineRef string
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

			// The tip of the branch this change targets, not the merge base —
			// and this is the one place that disagrees with
			// detect.ResolveBaseRef on purpose. Three-dot merge-base is right
			// for "which units does this branch touch". It is wrong here: a
			// branch that is behind its target has a merge base predating the
			// backlog, so the baseline would come back clean while the backlog
			// is still waiting. The question here is "what is pending if this
			// change does not exist", and the tip is what answers it.
			var tree *baseline.Worktree
			if baselineRef != "" {
				var err error
				if tree, err = baseline.NewWorktree(cmd.Context(), "", baselineRef); err != nil {
					return err
				}
				defer func() {
					if err := tree.Close(); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "%v\n", err)
					}
				}()
			}

			for _, unit := range resolved {
				fmt.Fprintf(cmd.ErrOrStderr(), "\n=== planning %s ===\n", unit)
				res, err := runner.Plan(cmd.Context(), unit, opts)
				if err != nil {
					return err
				}

				dest := filepath.Join(outDir, unit)
				if err := os.MkdirAll(dest, 0o755); err != nil {
					return fmt.Errorf("creating %s: %w", dest, err)
				}
				out := filepath.Join(dest, "plan.json")
				if err := os.WriteFile(out, res.JSON, 0o644); err != nil {
					return fmt.Errorf("writing %s: %w", out, err)
				}
				// Recorded per unit rather than once for the run: eval reads
				// this from a different job, and when the plans are split
				// across parallel jobs their artifacts are merged. One file
				// per unit merges; one file per run collides.
				if res.Engine != "" {
					engineFile := filepath.Join(dest, "engine.txt")
					if err := os.WriteFile(engineFile, []byte(res.Engine+"\n"), 0o644); err != nil {
						return fmt.Errorf("writing %s: %w", engineFile, err)
					}
				}
				if err := writeEnvironmentFile(dest, environment); err != nil {
					return err
				}
				if tree != nil {
					planned, err := recordBaseline(cmd.Context(), tree, unit, res.JSON, dest,
						func(ctx context.Context, dir string) ([]byte, error) {
							r, err := runner.Plan(ctx, dir, opts)
							return r.JSON, err
						})
					if err != nil {
						return err
					}
					if planned {
						fmt.Fprintf(cmd.ErrOrStderr(), "=== planned %s at %s ===\n", unit, tree.Ref())
					}
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
	cmd.Flags().StringVar(&environment, "environment", "", "environment these units belong to, recorded beside each plan for 'blastdoor eval' to fold into a deployment method")
	cmd.Flags().StringVar(&baselineRef, "baseline-ref", "",
		"git ref to plan each changed unit against as well, to detect changes already waiting to be applied there (default: off)")

	return cmd
}

// baselineTree is the part of a baseline worktree this file needs.
//
// An interface so the decision below can be tested without a git checkout and
// without shelling out to terraform. baseline.Worktree satisfies it.
type baselineTree interface {
	Ref() string
	Commit() string
	UnitDir(unit string) (string, bool)
}

// planner produces plan JSON for a directory.
type planner func(ctx context.Context, dir string) ([]byte, error)

// recordBaseline plans one unit at the baseline and writes what it found
// beside the unit's own plan. It reports whether it planned anything.
//
// The skip is the load-bearing part. A unit whose own plan applies nothing has
// nothing for a backlog to ride along with, so it needs no baseline — and that
// is also what lets a merge request which clears the backlog through, without
// an override anybody has to be trusted with.
func recordBaseline(ctx context.Context, tree baselineTree, unit string, headJSON []byte, dest string, plan planner) (bool, error) {
	head, err := policy.ApplicableAddresses(headJSON)
	if err != nil {
		return false, fmt.Errorf("%s: %w", unit, err)
	}
	if len(head) == 0 {
		return false, nil
	}

	unitDir, ok := tree.UnitDir(unit)
	if !ok {
		// A unit this change creates. Nothing can be pending on it.
		return false, baseline.Write(dest, baseline.Result{
			Ref: tree.Ref(), Commit: tree.Commit(), State: baseline.Absent,
		})
	}

	raw, err := plan(ctx, unitDir)
	if err != nil {
		return true, fmt.Errorf("planning %s at %s: %w", unit, tree.Ref(), err)
	}
	addresses, err := policy.ApplicableAddresses(raw)
	if err != nil {
		return true, fmt.Errorf("%s at %s: %w", unit, tree.Ref(), err)
	}

	state := baseline.Clean
	if len(addresses) > 0 {
		state = baseline.Dirty
	}
	return true, baseline.Write(dest, baseline.Result{
		Ref: tree.Ref(), Commit: tree.Commit(), State: state, Addresses: addresses,
	})
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

// writeEnvironmentFile records which environment a unit belongs to, beside its
// plan.
//
// Per unit rather than once per run, for the same reason engine.txt is: eval
// runs in another job, and when plans are split across a parallel matrix their
// artifacts are merged. One file per unit merges; one file per run collides,
// and the survivor is whichever leg finished last.
//
// An empty name writes nothing. Without a deployment method wish the whole
// feature is off, and a file saying nothing is worse than no file.
func writeEnvironmentFile(dest, environment string) error {
	if environment == "" {
		return nil
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dest, err)
	}
	path := filepath.Join(dest, "environment.txt")
	if err := os.WriteFile(path, []byte(environment+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
