# Environment

A variable is a flag default and nothing more, so the same rule still decides
every setting: **the flag if it was given, otherwise the variable, otherwise
the config, otherwise the default.** Only what a repository's own config cannot
carry is wired this way — a credential, and what the pipeline already knows
about itself.

| Variable | Used by | Sets |
|---|---|---|
| `BLASTDOOR_CONFIG` | all | `--config`, for a job that cannot choose its working directory |
| `BLASTDOOR_GITLAB_TOKEN` | `gate` | `--token` — a token with the `api` scope |
| `GITLAB_TOKEN` | `gate` | `--token`, read only when `BLASTDOOR_GITLAB_TOKEN` is unset |
| `BLASTDOOR_APPROVER_GROUP_IDS` | `gate` | `--approver-group-id`, comma-separated group ids |
| `BLASTDOOR_DEPLOYMENT_METHOD_WISH` | `eval` | `--deployment-method-wish`, per-environment ceiling, e.g. `int=auto,stg=auto,prd=manual` |

`BLASTDOOR_APPROVER_GROUP_IDS` and `BLASTDOOR_DEPLOYMENT_METHOD_WISH` are the
two variables that outrank the config rather than merely filling a flag in: a
branch naming its own approver group, or its own deployment method, in
`.blastdoor.yml` is a branch approving itself — the config decoder rejects an
`environments:` key outright rather than let the wish in by that door — so the
pipeline's statement wins over the repository's for both.

## What GitLab sets itself

GitLab sets the rest itself, and blastdoor reads them so a job needs no flags
to say where it is running:

| Variable | Used by | Sets |
|---|---|---|
| `CI_MERGE_REQUEST_DIFF_BASE_SHA` | `detect`, `plan`, `eval` | the base of the diff on a merge request pipeline |
| `CI_DEFAULT_BRANCH` | `detect`, `plan`, `eval` | the branch to take the merge base with on a branch pipeline (default `main`) |
| `CI_MERGE_REQUEST_IID` | `gate` | `--mr-iid` |
| `CI_COMMIT_BRANCH` | `gate` | `--branch`, used to find the open merge request |
| `CI_PROJECT_ID` | `gate` | `--project-id` |
| `CI_API_V4_URL` | `gate` | `--api-url` (default `https://gitlab.com/api/v4`) |

## What eval writes back out

`eval` writes the verdict back out to `blastdoor.env` — `BLASTDOOR_VERDICT`,
`BLASTDOOR_UNIT_COUNT`, `BLASTDOOR_PASS_COUNT`, `BLASTDOOR_REVIEW_COUNT`,
`BLASTDOOR_DENY_COUNT` — which the GitLab template publishes as a dotenv report
so a later job can branch on the outcome without parsing `report.json`.

When `--deployment-method-wish` names any environments, `eval` also writes one
`BLASTDOOR_DEPLOY_<ENV>` key per environment named — `auto`, `manual` or
`none` — uppercased from the wish's own names. This dotenv is a record for
humans and later tooling to read; it cannot drive a job's `when:` itself. See
[GitLab](gitlab.md#the-deployment-method) for why, and for the generated
pipeline that is the actual mechanism.

## Everything else

Everything else in the environment is handed to the tool being run, which is
how provider and backend credentials reach it. The two exceptions are
`MISE_SAFE=1` and `MISE_YES=1`, which blastdoor sets for every mise invocation
and a repository cannot unset — see
[Terraform, OpenTofu, Terragrunt](toolchain.md).

Most other `BLASTDOOR_*` names in
[ci/gitlab/blastdoor.yml](https://github.com/raccoon-core/blastdoor/blob/main/ci/gitlab/blastdoor.yml)
— `BLASTDOOR_IMAGE`, `BLASTDOOR_ROOT`, `BLASTDOOR_GUARD_PATHS` and the rest —
belong to the template, not to blastdoor: the jobs turn them into flags on the
command line, and the binary never reads them itself, so setting one outside
those jobs does nothing.

`BLASTDOOR_DEPLOYMENT_METHOD_WISH` is the exception, and it is listed in the
first table above rather than here for exactly that reason: `eval`'s
`--deployment-method-wish` flag defaults from it directly, so it works even
when nothing turns it into a flag on the command line.
`BLASTDOOR_APPLY_INCLUDE` is not an exception despite looking like one — it
only ever reaches blastdoor as the `--apply-include` flag value the template
passes; the binary has its own hardcoded default and does not read the
variable back.
