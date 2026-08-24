# blastdoor

Terraform/OpenTofu plan checker for self-service.

Blastdoor scores a plan against [OPA](https://www.openpolicyagent.org/) policies
you write, then decides whether a merge request can go through on its own or
needs a human. **Every resource change starts at the maximum score of 100.**
Policies bring it down by classifying changes they understand, so a plan that
touches something no policy covers is never waved through.

## Quick start

Score one of the bundled example plans:

```console
$ docker run --rm -v "$PWD:/work" raccooncore/blastdoor \
    eval --plan examples/plans/kafka-topic-create.json --policy examples/policies

## Blastdoor risk assessment

**Pass** — total risk score **0** is below the threshold of 50.

| Unit | Resource | Score | Finding |
|---|---|---|---|
| … | kafka_topic.topics["orders.created.v1"] | 0 | creating topic orders.created.v1 |
```

Swap the plan for `unclassified-resource.json` and the score jumps to 100: no
policy claims an `aws_s3_bucket`, so the door stays shut.

## Writing a policy

A policy is Rego in `package blastdoor` that does two things — scores a change,
and claims it:

```rego
package blastdoor

# Score it.
deny contains {"msg": msg, "score": 0, "resource": rc.address} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["create"]
	msg := sprintf("%s: creating a topic", [rc.address])
}

# Claim it, so the built-in backstop stops scoring it 100.
classified contains rc.address if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["create"]
}
```

`input` is the plan JSON from `tofu show -json`. A finding needs `msg`; `score`
defaults to 100 when omitted, and `resource` is what shows up in the report.

A score of 0 is how a policy green-flags a change. Keep the `classified` claim
as narrow as the rule that justifies it — a claim wider than the rule is how
changes slip through unscored.

See [examples/](examples/) for a worked policy and a plan per scenario, and
[examples/README.md](examples/README.md) for how to iterate on one.

## What "denied by default" means

Only a policy can lower a score, and only for what it explicitly claims.
Everything else is the maximum:

| | |
|---|---|
| A resource type no policy claims | 100 |
| A claimed type in an unclaimed shape (a replace, say) | 100 |
| A finding written without a `score` | 100 |
| A negative score | rejected — it would mask risk found elsewhere |
| A fractional score | rounded away from 0, never into "allowed" |
| Anything that isn't plan JSON | an error, never a score of 0 |
| A run that scored no units at all | the gate abstains — no approval, no merge |

The only change the backstop passes on its own is a `no-op`. These are pinned
by [`default_deny_test.go`](internal/policy/default_deny_test.go); `eval
--no-base-policy` turns the backstop off for debugging a policy in isolation
and does not belong in CI.

## Commands

| Command | What it does |
|---|---|
| `blastdoor detect` | Lists the units a change touches, from the git diff |
| `blastdoor plan` | Runs init/plan/show for each unit, saving plan JSON |
| `blastdoor eval` | Scores plan JSON, writing `report.json`, `summary.md`, `blastdoor.env` |
| `blastdoor gate` | Posts the summary on a GitLab merge request and gates it |

`--help` on any of them has the flags.

A unit is a directory with a `terragrunt.hcl` or `.tf` files. `detect` treats a
unit as affected when a `.hcl`/`.tf` file changed in the unit *or in any parent
directory*, matching how Terragrunt's `find_in_parent_folders()` shares config —
so editing one `component.hcl` plans every environment under it.

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

Set `BLASTDOOR_GITLAB_TOKEN` to a token with the `api` scope. Above the
threshold, `gate` puts an approval rule on the merge request; below it, it
approves, and with `--auto-merge` queues the merge for when the pipeline
succeeds. A 401/403 fails the job rather than being mistaken for "nothing to
gate".

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

Commits follow [Conventional Commits](https://www.conventionalcommits.org/) —
`fix:` and `feat:` on `main` release themselves, versioned by semantic-release.

## License

Apache 2.0. See [LICENSE](LICENSE).
