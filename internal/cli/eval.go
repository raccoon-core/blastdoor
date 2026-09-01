package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/raccoon-core/blastdoor/internal/detect"
	"github.com/raccoon-core/blastdoor/internal/policy"
	"github.com/raccoon-core/blastdoor/internal/report"
	"github.com/spf13/cobra"
)

func newEvalCmd() *cobra.Command {
	var (
		planFiles   []string
		planDir     string
		policyPaths []string
		outDir      string
		failOnBlock bool
		guardPaths  []string
		baseRef     string
		headRef     string

		requireCoverage bool
		ignorePaths     []string
		root            string

		wishFlag            string
		applyInclude        string
		applyIncludeProject string
		applyIncludeRef     string
	)

	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Judge plan JSON against policies",
		Long: `Judges every change in a plan against Rego policies and writes report.json,
summary.md and blastdoor.env into --out-dir.

Each change comes back pass, review or deny, and the worst one decides the
plan. A change no policy matches is denied.

--policy is repeatable, and each one is searched all the way down for .rego
files, so shared policies and a repository's own can be passed together:

  blastdoor eval --plan-dir .blastdoor --policy common --policy policy

Only .rego files are read. Fixtures and test plans can sit in the same tree
without becoming part of the evaluation. A --policy path holding no .rego at
all is an error, not an empty rule set.

--require-coverage judges what the plans do not cover. A file that selects no
unit — a topics.yaml a unit reads, a .terragrunt-version deciding the binary
that applies it — is planned by nothing and so scored by nothing. Rather than
letting it through unseen, it forces a review:

  blastdoor eval --plan-dir .blastdoor --policy policy \
    --require-coverage --ignore-path docs --ignore-path README.md

Name the paths allowed to go unplanned with --ignore-path. Guarded paths are
already exempt: they force review on their own.

Point --plan at a single file while writing policies:

  blastdoor eval --plan examples/plans/kafka-topic-create.json --policy examples/policies

or --plan-dir at the tree 'blastdoor plan' produced, to judge a whole change.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root = pickString(cmd, "root", root, cfg().Root)

			ignorePaths = pickList(cmd, "ignore-path", ignorePaths, cfg().Ignore)
			requireCoverage = pickBool(cmd, "require-coverage", requireCoverage, cfg().RequireCoverage)
			guardPaths, guardsStated := guardPathsFor(cmd, guardPaths)

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

			// --policy replaces the layer set with one unnamed layer, under
			// the same rule as every other flag: the flag if it was given,
			// otherwise the config.
			var layers []policy.Layer
			var provenance []report.Layer
			if !cmd.Flags().Changed("policy") && len(cfg().Policies) > 0 {
				var err error
				layers, provenance, err = resolveLayers(cmd.Context(), cfg().Policies, os.Stderr)
				if err != nil {
					return err
				}
			}

			// A --policy run has no layer to name, so the paths stand in:
			// the note should always be able to say what judged the change.
			if len(provenance) == 0 {
				for _, path := range policyPaths {
					// Named by its path, and marked local: it is a directory
					// in the working tree, not a repository root.
					provenance = append(provenance, report.Layer{
						Name:       path,
						Repository: ".",
						Directory:  path,
					})
				}
			}

			evaluator, err := policy.New(cmd.Context(), policy.Options{
				Layers:      layers,
				PolicyPaths: policyPaths,
				Vars:        cfg().Vars,
			})
			if err != nil {
				return err
			}

			units, err := judgePlans(cmd.Context(), evaluator, plans)
			if err != nil {
				return err
			}

			rep := report.Build(units)
			rep.Engines = enginesFor(plans)
			rep.Layers = provenance

			if err := applyGuards(cmd, &rep, guardPaths, guardsStated, baseRef, headRef); err != nil {
				return err
			}

			// A file no unit selects is planned by nothing and judged by
			// nothing. Guarded and ignored paths are already accounted for,
			// so they are not reported twice.
			if requireCoverage {
				missing, err := uncoveredFiles(root, baseRef, headRef, append(ignorePaths, guardPaths...))
				if err != nil {
					return err
				}
				rep.RequireCoverage(missing)
			}

			wish, err := report.ParseWish(wishFlag)
			if err != nil {
				return err
			}
			// After the guards, deliberately. Both RequireReview and
			// RequireCoverage force review across the whole repository, and an
			// environment cannot apply unattended while the change is
			// rewriting the rules that judge it — but Decide can only see that
			// once they have recorded it.
			if err := rep.Decide(wish); err != nil {
				return err
			}

			include := report.ApplyInclude{File: applyInclude, Project: applyIncludeProject, Ref: applyIncludeRef}
			if err := writeReport(rep, outDir, include); err != nil {
				return err
			}
			if err := rep.WriteMarkdown(cmd.OutOrStdout()); err != nil {
				return err
			}

			if failOnBlock && rep.Verdict != policy.Pass {
				if len(rep.Guarded) > 0 {
					return fmt.Errorf("%s: this change edits guarded paths (%s)", rep.Verdict, strings.Join(rep.Guarded, ", "))
				}
				if len(rep.Uncovered) > 0 {
					return fmt.Errorf("%s: this change edits files no plan covers (%s)", rep.Verdict, strings.Join(rep.Uncovered, ", "))
				}
				return fmt.Errorf("verdict is %s", rep.Verdict)
			}
			return nil
		},
	}

	cmd.Flags().StringArrayVar(&planFiles, "plan", nil, "plan JSON file to score (repeatable)")
	cmd.Flags().StringVar(&planDir, "plan-dir", "", "directory tree of plan.json files, as written by 'blastdoor plan'")
	cmd.Flags().StringArrayVar(&policyPaths, "policy", nil, "directory to search for .rego policies, or a single .rego file (repeatable)")
	cmd.Flags().StringVar(&outDir, "out-dir", ".blastdoor", "directory to write report.json, summary.md and blastdoor.env into")
	cmd.Flags().BoolVar(&failOnBlock, "fail-on-block", false, "exit non-zero unless every change passes")
	cmd.Flags().StringArrayVar(&guardPaths, "guard-path", nil, "path whose modification forces review whatever the score (repeatable)")
	cmd.Flags().StringVar(&baseRef, "base-ref", "", "git ref to diff from for --guard-path (default: auto)")
	cmd.Flags().StringVar(&headRef, "head-ref", "HEAD", "git ref to diff to for --guard-path")
	cmd.Flags().BoolVar(&requireCoverage, "require-coverage", false, "force review when the change edits files no unit selects")
	cmd.Flags().StringArrayVar(&ignorePaths, "ignore-path", nil, "path --require-coverage may leave unplanned (repeatable)")
	cmd.Flags().StringVar(&root, "root", ".", "directory to scan for units when checking coverage")

	// Deliberately not readable from .blastdoor.yml, and this needs no code:
	// the config decoder runs with KnownFields(true), so an `environments:` key
	// rejects the whole file. A branch declaring prd=auto would be a branch
	// arranging its own unattended production apply — the same reasoning that
	// keeps the approver group ids out of the branch's hands in gate.go.
	cmd.Flags().StringVar(&wishFlag, "deployment-method-wish",
		envOr("BLASTDOOR_DEPLOYMENT_METHOD_WISH", ""),
		"per-environment ceiling, e.g. int=auto,stg=auto,prd=manual (env: BLASTDOOR_DEPLOYMENT_METHOD_WISH)")
	cmd.Flags().StringVar(&applyInclude, "apply-include", ".gitlab/blastdoor-apply.yml",
		"file the generated apply pipeline includes for the .blastdoor:apply job")
	cmd.Flags().StringVar(&applyIncludeProject, "apply-include-project", "",
		"project the .blastdoor:apply file lives in, if not this repository (switches --apply-include to a project: include)")
	cmd.Flags().StringVar(&applyIncludeRef, "apply-include-ref", "",
		"ref to use with --apply-include-project")

	return cmd
}

type planInput struct {
	name string // unit path, used as the label in the report
	file string
}

// judgePlans reads each plan and judges it, one report unit per plan.
//
// Any plan that cannot be read, parsed or validated fails the whole command
// rather than being skipped: a document blastdoor could not read must never
// come out looking like one it read and was happy with.
func judgePlans(ctx context.Context, evaluator *policy.Evaluator, plans []planInput) ([]report.Unit, error) {
	units := make([]report.Unit, 0, len(plans))
	for _, p := range plans {
		raw, err := os.ReadFile(p.file)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", p.file, err)
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("parsing %s as plan JSON: %w", p.file, err)
		}
		if err := policy.ValidatePlan(decoded); err != nil {
			return nil, fmt.Errorf("%s: %w", p.file, err)
		}

		res, err := evaluator.Evaluate(ctx, decoded)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.file, err)
		}
		units = append(units, report.Unit{
			Path:        p.name,
			Environment: environmentFor(p.file),
			Changes:     res.Changes,
		})
	}
	return units, nil
}

// applyGuards forces review when the change edits the rules that judge it.
//
// A change that edits its own policies or pipeline cannot be judged by them,
// so it goes to a person whatever it scored.
//
// stated says whether anyone actually asked for guards, and it decides what
// happens when the diff cannot be read. If they did, failing to check is an
// error: the caller asked for a guarantee blastdoor cannot give, and a verdict
// that quietly skipped the check is worse than no verdict. If they did not,
// the only guard is the config guarding itself and there is no diff to check
// it against — no merge request, so nothing to gate and nothing to guard.
// That is someone trying a policy against a saved plan, so say so and carry on.
func applyGuards(cmd *cobra.Command, rep *report.Report, guardPaths []string, stated bool, baseRef, headRef string) error {
	if len(guardPaths) == 0 {
		return nil
	}

	tripped, err := trippedGuards(guardPaths, baseRef, headRef)
	switch {
	case err == nil:
		rep.RequireReview(tripped)
	case stated:
		return err
	default:
		fmt.Fprintf(cmd.ErrOrStderr(),
			"no diff to check %s against, so it is not guarded here\n", cfg().Path)
	}
	return nil
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

// writeReport writes the files the CI jobs consume.
//
// apply.gitlab-ci.yml is written whenever Decide found at least one
// environment to decide about — len(rep.Environments) > 0 — which no longer
// requires a wish: Decide folds in whatever environments the units
// themselves recorded, and policy's own allow rules can authorise auto on
// their own. Every environment Decide considered gets an EnvDecision,
// including ones that resolved to none, so this is exactly "there is
// something to decide about." That case (the ordinary one for a docs-only or
// CI-only change, or a repository that has not arranged per-environment
// planning at all) does not need special handling here: WriteApplyYAML
// itself always produces a file GitLab can build a child pipeline from,
// writing a single placeholder job when nothing in the change has anything to
// apply, rather than the jobless include-only file that GitLab refuses to
// run. See its doc comment.
func writeReport(rep report.Report, outDir string, applyInclude report.ApplyInclude) error {
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

	if len(rep.Environments) > 0 {
		files = append(files, struct {
			name  string
			write func(io.Writer) error
		}{"apply.gitlab-ci.yml", func(w io.Writer) error {
			return rep.WriteApplyYAML(w, applyInclude)
		}})
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

// trippedGuards returns the changed files that fall under a guarded path.
func trippedGuards(guardPaths []string, baseRef, headRef string) ([]string, error) {
	changed, err := detect.ChangedFiles(detect.Options{BaseRef: baseRef, HeadRef: headRef})
	if err != nil {
		return nil, fmt.Errorf("--guard-path needs a diff: %w", err)
	}

	var tripped []string
	for _, file := range changed {
		if matchesPath(file, guardPaths) {
			tripped = append(tripped, filepath.ToSlash(filepath.Clean(file)))
		}
	}
	return tripped, nil
}

// enginesFor reads back what produced each plan, as 'blastdoor plan' recorded
// it beside the plan itself.
//
// Missing files are not an error. Plans passed straight to --plan have no
// engine recorded, and neither do plans from a blastdoor old enough not to
// have written one; the report says nothing rather than guessing. The engine
// cannot be read from the plan JSON, which carries a terraform_version key
// whichever of the two wrote it.
func enginesFor(plans []planInput) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range plans {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(p.file), "engine.txt"))
		if err != nil {
			continue
		}
		engine := strings.TrimSpace(string(raw))
		if engine == "" || seen[engine] {
			continue
		}
		seen[engine] = true
		out = append(out, engine)
	}
	return out
}

// environmentFor reads back the environment 'blastdoor plan --environment'
// recorded beside a plan.
//
// A missing file is silence, not an error: plans passed straight to --plan have
// no environment recorded, and neither do plans from a blastdoor old enough not
// to have written one. Report.Decide reports it, and only when a deployment
// method wish makes it matter.
func environmentFor(planFile string) string {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(planFile), "environment.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// uncoveredFiles lists the changed files that select no unit, less the ones
// the caller has accounted for.
//
// Guarded paths are passed in as exempt: they already force review, and
// naming the same file twice in one summary tells a reader nothing extra.
func uncoveredFiles(root, baseRef, headRef string, exempt []string) ([]string, error) {
	missing, err := detect.Uncovered(detect.Options{Root: root, BaseRef: baseRef, HeadRef: headRef})
	if err != nil {
		return nil, fmt.Errorf("--require-coverage needs a diff: %w", err)
	}

	var out []string
	for _, file := range missing {
		if matchesPath(file, exempt) {
			continue
		}
		out = append(out, file)
	}
	return out, nil
}

// matchesPath reports whether a changed file is one of the given paths, sits
// underneath one, or matches one as a pattern.
//
// Three forms, in the order they are tried:
//
//	policy            the path itself and everything under it
//	*.md              a glob, matched against the whole path
//	**/README.md      the same name in any directory
//
// A plain path stays a prefix, because that is what a guard needs: naming a
// directory has to cover what is inside it. The pattern forms exist for the
// ignore list, where the interesting sets are shaped like "every README in the
// repository" — a repository with one README per component cannot list them
// one by one and keep the list honest.
//
// It serves both lists — guards and coverage exemptions — which is why it is
// named for the matching rather than for either caller.
func matchesPath(file string, patterns []string) bool {
	f := filepath.ToSlash(filepath.Clean(file))
	for _, pattern := range patterns {
		g := filepath.ToSlash(filepath.Clean(pattern))

		if rest, ok := strings.CutPrefix(g, "**/"); ok {
			if f == rest || strings.HasSuffix(f, "/"+rest) {
				return true
			}
			// A ** pattern can still hold a glob: **/*.md.
			if ok, _ := path.Match(rest, path.Base(f)); ok {
				return true
			}
			continue
		}

		if strings.ContainsAny(g, "*?[") {
			if ok, _ := path.Match(g, f); ok {
				return true
			}
			continue
		}

		// The trailing slash keeps "policy" from matching "policyholder".
		if f == g || strings.HasPrefix(f, g+"/") {
			return true
		}
	}
	return false
}
