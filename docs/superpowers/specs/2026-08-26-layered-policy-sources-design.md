# Layered policy sources

Status: proposed
Date: 2026-08-26

## What this is for

An organisation wants policies in tiers. The company writes rules every
repository is judged by. A domain or product refines them. A repository has the
last word about itself. Today blastdoor takes a flat list of `--policy` paths,
unions every rule it finds, and lets the most severe verdict decide — which
gives tiers no way to differ from each other.

```yaml
policies:
  global:
    repository: https://git.domain.com/repo
    ref: main
    directory: path/to/policies/global
    weight: 0
  domain:
    repository: https://git.domain.com/repo
    ref: main
    directory: path/to/policies/domain
    weight: 1
  local:
    repository: .
    directory: policies
    weight: 99
```

Higher weight wins, for policies and for variables alike.

## What this costs, stated plainly

Blastdoor's central guarantee today is that **a rule can only tighten a
verdict, never loosen it**. It is why rules are safe to add: a contributor
cannot weaken the gate by contributing to it.

**This design removes that guarantee across layers.** A `local` rule at weight
99 that says `allow` beats a `global` rule at weight 0 that says `deny`. Any
repository can opt out of any company rule by writing one of its own.

That is what tiers mean, and it is what was asked for. It is written here so
that nobody later reads the weighted resolution as a bug and "fixes" it, and so
the containment below is understood as load-bearing rather than incidental.

The guarantee survives **within** a layer: two rules in the same layer still
resolve most-severe-wins. Only the layer boundary is an override.

### What contains it

- **The layer list is guarded.** `.blastdoor.yml` names the sources and their
  weights, and blastdoor guards that file whenever it loads one. Adding a
  higher-weight layer, or moving a weight, forces review of that commit.
- **Local policies live in the repository under review.** A merge request that
  adds a permissive local rule is a merge request that changed a guarded path,
  so a person sees it. This is the only thing standing between "tiers" and
  "every repository approves itself", and it works only if the local policy
  directory is in the guard list. The template and the docs must say so.
- **The note names what judged the change.** A reviewer can see that a verdict
  came from `local` overriding `global`, rather than reading `pass` and
  assuming the company rules agreed.

## Configuration

`policies` is a map of layer name to source. The name is what appears in the
report, so it is chosen by whoever writes the config.

| Key | Meaning |
|---|---|
| `repository` | Git URL to clone, or `.` for the working tree |
| `ref` | Branch, tag or commit to check out. Ignored for `.` |
| `directory` | Path within the source holding the `.rego`. Defaults to the root |
| `weight` | Integer. Higher wins. Required |

Rules:

- **Weights must be unique.** Two layers at the same weight have no defined
  order between them, and silently picking one would make a verdict depend on
  map iteration. Duplicate weights are an error naming both layers.
- **`policies` is the only way to name policies in the config.** The flat
  `policy` key is removed: two ways to say the same thing would leave two
  answers to "what judged this", and a repository with one tier writes one
  layer. Because an unknown key rejects the file, a config still saying
  `policy:` fails by name rather than being quietly ignored.
- **The `--policy` flag stays**, for `eval --plan` while writing a rule and for
  the one-line docker run. When given it replaces the layer set with a single
  unnamed layer, under the same rule as every other flag: the flag if it was
  given, otherwise the config.
- A layer whose `directory` holds no `.rego` is an error, as a `--policy` path
  with no `.rego` already is.

## Fetching

Each remote source is cloned into a cache directory: `git init`, `fetch --depth
1 origin <ref>`, `checkout FETCH_HEAD`, so `ref` may be a branch, a tag or a
commit sha — `clone --branch` rejects shas.

- **Credentials come from the environment**, as they already do for module
  sources: `~/.netrc`, or a URL carrying a token. Blastdoor does not grow its
  own credential store.
- **`repository: .` is not fetched.** It is the working tree, read in place.
- **A source that cannot be fetched is a hard error.** This is the most
  important failure mode in the design: quietly evaluating with the layers that
  did fetch means a company layer that is briefly unreachable takes its `deny`
  rules with it, and the change comes back `pass`. A gate that gets more
  permissive when the network fails is not a gate. Never fall back to a subset
  of layers.
- **Resolved commit shas are recorded** in `report.json` and in the note. `ref:
  main` is mutable: without recording what was actually used, a verdict cannot
  be reproduced or explained after the fact.

Pinning to a tag or sha is recommended in the docs, and `ref: main` is left
legal — an organisation that wants its policy changes to take effect everywhere
immediately is making a deliberate choice.

## Loading and layering

Policies are written as `package blastdoor`, whichever layer they come from —
an author should not have to know their file's weight.

At load time each layer's modules are parsed and their package path rewritten
to `data.layers.<name>`, so every layer can be queried on its own. This is
verified to work with OPA 1.19: `ast.ParseModule`, set `Package.Path`, pass as
`rego.ParsedModule`.

For each layer, blastdoor prepares the same three queries it does today, at
that layer's path:

```
data.layers.<name>.allow
data.layers.<name>.review
data.layers.<name>.deny
```

Only `.rego` is loaded, exactly as now: a policy repository's fixtures are not
part of the evaluation.

## Resolving a verdict

For one resource change:

1. Ask every layer for its verdicts.
2. Within a layer, the most severe answer wins — the existing rule, unchanged.
3. Across layers, **the judgement from the highest-weight layer that judged it
   at all decides**. Lower layers do not contribute to the verdict.
4. A change no layer judged is denied, computed in Go from the plan, exactly as
   today. That does not move: it must not become something a policy author can
   switch off.

A layer that is silent about a change is not an `allow`. Silence falls through
to the next layer down, which is what makes a tier able to add rules without
having to restate the ones below it.

The reasons from the deciding layer are what the note shows. Reasons from
overridden layers are recorded in `report.json` under the layer that gave them,
so the override is auditable, but they do not clutter the summary.

## Variables

Same rule, applied per key. `variables` may be set by any layer — a `.yml`
beside the policies in a remote source, and the `variables:` key in
`.blastdoor.yml` for the local one — and the highest-weight layer setting a key
wins that key.

Merging is per key, not per document: a domain setting `max_partitions` does
not erase a company setting of `retention_days`.

The merged result is mounted at `data.variables`, as now, so policies are
unchanged.

## The note and the report

The summary names the layers, their refs and the resolved shas:

```
## Terraform Blastdoor

👀 **Review required** — 1 change(s) need a person to approve, 2 passed.

Judged by: local (.), domain (@a1b2c3d), global (@a1b2c3d)
```

A verdict that came from a layer overriding a lower one is marked in the table,
because "this passed" and "this passed because the repository overrode the
company rule" are different facts and a reviewer needs the second one.

`report.json` gains a `layers` array — name, repository, ref, resolved sha,
weight — and each change records which layer decided it.

## What changes in the code

| Area | Change |
|---|---|
| `internal/config` | `policies` map, replacing the `policy` key; validation for unique weights |
| `internal/fetch` (new) | Clone at ref into a cache, return a directory and a resolved sha |
| `internal/policy` | Per-layer package rewriting, per-layer queries, weighted resolution |
| `internal/report` | Layer provenance in the summary and in `report.json` |
| `AGENTS.md` | The "most severe wins" entry is no longer true across layers and must be rewritten, not quietly left |
| `docs/hardening.md` | A section on what tiers mean for self-approval |

## Testing

- Weighted resolution: a local `allow` over a global `deny`; a global `deny`
  where local is silent; three layers where the middle one decides.
- Within one layer, most-severe still wins.
- A change no layer judges is still denied.
- Duplicate weights are an error naming both layers.
- A config still using the removed `policy` key fails, naming it.
- `--policy` replaces the layer set entirely.
- A source that cannot be fetched fails the command, and does **not** evaluate
  with the remaining layers.
- Variables merge per key across layers, highest weight winning each key.
- Resolved shas reach `report.json`.
- `repository: .` is read in place and never fetched.

## Migration

The `policy` key is removed rather than deprecated. A repository using it moves
to a one-layer `policies` map:

```yaml
policies:
  local:
    repository: .
    directory: policy
    weight: 0
```

Nothing silently changes behaviour: the strict-key check rejects the old key
and names it, so the failure is a message rather than a policy set that turned
out to be empty.

## Open questions

- **Should a layer be able to mark a rule as not-overridable?** Real tiering
  systems grow this need — a company rule that is genuinely non-negotiable,
  regardless of weight. It is deliberately not in this design, which keeps the
  model to one sentence. If it is added later it should be an explicit marker
  on the rule, not a second weight system.
- **Caching between pipelines.** Fetching every layer on every job is a network
  round trip per layer. The cache directory can live under the workspace so CI
  can cache it, the way the toolchain cache does.
