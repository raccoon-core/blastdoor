# blastdoor

Terraform/OpenTofu plan checker for self-service.

Blastdoor judges every change in a plan against [OPA](https://www.openpolicyagent.org/)
policies you write. Each change comes back **pass**, **review** or **deny**, and
the worst one decides the merge request.

Each rule answers one question about one change — is this fine, does a person
need to look, or is this not allowed — and says why in a sentence the reviewer
reads. **A change no policy matches is denied.**

## Quick start

Judge one of the bundled example plans:

```console
$ docker run --rm -v "$PWD:/work" raccooncore/blastdoor \
    eval --plan examples/plans/kafka-topic-create.json --policy examples/policies

## Blastdoor

**Pass** — every one of the 1 change(s) is allowed by policy.

| Verdict | Unit | Change | Why |
|---|---|---|---|
| pass | … | `kafka_topic.topics["orders.created.v1"]` (create) | creating topic orders.created.v1 |
```

Swap the plan for `unmatched-resource.json` and it comes back **denied**: no
rule matches an `aws_s3_bucket`, so the door stays shut and the summary names
the change that needs one.

## Writing a policy

A policy is Rego in `package blastdoor` that puts changes into one of three
rule sets, each with a reason:

```rego
package blastdoor

allow contains {"resource": rc.address, "reason": "creating a topic is additive"} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["create"]
}

review contains {"resource": rc.address, "reason": "deleting a topic destroys its data"} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["delete"]
}

deny contains {"resource": rc.address, "reason": "wildcard grants unbounded access"} if {
	some rc in input.resource_changes
	rc.type == "kafka_acl"
	contains(rc.change.after.acl_principal, "*")
}
```

`input` is the plan JSON from `tofu show -json`. Every judgement needs a
`resource` to attach to and a `reason` — the reason is what the reviewer reads,
so both are required rather than optional.

When several rules in the same layer match one change, **the most severe wins**,
so adding a rule to a layer can only make it stricter. Across layers a higher
weight overrides a lower one — see [Layered policies](#layered-policies).

See [examples/](examples/) for a worked policy and a plan per scenario, and
[examples/README.md](examples/README.md) for how to iterate on one.

## The three verdicts

| Verdict | Meaning | What the gate does |
|---|---|---|
| `pass` | A rule allows it | Approves, and with `--auto-merge` queues the merge |
| `review` | A rule wants a person | Posts the summary, and with `--approval-rule` requires a human approval |
| `deny` | A rule forbids it, or no rule matched it | The same **and** fails the job, so the pipeline is red |

The approval rule is opt-in, because it writes to the merge request. Without
`--approval-rule` the gate still posts the summary, still fails the job on a
denial, and still withdraws any approval it gave an earlier and safer push —
but nothing stops a reviewed change going in unapproved. The GitLab template
passes it, the same way it passes `--guard-path`.

The plan takes the worst verdict of any change in it: one change a policy
forbids is not offset by nine it allows.

`deny` and `review` are deliberately different. A review is a question for a
person, and approving answers it. A denial is not — it says this should not
happen, so clearing it means changing the plan or changing the policy.

Denied by default, specifically:

| | |
|---|---|
| A change no rule matches | `deny`, reason "no policy judges this change" |
| A type with rules, in a shape none of them match | `deny` |
| A judgement with no `resource` or no `reason` | an error — it cannot be attached or explained |
| Anything that isn't plan JSON | an error, never an empty plan that passes |
| A run with no units at all | the gate abstains — no approval, no merge |

A `no-op` is the only change needing no rule. These are pinned by
[`policy_test.go`](internal/policy/policy_test.go).

Verdicts are not the whole story: a change that can edit the policies judging
it, or delete the job that runs them, does not need to beat them. Pass
`--guard-path` (the GitLab template does by default) and read
[docs/hardening.md](docs/hardening.md) before relying on the gate.

## Commands

| Command | What it does |
|---|---|
| `blastdoor detect` | Lists the units a change touches, from the git diff |
| `blastdoor prepare` | Installs the tool versions those units need |
| `blastdoor plan` | Runs init/plan/show for each unit, saving plan JSON |
| `blastdoor eval` | Judges plan JSON, writing `report.json`, `summary.md`, `blastdoor.env` |
| `blastdoor gate` | Posts the summary on a GitLab merge request and gates it |

`--help` on any of them has the flags.

A unit is a directory with a `terragrunt.hcl` or `.tf` files. `detect` treats a
unit as affected when a `.hcl`, `.tf`, `.tfvars`, `.tf.json` or `.tfvars.json`
file changed in the unit *or in any parent directory*, matching how Terragrunt's
`find_in_parent_folders()` shares config — so editing one `component.hcl` plans
every environment under it.

## Configuration

Settings that describe a repository can live in a `.blastdoor.yml` beside it,
rather than being repeated as flags in every job:

```yaml
root: terraform
policies:
  local:
    repository: .
    directory: policy
    weight: 0
require_coverage: true
guard:
  - policy
  - .gitlab-ci.yml
ignore:
  - ansible
  - "**/README.md"
variables:
  max_partitions: 32
```

That is the useful half of it. Every key blastdoor understands, with what each
one does and defaults to, is in
[examples/blastdoor.yml](examples/blastdoor.yml).

Blastdoor reads it from the directory it runs in. There is no search upwards
and no per-directory config: the file that judges a change must be one a
reviewer can find.

`--config` names a different file, or `BLASTDOOR_CONFIG` does for a job that
cannot choose its working directory; the flag wins over the variable. A config
named either way has to exist — asking for one and silently getting none would
run with no guards and no ignore list.

One rule decides every setting: **the flag if it was given, otherwise the
config, otherwise the default.** Lists are replaced whole, never merged.

`variables` is the exception in shape rather than precedence: its keys belong to
whoever wrote the policies, so it is the one place unknown names are not an
error. Policies read them as `data.variables`, which is what lets a shared rule
carry a default a repository can move:

```rego
default max_partitions := 10

max_partitions := data.variables.max_partitions if {
	data.variables.max_partitions
}
```

They are mounted at `data.variables` and never at the root, so a variable cannot
land on `data.blastdoor` and displace the rules themselves — the reason this
exists rather than letting the loader read `.json` out of a policy directory.
Nothing caps what a repository may set: the config guards itself, so raising a
limit forces a person to look at the commit that raised it.

Two things do not follow that rule, both on purpose:

- **A config file guards itself.** Its path is added to the guard list whatever
  the config says, so a merge request cannot quietly edit `.blastdoor.yml` to
  excuse the tree it is changing.
- **A key blastdoor does not understand rejects the whole file** and fails the
  command. Carrying on would mean running with no guards and no ignore list —
  and that is also what a config written for a newer blastdoor looks like.

Because guards are an override, a pipeline that passes no `--guard-path` lets
the repository decide what is guarded. The GitLab template always passes one;
see [docs/hardening.md](docs/hardening.md) if you wire the commands up yourself.

### Environment

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

`BLASTDOOR_APPROVER_GROUP_IDS` is the one variable that outranks the config
rather than merely filling a flag in: a branch naming its own approver group in
`.blastdoor.yml` is a branch approving itself, so the pipeline's list wins over
the repository's.

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

`eval` writes the verdict back out to `blastdoor.env` — `BLASTDOOR_VERDICT`,
`BLASTDOOR_UNIT_COUNT`, `BLASTDOOR_PASS_COUNT`, `BLASTDOOR_REVIEW_COUNT`,
`BLASTDOOR_DENY_COUNT` — which the GitLab template publishes as a dotenv report
so a later job can branch on the outcome without parsing `report.json`.

Everything else in the environment is handed to the tool being run, which is
how provider and backend credentials reach it. The two exceptions are
`MISE_SAFE=1` and `MISE_YES=1`, which blastdoor sets for every mise invocation
and a repository cannot unset — see
[Terraform, OpenTofu, Terragrunt](#terraform-opentofu-terragrunt).

The other `BLASTDOOR_*` names in
[ci/gitlab/blastdoor.yml](ci/gitlab/blastdoor.yml) — `BLASTDOOR_IMAGE`,
`BLASTDOOR_ROOT`, `BLASTDOOR_GUARD_PATHS` and the rest — belong to the
template, not to blastdoor: the jobs turn them into flags on the command line.
The binary never reads them, so setting one outside those jobs does nothing.

### Layered policies

`policies` can name several sources, so an organisation can tier its rules: a
company layer every repository is judged by, a domain layer refining it, and
the repository itself with the last word.

```yaml
policies:
  company:
    repository: https://git.example.com/policies
    ref: v1                     # a branch, tag or commit
    directory: rules/company
    weight: 0
  domain:
    repository: https://git.example.com/policies
    ref: v1
    directory: rules/domain
    weight: 1
  local:
    repository: .               # the working tree, never fetched
    directory: policy
    weight: 99
```

Each layer is fetched at its ref and evaluated on its own. For one change,
**the highest-weight layer that judged it at all decides it** — a layer that
says nothing falls through to the next one down, which is what lets a tier add
rules without restating the ones beneath it. A change no layer judges is still
denied.

Weights must be unique: two layers at the same weight have no order between
them. A remote layer must name a `ref`, and every layer must state a `weight` —
defaulting it to zero would quietly put a layer at the bottom.

> **This means a repository can loosen its company's rules.** A local `allow`
> at weight 99 beats a company `deny` at weight 0. That is what tiering is for,
> and it is only contained by guarding the layer list *and* the local policy
> directory — read [docs/hardening.md](docs/hardening.md) before relying on a
> shared policy being binding.

The note names the layers, the commit each ref resolved to, and which layer
overrode which:

```
Judged by: local, domain (v1@470da52), company (v1@470da52) — highest weight first.

| ✅ pass | … | `kafka_acl.a` (create) | local: approved by exception (local overrides: company said deny) |
```

A source that cannot be fetched fails the command. Evaluating with the layers
that did arrive would drop a company layer's `deny` rules the moment its host
was unreachable.

## Terraform, OpenTofu, Terragrunt

Which version runs is a version manager's job, and the image ships both:

| Manager | Reads | Used when |
|---|---|---|
| [tenv](https://github.com/tofuutils/tenv) | `.opentofu-version`, `.terraform-version`, `.terragrunt-version`, `terragrunt.hcl` constraints | The default |
| [mise](https://mise.jdx.dev) | `mise.toml`, `.tool-versions` | The unit or an ancestor is a mise project |

`blastdoor prepare` installs those versions before anything is planned. Run it
in the same job as `plan` — as its own step, so a toolchain that will not
install does not look like a plan that will not run. `--manager` overrides the
choice (`auto`, `tenv`, `mise`, `none`); `none` uses whatever is on `PATH`.

mise runs with `MISE_SAFE=1`, which stops a repository's own `mise.toml`
executing code — hooks, tasks, `[env]` injection, `exec()` in templates —
while resolving versions. Blastdoor judges merge requests it does not trust, so
this is on by default and should stay on.

`--tool` picks the binary; the default `auto` uses Terragrunt for a Terragrunt
unit, and otherwise whichever of OpenTofu/Terraform the repository pins.

Terragrunt drives one of the two, and blastdoor works out which from the same
pins — the nearest `.terraform-version` or `.opentofu-version` at or above the
unit, so a file at the repository root still governs a unit several directories
down. For a mise project it asks mise instead. With nothing pinned it uses
OpenTofu. Every plan logs which it chose and what decided it; override with
`--terragrunt-tf-path`.

Whichever it lands on is recorded beside the plan and titles the summary, so
the note on a merge request says **Terraform Blastdoor** or **OpenTofu
Blastdoor** without anyone opening the job log. A repository part-way between
the two gets both. The plan JSON cannot answer this on its own — it carries a
`terraform_version` key whichever tool wrote it.

## GitLab

Include the ready-made jobs:

```yaml
include:
  - remote: https://raw.githubusercontent.com/raccoon-core/blastdoor/v1/ci/gitlab/blastdoor.yml

stages: [plan, risk]
```

Set `BLASTDOOR_GITLAB_TOKEN` to a token with the `api` scope; the rest of the
pipeline's variables are in [Environment](#environment). `gate` approves a
passing merge request, requires an approval on `review`, and on `deny` also
fails the job. A 401/403 fails the job rather than being mistaken for "nothing
to gate".

### Telling people there is something to look at

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

### Merge request or branch

Both work. What changes is where the diff starts from:

| Pipeline | Base of the diff |
|---|---|
| Merge request | `CI_MERGE_REQUEST_DIFF_BASE_SHA`, which GitLab has already worked out |
| Branch | The merge base with `CI_DEFAULT_BRANCH` |

Leave `--base-ref` unset and blastdoor picks the right one. Do not pass
`$CI_COMMIT_SHA` on a branch pipeline — it *is* `HEAD`, so the diff is empty
and nothing gets planned or gated. Blastdoor refuses that rather than reporting
"no changes".

A branch pipeline needs the full history for the merge base, so the template
sets `GIT_DEPTH: 0`. Only work that landed on the branch counts; commits that
reached the default branch afterwards do not.

`gate` finds the open merge request for the branch by itself. With none open it
says so and does nothing, so the earlier jobs still report on every push.

See [ci/gitlab/blastdoor.yml](ci/gitlab/blastdoor.yml) for the variables.

## Install

```console
docker pull raccooncore/blastdoor
# or
go install github.com/raccoon-core/blastdoor/cmd/blastdoor@latest
```

Binaries for linux, macOS and Windows are attached to each
[release](https://github.com/raccoon-core/blastdoor/releases).

The image bundles tenv and mise, so `prepare` and `plan` work out of the box. A
bare binary gives you `eval` and `gate`; `prepare` needs tenv or mise, and
`plan` needs tofu/terraform/terragrunt reachable one way or another.

## Contributing

[CONTRIBUTING.md](CONTRIBUTING.md). `make check` runs everything CI does.
Agents: [AGENTS.md](AGENTS.md).

Commits follow [Conventional Commits](https://www.conventionalcommits.org/) —
`fix:` and `feat:` on `main` release themselves, versioned by semantic-release.

## License

Apache 2.0. See [LICENSE](LICENSE).
