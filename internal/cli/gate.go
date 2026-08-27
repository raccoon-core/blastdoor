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
	"github.com/raccoon-core/blastdoor/internal/policy"
	"github.com/raccoon-core/blastdoor/internal/report"
	"github.com/spf13/cobra"
)

func newGateCmd() *cobra.Command {
	var (
		reportPath   string
		summaryPath  string
		ruleName     string
		approverIDs  []string
		approvalRule bool
		reviewers    bool
		autoMerge    bool
		squash       bool
		mrIID        int
		branch       string
		apiURL       string
		projectID    string
		token        string
		skipNote     bool
	)

	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Gate a GitLab merge request on the verdict",
		Long: `Reads the report written by 'blastdoor eval' and acts on the merge request.

  pass    approves it, and with --auto-merge queues the merge
  review  withdraws blastdoor's own earlier approval
  deny    the same, and fails the job, so the pipeline is red too — a denial is
          settled by changing the plan or the policy, not by approving it

On review and deny, --approval-rule additionally puts an approval rule on the
merge request, so a person has to approve before it can merge, and --reviewers
puts the approver groups' members on it as reviewers. Both write to the merge
request, so both are off until asked for.

A 401 or 403 from GitLab fails the command: a token that cannot reach the API
must not be mistaken for a change that needs no gate.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ruleName = pickString(cmd, "rule-name", ruleName, cfg().RuleName)
			approvalRule = pickBool(cmd, "approval-rule", approvalRule, cfg().ApprovalRule)
			reviewers = pickBool(cmd, "reviewers", reviewers, cfg().Reviewers)
			autoMerge = pickBool(cmd, "auto-merge", autoMerge, cfg().AutoMerge)
			squash = pickBool(cmd, "squash", squash, cfg().Squash)
			// The approver list defaults from a CI variable rather than from
			// the flag's own default, so an unset flag is not the whole test:
			// the pipeline's statement has to outrank the repository's, since
			// a branch naming its own approver group is a branch approving
			// itself.
			if !cmd.Flags().Changed("approver-group-id") &&
				os.Getenv("BLASTDOOR_APPROVER_GROUP_IDS") == "" &&
				cfg().ApproverGroupIDs != nil {
				approverIDs = groupIDStrings(cfg().ApproverGroupIDs)
			}

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

			if rep.Verdict != policy.Pass {
				groups, err := parseGroupIDs(approverIDs)
				if err != nil {
					return err
				}
				// Withdraw any approval blastdoor gave when this merge
				// request still passed. Without this, a push that makes the
				// change worse is waved through by the approval the previous,
				// safer push earned. Independent of --approval-rule: it
				// undoes something blastdoor itself did.
				if err := client.Unapprove(ctx, iid); err != nil {
					return fmt.Errorf("withdrawing the earlier approval on !%d: %w", iid, err)
				}

				if approvalRule {
					if len(groups) == 0 {
						fmt.Fprintln(cmd.ErrOrStderr(), "warning: no --approver-group-id given, so the rule accepts any eligible approver")
					}
					if err := client.SetApprovalRule(ctx, iid, ruleName, 1, groups); err != nil {
						return fmt.Errorf("setting approval rule %q: %w", ruleName, err)
					}
				}

				// After the rule, so a merge request is already gated even if
				// naming reviewers then fails.
				if reviewers {
					if err := addGroupReviewers(ctx, client, cmd, iid, groups); err != nil {
						return err
					}
				}

				if rep.Verdict == policy.Deny {
					// A denial is not something an approval settles. Fail the
					// job so the pipeline is red too, and the change has to
					// alter either the plan or the policy.
					fmt.Fprintf(out, "denied: %d change(s) no policy allows — !%d blocked\n", rep.Counts[policy.Deny], iid)
					return fmt.Errorf("denied by policy: %d change(s) not allowed", rep.Counts[policy.Deny])
				}

				if approvalRule {
					fmt.Fprintf(out, "review required: %d change(s) — !%d now needs an approval via rule %q\n",
						rep.Counts[policy.Review], iid, ruleName)
					return nil
				}
				// Say what was not done. Without the rule the summary is the
				// whole gate — nothing stops the merge request going in
				// unapproved — and a reader should not have to infer that
				// from silence.
				fmt.Fprintf(out, "review required: %d change(s) on !%d — no approval rule set (--approval-rule is off)\n",
					rep.Counts[policy.Review], iid)
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

			fmt.Fprintf(out, "every change passes — approving !%d\n", iid)

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
	cmd.Flags().BoolVar(&approvalRule, "approval-rule", false, "on review and deny, require an approval via a merge request approval rule")
	cmd.Flags().BoolVar(&reviewers, "reviewers", false, "on review and deny, add the approver groups' members as reviewers")
	cmd.Flags().BoolVar(&autoMerge, "auto-merge", false, "queue the merge when every change passes")
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

// addGroupReviewers puts the members of the approver groups on the merge
// request, so the people who can clear it are told there is something to
// clear.
//
// GitLab reviewers are users, not groups, so each group is expanded. Members
// already reviewing, and the author, are left out by the client.
func addGroupReviewers(ctx context.Context, client *gitlabapi.Client, cmd *cobra.Command, iid int, groups []int) error {
	if len(groups) == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: --reviewers needs --approver-group-id to know who to add — nobody was added")
		return nil
	}

	var users []int
	for _, group := range groups {
		members, err := client.GroupMembers(ctx, group)
		if err != nil {
			return fmt.Errorf("adding reviewers: %w", err)
		}
		// An empty group is worth saying out loud: it looks identical to a
		// working configuration from the merge request's side.
		if len(members) == 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: group %d has no active members to add as reviewers\n", group)
		}
		users = append(users, members...)
	}

	added, err := client.AddReviewers(ctx, iid, users)
	if err != nil {
		return fmt.Errorf("adding reviewers to !%d: %w", iid, err)
	}
	if len(added) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "reviewers already on !%d — none added\n", iid)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "added %d reviewer(s) to !%d\n", len(added), iid)
	return nil
}

// splitList parses a comma-separated environment variable.
func splitList(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}
