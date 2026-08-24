package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/raccoon-core/blastdoor/internal/policy"
	"github.com/raccoon-core/blastdoor/internal/report"
	"github.com/spf13/cobra"
)

func newEvalCmd() *cobra.Command {
	var (
		planFiles    []string
		planDir      string
		policyPaths  []string
		query        string
		noBasePolicy bool
		threshold    int
		outDir       string
		failOnReview bool
	)

	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Score plan JSON against policies",
		Long: `Evaluates plan JSON against Rego policies and writes report.json, summary.md
and blastdoor.env into --out-dir.

Point --plan at a single file while writing policies:

  blastdoor eval --plan examples/plans/kafka-topic-create.json --policy examples/policies

or --plan-dir at the tree 'blastdoor plan' produced, to score a whole change.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			plans, err := collectPlans(planFiles, planDir)
			if err != nil {
				return err
			}
			// An empty --plan-dir means the change touched no units, which
			// is a pass with nothing to say. Finding nothing when neither
			// flag was given is a mistake worth reporting.
			if len(plans) == 0 && planDir == "" {
				return fmt.Errorf("no plan JSON to score: pass --plan or --plan-dir")
			}

			evaluator, err := policy.New(cmd.Context(), policy.Options{
				PolicyPaths:  policyPaths,
				Query:        query,
				NoBasePolicy: noBasePolicy,
			})
			if err != nil {
				return err
			}

			units := make([]report.Unit, 0, len(plans))
			for _, p := range plans {
				raw, err := os.ReadFile(p.file)
				if err != nil {
					return fmt.Errorf("reading %s: %w", p.file, err)
				}
				var decoded any
				if err := json.Unmarshal(raw, &decoded); err != nil {
					return fmt.Errorf("parsing %s as plan JSON: %w", p.file, err)
				}
				// Refuse to score something that is not a plan: an
				// unreadable document must not come out looking safe.
				if err := policy.ValidatePlan(decoded); err != nil {
					return fmt.Errorf("%s: %w", p.file, err)
				}

				findings, err := evaluator.Evaluate(cmd.Context(), decoded)
				if err != nil {
					return fmt.Errorf("%s: %w", p.file, err)
				}
				units = append(units, report.Unit{Path: p.name, Findings: findings})
			}

			rep := report.Build(units, threshold)

			if err := writeReport(rep, outDir); err != nil {
				return err
			}
			if err := rep.WriteMarkdown(cmd.OutOrStdout()); err != nil {
				return err
			}

			if failOnReview && rep.Decision == report.DecisionReviewRequired {
				return fmt.Errorf("total risk score %d is at or above the threshold of %d", rep.TotalScore, rep.Threshold)
			}
			return nil
		},
	}

	cmd.Flags().StringArrayVar(&planFiles, "plan", nil, "plan JSON file to score (repeatable)")
	cmd.Flags().StringVar(&planDir, "plan-dir", "", "directory tree of plan.json files, as written by 'blastdoor plan'")
	cmd.Flags().StringArrayVar(&policyPaths, "policy", nil, "policy directory or .rego file (repeatable)")
	cmd.Flags().StringVar(&query, "query", policy.DefaultQuery, "rule to evaluate")
	cmd.Flags().BoolVar(&noBasePolicy, "no-base-policy", false, "drop the built-in default-deny backstop")
	cmd.Flags().IntVar(&threshold, "threshold", envInt("BLASTDOOR_THRESHOLD", 50), "score at or above which review is required")
	cmd.Flags().StringVar(&outDir, "out-dir", ".blastdoor", "directory to write report.json, summary.md and blastdoor.env into")
	cmd.Flags().BoolVar(&failOnReview, "fail-on-review", false, "exit non-zero when review is required")

	return cmd
}

type planInput struct {
	name string // unit path, used as the label in the report
	file string
}

// collectPlans gathers plan files from explicit --plan flags and/or a tree of
// plan.json files under --plan-dir.
func collectPlans(planFiles []string, planDir string) ([]planInput, error) {
	var out []planInput
	for _, f := range planFiles {
		out = append(out, planInput{name: f, file: f})
	}

	if planDir != "" {
		// The plan step writes nothing when no unit changed, so a missing
		// directory means "nothing to score", not a broken invocation.
		if _, err := os.Stat(planDir); errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}

		err := filepath.WalkDir(planDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || d.Name() != "plan.json" {
				return nil
			}
			unit, relErr := filepath.Rel(planDir, filepath.Dir(p))
			if relErr != nil {
				return relErr
			}
			out = append(out, planInput{name: filepath.ToSlash(unit), file: p})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scanning %s: %w", planDir, err)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// writeReport writes the three output files the CI jobs consume.
func writeReport(rep report.Report, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", outDir, err)
	}

	files := []struct {
		name  string
		write func(io.Writer) error
	}{
		{"report.json", rep.WriteJSON},
		{"summary.md", rep.WriteMarkdown},
		{"blastdoor.env", rep.WriteEnv},
	}

	for _, f := range files {
		path := filepath.Join(outDir, f.name)
		file, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("creating %s: %w", path, err)
		}
		if err := f.write(file); err != nil {
			file.Close()
			return fmt.Errorf("writing %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("closing %s: %w", path, err)
		}
	}
	return nil
}
