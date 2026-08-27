# Layered policies

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

!!! danger "A repository can loosen its company's rules"

    A local `allow` at weight 99 beats a company `deny` at weight 0. That is
    what tiering is for, and it is only contained by guarding the layer list
    *and* the local policy directory — read [Hardening](hardening.md) before
    relying on a shared policy being binding.

## What the note records

The note names the layers, the commit each ref resolved to, and which layer
overrode which:

```
Judged by: local, domain (v1@470da52), company (v1@470da52) — highest weight first.

| ✅ pass | … | `kafka_acl.a` (create) | local: approved by exception (local overrides: company said deny) |
```

A source that cannot be fetched fails the command. Evaluating with the layers
that did arrive would drop a company layer's `deny` rules the moment its host
was unreachable.
