package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/raccoon-core/blastdoor/internal/gitlabapi"
	"github.com/raccoon-core/blastdoor/internal/report"
	"github.com/spf13/cobra"
)

func newGateCmd() *cobra.Command {
	var (
		reportPath  string
		summaryPath string
		ruleName    string
		approverIDs []string
		autoMerge   bool
		squash      bool
		mrIID       int
		branch      string
		apiURL      string
		projectID   string
		token       string
		skipNote    bool
	)

	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Gate a GitLab merge request on the risk score",
		Long: `Reads the report written by 'blastdoor eval' and acts on the merge request.

Above the threshold it creates or updates an approval rule, so a human has to
approve. Below it, it approves the merge request itself and — with
--auto-merge — queues the merge for when the pipeline succeeds.

A 401 or 403 from GitLab fails the command: a token that cannot reach the API
must not be mistaken for a change that needs no gate.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep, err := readReport(reportPath)
			if err != nil {
				return err
			}

			if token == "" {
				return fmt.Errorf("no GitLab token: pass --token or set BLASTDOOR_GITLAB_TOKEN or GITLAB_TOKEN")
			}
			if projectID == "" {
				return fmt.Errorf("no project: pass --project-id or set CI_PROJECT_ID")
			}

			client := gitlabapi.New(apiURL, projectID, token)
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			iid, err := resolveMR(ctx, client, mrIID, branch)
			if err != nil {
				return err
			}
			if iid == 0 {
				fmt.Fprintf(out, "no open merge request for branch %q — nothing to gate\n", branch)
				return nil
			}

			if !skipNote {
				body, err := noteBody(summaryPath, rep)
				if err != nil {
					return err
				}
				if err := client.PostNote(ctx, iid, body); err != nil {
					return fmt.Errorf("posting summary note: %w", err)
				}
				fmt.Fprintf(out, "posted risk summary to !%d\n", iid)
			}

			if rep.Decision == report.DecisionReviewRequired {
				groups, err := parseGroupIDs(approverIDs)
				if err != nil {
					return err
				}
				if len(groups) == 0 {
					fmt.Fprintln(cmd.ErrOrStderr(), "warning: no --approver-group-id given, so the rule accepts any eligible approver")
				}
				if err := client.SetApprovalRule(ctx, iid, ruleName, 1, groups); err != nil {
					return fmt.Errorf("setting approval rule %q: %w", ruleName, err)
				}
				fmt.Fprintf(out, "score %d >= threshold %d — !%d now requires an approval via rule %q\n",
					rep.TotalScore, rep.Threshold, iid, ruleName)
				return nil
			}

			// Nothing was scored, so blastdoor has no opinion to act on.
			// Approving here would vouch for a change it never read — and
			// zero units is also what a misconfigured --root looks like.
			// Any rule left by an earlier, riskier push deliberately stays.
			if rep.UnitCount == 0 {
				fmt.Fprintf(out, "no units were scored — leaving !%d alone\n", iid)
				return nil
			}

			fmt.Fprintf(out, "score %d < threshold %d — approving !%d\n", rep.TotalScore, rep.Threshold, iid)

			// Approve rather than relaxing an existing rule to zero: a rule
			// left over from an earlier, riskier push is then satisfied by a
			// real approval instead of being neutered.
			if err := client.Approve(ctx, iid); err != nil {
				return fmt.Errorf("approving !%d: %w", iid, err)
			}

			if autoMerge {
				if err := client.MergeWhenPipelineSucceeds(ctx, iid, squash); err != nil {
					return fmt.Errorf("queueing merge for !%d: %w", iid, err)
				}
				fmt.Fprintf(out, "!%d will merge once the pipeline succeeds\n", iid)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&reportPath, "report", ".blastdoor/report.json", "report.json written by 'blastdoor eval'")
	cmd.Flags().StringVar(&summaryPath, "summary", ".blastdoor/summary.md", "summary.md to post as a note")
	cmd.Flags().StringVar(&ruleName, "rule-name", "blastdoor", "name of the approval rule to manage")
	cmd.Flags().StringArrayVar(&approverIDs, "approver-group-id", splitList(os.Getenv("BLASTDOOR_APPROVER_GROUP_IDS")), "GitLab group id allowed to approve (repeatable)")
	cmd.Flags().BoolVar(&autoMerge, "auto-merge", false, "queue the merge when the score is below the threshold")
	cmd.Flags().BoolVar(&squash, "squash", true, "squash commits when auto-merging")
	cmd.Flags().IntVar(&mrIID, "mr-iid", envInt("CI_MERGE_REQUEST_IID", 0), "merge request IID (default $CI_MERGE_REQUEST_IID)")
	cmd.Flags().StringVar(&branch, "branch", envOr("CI_COMMIT_BRANCH", ""), "source branch, used to find the MR on a branch pipeline")
	cmd.Flags().StringVar(&apiURL, "api-url", envOr("CI_API_V4_URL", "https://gitlab.com/api/v4"), "GitLab API base URL")
	cmd.Flags().StringVar(&projectID, "project-id", envOr("CI_PROJECT_ID", ""), "GitLab project id")
	cmd.Flags().StringVar(&token, "token", envOr("BLASTDOOR_GITLAB_TOKEN", os.Getenv("GITLAB_TOKEN")), "GitLab token with the api scope")
	cmd.Flags().BoolVar(&skipNote, "skip-note", false, "do not post the summary note")

	return cmd
}

// resolveMR uses the explicit IID when there is one, otherwise looks up the
// open MR for the branch.
func resolveMR(ctx context.Context, client *gitlabapi.Client, iid int, branch string) (int, error) {
	if iid != 0 {
		return iid, nil
	}
	if branch == "" {
		return 0, fmt.Errorf("no merge request: pass --mr-iid or --branch (or set CI_MERGE_REQUEST_IID or CI_COMMIT_BRANCH)")
	}
	found, err := client.FindMergeRequestForBranch(ctx, branch)
	if err != nil {
		return 0, fmt.Errorf("looking up the merge request for %q: %w", branch, err)
	}
	return found, nil
}

func readReport(path string) (report.Report, error) {
	var rep report.Report
	raw, err := os.ReadFile(path)
	if err != nil {
		return rep, fmt.Errorf("reading %s — run 'blastdoor eval' first: %w", path, err)
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		return rep, fmt.Errorf("parsing %s: %w", path, err)
	}
	return rep, nil
}

// noteBody prefers the rendered summary, falling back to re-rendering from
// the report when the summary file is missing.
func noteBody(summaryPath string, rep report.Report) (string, error) {
	if raw, err := os.ReadFile(summaryPath); err == nil {
		return string(raw), nil
	}
	var b strings.Builder
	if err := rep.WriteMarkdown(io.Writer(&b)); err != nil {
		return "", err
	}
	return b.String(), nil
}

func parseGroupIDs(values []string) ([]int, error) {
	var ids []int
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("approver group id %q is not a number: %w", v, err)
		}
		ids = append(ids, n)
	}
	return ids, nil
}

// splitList parses a comma-separated environment variable.
func splitList(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}
