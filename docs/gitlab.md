# GitLab

Include the ready-made jobs:

```yaml
include:
  - remote: https://raw.githubusercontent.com/raccoon-core/blastdoor/v1/ci/gitlab/blastdoor.yml

stages: [plan, risk]
```

The apply job (below) runs in `.post`, which GitLab always defines, so it needs
no entry in `stages:` and no change to this list.

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

## The deployment method

`eval` can also answer a second question, per environment: may this be applied
unattended? Set `BLASTDOOR_DEPLOYMENT_METHOD_WISH` — comma-separated
`env=method` pairs, e.g. `int=auto,stg=auto,prd=manual` — and `eval` folds each
environment's worst verdict into `auto`, `manual` or `none`. Leave the variable
unset and the whole feature is off: no new dotenv keys, no generated pipeline,
no change in behaviour.

The wish is a **ceiling, not a default.** A `review` or `deny` verdict, a
guarded path, or an uncovered file all force `manual` regardless of what was
asked for; nothing here can turn a `manual` wish into an unattended apply. See
[Verdicts](verdicts.md) for how the fold works.

`none` means no unit in that environment changed. Emitting `auto` for an
environment with nothing to apply would be true but useless — it would
generate a job that runs the repository's apply script against an empty unit
list. `none` says the honest thing instead: no apply job is generated for it
at all.

### Stating a wish requires the plan job to say which environment it planned

`eval` folds environments from `environment.txt`, which `blastdoor plan`
writes when it is passed `--environment`. The shipped `blastdoor:plan` job is
a single job planning a single unit list, and does not pass `--environment` to
anything by default — there is no environment for it to name. Setting
`BLASTDOOR_DEPLOYMENT_METHOD_WISH` without also arranging for `plan` to run
once per environment gets every unit planned with no environment recorded at
all, and `eval` fails outright rather than guessing one:

```
blastdoor: unit "<path>" has no environment recorded: pass --environment to
'blastdoor plan' so it writes one beside the plan
```

If you see that error after setting a wish, this is why. A project running
`int`, `stg` and `prd` overrides `blastdoor:plan` in its own `.gitlab-ci.yml`
with a `parallel:matrix` supplying `ENV`, and a `--units-file` scoped to that
environment (a `grep` over the full unit list is the usual way to build it) —
each leg then calls `blastdoor plan --environment "$ENV"` for its own slice.
The environment split is the consuming project's own pipeline shape, the same
way its apply command is; the shared template only threads `--environment
"$ENV"` through so a project that does this has somewhere to plug it in.

To use this, somewhere provides a hidden `.blastdoor:apply` job: its image,
its credentials, and the apply command itself, reading `$BLASTDOOR_ENV` to
know which environment it is applying. Blastdoor generates the `when:`; it has
no way to know your image, your credentials, or whether you apply a saved plan
or re-plan, so it does not generate the job that does the applying.

Two ways to supply it, both via `blastdoor eval`'s flags (or the matching
`BLASTDOOR_APPLY_INCLUDE*` CI variables):

- **`--apply-include`** (default `.gitlab/blastdoor-apply.yml`) — a `local:`
  include, the job lives in the repository being judged. A minimal example,
  obviously a template rather than something to use as-is:

  ```yaml
  # .gitlab/blastdoor-apply.yml
  .blastdoor:apply:
    image: your-terraform-image:tag
    variables:
      # However your project supplies credentials to an apply job — a
      # protected CI/CD variable, OIDC, vault: — goes here, not in this file.
      TF_VAR_something: $YOUR_APPLY_CREDENTIAL
    script:
      - terraform -chdir="environments/$BLASTDOOR_ENV" apply -auto-approve
  ```

- **`--apply-include-project`** (optionally with `--apply-include-ref`) — a
  `project:`/`ref:` include instead, so a single `.blastdoor:apply` defined
  once in a shared repository can be reused by every consumer, rather than
  copied into each one. `--apply-include` then names the file *within* that
  project. Useful when many repositories share the same apply shape (same
  image, same toolchain, same credential pattern) and would otherwise be
  maintaining near-identical copies.

`$BLASTDOOR_APPLY_INCLUDE` must stay in `BLASTDOOR_GUARD_PATHS` — see
[Hardening](hardening.md#the-apply-include-must-be-guarded-too) — because this
is the file that runs with production credentials. That guard only covers the
`local:` shape, though: see the note there about `--apply-include-project`.

**`strategy: depend` means the parent pipeline waits on this.** `blastdoor:apply`
triggers the generated pipeline with `strategy: depend`, so the parent mirrors
the child's state rather than firing and forgetting it. This is a consequence
of the design, not a warning to work around: with `prd=manual`, the main
pipeline sits unfinished — neither passed nor failed — until somebody clicks
the manual `apply:prd` job in the generated pipeline. That is what "the pipeline
states a wish, the verdict may only tighten it" is for.

### Why the decision is not just a dotenv variable

The obvious design is to write `BLASTDOOR_DEPLOY_PRD=manual` into
`artifacts:reports:dotenv` and read it from the apply job's `when:`. **This does
not work, and it fails silently rather than with an error**, so it is worth
spelling out why before anyone tries it:

- `when:` does not accept a variable. `when: $BLASTDOOR_DEPLOY_PRD` is a
  template error ([gitlab#31974]).
- The documented workaround is `rules:`, which *can* set `when:` conditionally —
  but rules are evaluated at **pipeline creation**, before any job has run, and
  GitLab states plainly that dotenv variables are unavailable in `rules:`
  ([dotenv_variables], [gitlab#235812]).

So a rule like

```yaml
rules:
  - if: $BLASTDOOR_DEPLOY_PRD == "manual"
    when: manual
```

parses without complaint and simply never matches: the variable it names does
not exist yet at the point GitLab reads this. The job runs with whatever
`when:` its other rules give it, and nothing tells you the condition was dead.

The decision is therefore delivered twice, in two different forms, because
neither form alone can do the whole job:

- **`blastdoor.env`** is the record — read by humans in job logs, and by
  anything that wants the decision as data.
- **`apply.gitlab-ci.yml`**, written by `eval` into its `--out-dir` whenever a
  wish was stated, is the mechanism — a generated child pipeline whose jobs
  carry a **literal** `when: on_success` or `when: manual`, one per
  environment that isn't `none`, triggered with `trigger: include: artifact:`.
  A literal `when:` is the only form of this decision GitLab will actually
  act on. A wish where every environment resolves to `none` — the ordinary
  case for a docs-only or CI-only change — still gets a file: one holding a
  single placeholder job rather than the `include:`-and-nothing-else that
  GitLab refuses to build a pipeline from.

The dotenv is the record. The generated YAML is the mechanism. Read the
dotenv if you want to know what happened; the generated pipeline is what makes
it happen.

[gitlab#31974]: https://gitlab.com/gitlab-org/gitlab/-/issues/31974
[gitlab#235812]: https://gitlab.com/gitlab-org/gitlab/-/issues/235812
[dotenv_variables]: https://docs.gitlab.com/ci/variables/dotenv_variables/
