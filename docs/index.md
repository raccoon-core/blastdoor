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

## Where to go next

<div class="grid cards" markdown>

- :material-download: **[Install](install.md)**

    The image, the binary, and what each one can do.

- :material-shield-key: **[Your first policy](writing-policies.md)**

    The shape of a rule, and what `input` holds.

- :material-scale-balance: **[The three verdicts](verdicts.md)**

    What `pass`, `review` and `deny` mean, and what is denied by default.

- :material-cog: **[Configuration](configuration.md)**

    `.blastdoor.yml`, and the one rule that decides every setting.

- :material-gitlab: **[GitLab](gitlab.md)**

    The ready-made jobs, and how the gate acts on a merge request.

- :material-lock: **[Hardening](hardening.md)**

    The threat model — read this before relying on the gate.

</div>

!!! warning "Verdicts are not the whole story"

    A change that can edit the policies judging it, or delete the job that runs
    them, does not need to beat them. Pass `--guard-path` (the GitLab template
    does by default) and read [Hardening](hardening.md) before relying on the
    gate.
