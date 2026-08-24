# blastdoor

Terraform/OpenTofu plan checker for self-service.

Blastdoor judges every change in a plan against [OPA](https://www.openpolicyagent.org/)
policies you write. Each change comes back **pass**, **review** or **deny**, and
the worst one decides the merge request.

There is no score and no threshold. A policy author answers a question they can
actually answer — is this fine, does a person need to look, or is this not
allowed? — instead of inventing a number and hoping it lands the right side of
a cutoff. **A change no policy matches is denied.**

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

Swap the plan for `unclassified-resource.json` and it comes back **denied**: no
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

When several rules match one change, **the most severe wins**. Adding a rule can
therefore only ever make a change stricter, never weaker.

See [examples/](examples/) for a worked policy and a plan per scenario, and
[examples/README.md](examples/README.md) for how to iterate on one.

## The three verdicts

| Verdict | Meaning | What the gate does |
|---|---|---|
| `pass` | A rule allows it | Approves, and with `--auto-merge` queues the merge |
| `review` | A rule wants a person | Requires a human approval |
| `deny` | A rule forbids it, or no rule matched it | Requires approval **and** fails the job, so the pipeline is red |

The plan takes the worst verdict of any change in it. No arithmetic: ten
changes a policy is happy with do not add up to a problem, and one it forbids
is not offset by nine that are fine.

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
| `blastdoor plan` | Runs init/plan/show for each unit, saving plan JSON |
| `blastdoor eval` | Judges plan JSON, writing `report.json`, `summary.md`, `blastdoor.env` |
| `blastdoor gate` | Posts the summary on a GitLab merge request and gates it |

`--help` on any of them has the flags.

A unit is a directory with a `terragrunt.hcl` or `.tf` files. `detect` treats a
unit as affected when a `.hcl`, `.tf`, `.tfvars`, `.tf.json` or `.tfvars.json`
file changed in the unit *or in any parent directory*, matching how Terragrunt's
`find_in_parent_folders()` shares config — so editing one `component.hcl` plans
every environment under it.

## Terraform, OpenTofu, Terragrunt

Version selection is [tenv](https://github.com/tofuutils/tenv)'s job. The image
ships it with `TENV_AUTO_INSTALL=true`, so each unit gets the version its
`.opentofu-version`, `.terraform-version`, `.terragrunt-version` file or
`terragrunt.hcl` constraint asks for.

`blastdoor plan --tool` picks the binary; the default `auto` uses Terragrunt for
a Terragrunt unit and otherwise follows the version files, defaulting to
OpenTofu. Terragrunt wraps OpenTofu unless you pass `--terragrunt-tf-path`.

## GitLab

Include the ready-made jobs:

```yaml
include:
  - remote: https://raw.githubusercontent.com/raccoon-core/blastdoor/v1/ci/gitlab/blastdoor.yml

stages: [plan, risk]
```

Set `BLASTDOOR_GITLAB_TOKEN` to a token with the `api` scope. `gate` approves a
passing merge request, requires an approval on `review`, and on `deny` also
fails the job. A 401/403 fails the job rather than being mistaken for "nothing
to gate".

See [ci/gitlab/blastdoor.yml](ci/gitlab/blastdoor.yml) for the variables.

## Install

```console
docker pull raccooncore/blastdoor
# or
go install github.com/raccoon-core/blastdoor/cmd/blastdoor@latest
```

Binaries for linux, macOS and Windows are attached to each
[release](https://github.com/raccoon-core/blastdoor/releases).

The image bundles tenv, so `plan` works out of the box. A bare binary gives you
`eval` and `gate`; `plan` also needs tofu/terraform/terragrunt on your `PATH`.

## Contributing

[CONTRIBUTING.md](CONTRIBUTING.md). `make check` runs everything CI does.
Agents: [AGENTS.md](AGENTS.md).

Commits follow [Conventional Commits](https://www.conventionalcommits.org/) —
`fix:` and `feat:` on `main` release themselves, versioned by semantic-release.

## License

Apache 2.0. See [LICENSE](LICENSE).
