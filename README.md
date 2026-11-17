# blastdoor

Terraform/OpenTofu plan checker for self-service.

**[Documentation →](https://raccoon-core.github.io/blastdoor/)**

Blastdoor judges every change in a plan against [OPA](https://www.openpolicyagent.org/)
policies you write. Each change comes back **pass**, **review** or **deny**, and
the worst one decides the merge request.

Each rule answers one question about one change — is this fine, does a person
need to look, or is this not allowed — and says why in a sentence the reviewer
reads. **A change no policy matches is sent to review.**

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

Swap the plan 