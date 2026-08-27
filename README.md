# blastdoor

Terraform/OpenTofu plan checker for self-service.

**[Documentation →](https://raccoon-core.github.io/blastdoor/)**

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

A policy is Rego in `package blastdoor` contributing to three rule sets, each
judgement carrying the resource it is about and the reason a reviewer reads:

```rego
package blastdoor

allow contains {"resource": rc.address, "reason": "creating a topic is additive"} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["create"]
}
```

## Install

```console
docker pull raccooncore/blastdoor
# or
go install github.com/raccoon-core/blastdoor/cmd/blastdoor@latest
```

Binaries for linux, macOS and Windows are attached to each
[release](https://github.com/raccoon-core/blastdoor/releases).

## Documentation

Everything lives at **[raccoon-core.github.io/blastdoor](https://raccoon-core.github.io/blastdoor/)**,
built from [`docs/`](docs/):

| | |
|---|---|
| [Your first policy](https://raccoon-core.github.io/blastdoor/writing-policies/) | The shape of a rule, and what `input` holds |
| [The three verdicts](https://raccoon-core.github.io/blastdoor/verdicts/) | What `pass`, `review` and `deny` mean, and what is denied by default |
| [Commands](https://raccoon-core.github.io/blastdoor/commands/) | `detect`, `prepare`, `plan`, `eval`, `gate` |
| [Configuration](https://raccoon-core.github.io/blastdoor/configuration/) | `.blastdoor.yml`, environment, gating, layered policies |
| [Terraform, OpenTofu, Terragrunt](https://raccoon-core.github.io/blastdoor/toolchain/) | How the version and the binary are resolved |
| [GitLab](https://raccoon-core.github.io/blastdoor/gitlab/) | The ready-made jobs, and how the gate acts |
| [Hardening](https://raccoon-core.github.io/blastdoor/hardening/) | The threat model — read it before relying on the gate |

> [!WARNING]
> A change that can edit the policies judging it, or delete the job that runs
> them, does not need to beat them. Pass `--guard-path` (the GitLab template
> does by default) and read [docs/hardening.md](docs/hardening.md) before
> relying on the gate.

## Contributing

[CONTRIBUTING.md](CONTRIBUTING.md). `make check` runs everything CI does.
Agents: [AGENTS.md](AGENTS.md).

Commits follow [Conventional Commits](https://www.conventionalcommits.org/) —
`fix:` and `feat:` on `main` release themselves, versioned by semantic-release.

To work on the docs: `make docs-serve` (needs Python), or `make docs-build` to
check the site compiles the way CI does.

## License

Apache 2.0. See [LICENSE](LICENSE).
