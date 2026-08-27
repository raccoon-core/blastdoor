# GitLab

Include the ready-made jobs:

```yaml
include:
  - remote: https://raw.githubusercontent.com/raccoon-core/blastdoor/v1/ci/gitlab/blastdoor.yml

stages: [plan, risk]
```

Set `BLASTDOOR_GITLAB_TOKEN` to a token with the `api` scope; the rest of the
pipeline's variables are in [Environment](environment.md). `gate` approves a
passing merge request, requires an approval on `review`, and on `deny` also
fails the job. A 401/403 fails the job rather than being mistaken for "nothing
to gate".

## Telling people there is something to look at

`--reviewers` puts the approver groups' members on the merge request as
reviewers when the verdict is `review` or `deny`, so the people who can clear
it are told there is something to clear.

GitLab reviewers are users rather than groups, so each group in
`approver_group_ids` is expanded into its **active direct members** — the token
has to be able to read them. Membership inherited from a parent group is
deliberately not followed: naming one team as an approver should not quietly
put the whole organisation on a merge request. Whoever is already reviewing
stays, since GitLab's `reviewer_ids` replaces the list rather than appending to
it, and the author is never added to review their own change.

A merge request that already has everyone is left untouched, so re-running a
pipeline does not show up as an edit. It runs after the approval rule, so a
change is gated even if naming reviewers then fails.

## Merge request or branch

Both work. What changes is where the diff starts from:

| Pipeline | Base of the diff |
|---|---|
| Merge request | `CI_MERGE_REQUEST_DIFF_BASE_SHA`, which GitLab has already worked out |
| Branch | The merge base with `CI_DEFAULT_BRANCH` |

Leave `--base-ref` unset and blastdoor picks the right one.

!!! danger "Do not pass `$CI_COMMIT_SHA` as the base on a branch pipeline"

    It *is* `HEAD`, so the diff is empty and nothing gets planned or gated.
    Blastdoor refuses that rather than reporting "no changes".

A branch pipeline needs the full history for the merge base, so the template
sets `GIT_DEPTH: 0`. Only work that landed on the branch counts; commits that
reached the default branch afterwards do not.

`gate` finds the open merge request for the branch by itself. With none open it
says so and does nothing, so the earlier jobs still report on every push.

See [ci/gitlab/blastdoor.yml](https://github.com/raccoon-core/blastdoor/blob/main/ci/gitlab/blastdoor.yml)
for the variables.
