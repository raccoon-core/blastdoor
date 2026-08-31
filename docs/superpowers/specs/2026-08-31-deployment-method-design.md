# Deployment method per environment

Status: proposed
Date: 2026-08-31

## What this is for

Blastdoor answers one question today: may this merge request merge? It says
nothing about what happens afterwards. A repository that plans `int`, `stg` and
`prd` gets one verdict across all three, and the decision to apply — unattended,
or with somebody watching — is made by hand in the pipeline, the same way for
every change.

This design adds a second question, asked per environment: **may this be applied
unattended?** The pipeline states a wish (`int=auto stg=auto prd=manual`), the
verdict may tighten it, and the answer reaches GitLab as a per-environment
`auto` or `manual`.

The wish is a **ceiling, not a default**. Nothing here can turn a `manual` wish
into an unattended apply. Every mechanism below can only move an environment
towards `manual`.

## The constraint that shapes the output

The obvious implementation — write `BLASTDOOR_DEPLOY_PRD=manual` into
`artifacts:reports:dotenv` and read it from the apply job's `when:` — does not
work, for two reasons that compound:

- `when:` does not accept a variable. `when: $BLASTDOOR_DEPLOY_PRD` is a
  template error ([gitlab#31974], open since 2017).
- The documented workaround is `rules:`, which *can* set `when:` conditionally
  — but rules are evaluated at **pipeline creation**, before any job runs, and
  GitLab states plainly that dotenv variables are unavailable in `rules`
  ([dotenv_variables], [gitlab#235812]).

A dotenv variable produced by `blastdoor:eval` is therefore visible only in
later jobs' *scripts*. It cannot decide whether a job is manual.

So the decision is delivered **twice, in two forms**:

- `blastdoor.env` — the machine-readable statement of what was decided. Read by
  humans in job logs, by Part B, and by anything else that wants it.
- `apply.gitlab-ci.yml` — a generated child pipeline whose jobs carry a
  **literal** `when: on_success` or `when: manual`, triggered via
  `trigger: include: artifact:`.

The dotenv is the record. The generated YAML is the mechanism. Neither is
redundant, and they are produced from the same fold so they cannot disagree.

[gitlab#31974]: https://gitlab.com/gitlab-org/gitlab/-/issues/31974
[gitlab#235812]: https://gitlab.com/gitlab-org/gitlab/-/issues/235812
[dotenv_variables]: https://docs.gitlab.com/ci/variables/dotenv_variables/

## What this does not change

**No Rego changes. No new rule set.** The verdict a policy already produces is
what tightens the wish. `allow`, `review` and `deny` keep their present meaning
and their present three-way shape, and a policy author writes nothing new.

This was not the first design. A fourth `manual` rule set — "this may merge, but
apply it by hand" — was specified and then cut; [why](#the-manual-rule-set-considered-and-cut)
is recorded at the end, along with what would justify adding it.

---

# Part A — the decision

Ships on its own. Everything below is on the merge request side, and produces
artefacts nothing yet consumes; the apply jobs arrive in Part B.

## The environment dimension

Blastdoor has no concept of an environment today. The `int`/`stg`/`prd` split
lives entirely in the consuming template's shell:

```sh
grep -E "(^|/)${ENV}(/|$)" units.all.txt > "units.${ENV}.txt"
```

`blastdoor plan` gains `--environment <name>`. It writes `environment.txt`
beside each `plan.json`, and `eval` reads it back per unit:

```
.blastdoor/
  ops/kafka/int/topics/
    plan.json
    engine.txt         # exists today
    environment.txt    # new: "int"
```

**Per unit, not per run.** This is the same decision `engine.txt` already
records, for the same reason: `eval` runs in a different job from `plan`, plans
are split across a `parallel:matrix`, and their artifacts are merged. One file
per unit merges cleanly; one file per run collides, and the surviving copy is
whichever leg finished last.

`report.Unit` gains `Environment string`.

The `grep` stays in the template. Teaching `detect` to map paths to
environments is a separate change with its own failure modes, and this design
does not need it — the plan job already knows which environment it is planning,
because the matrix told it.

### A unit with no environment

`environment.txt` missing, or `--environment` not passed, while a wish is
stated, is an **error**. The unit would otherwise be applied by a job nobody
configured, or silently skipped — and "silently skipped" is indistinguishable
from "applied fine" in every artefact downstream.

When no wish is stated at all, the whole feature is off and a missing
`environment.txt` is silence, not an error. This keeps every existing consumer
working untouched.

## The wish

```
--deployment-method-wish int=auto,stg=auto,prd=manual
```

defaulting from `BLASTDOOR_DEPLOYMENT_METHOD_WISH`. Comma-separated, matching
the `BLASTDOOR_APPROVER_GROUP_IDS` / `splitList` precedent — and deliberately
not space-separated, which is the shape the package doc of `internal/config`
records as the bug that motivated the config file in the first place.

**The wish is not readable from `.blastdoor.yml`.** This needs no code: the
config decoder runs with `KnownFields(true)`, so an `environments:` key rejects
the whole file and fails the command.

The reason is the one `gate` already applies to approver groups — *"a branch
naming its own approver group is a branch approving itself."* A branch declaring
`prd=auto` is a branch arranging its own unattended production apply. The
pipeline states the wish, and the pipeline's statement is the only one.

A project that needs a different wish from the shared template overrides the
variable in its own `.gitlab-ci.yml`, which is itself in `BLASTDOOR_GUARD_PATHS`
— so changing it forces review of the commit that changed it.

Rules:

- An environment named in the wish with no changed units is fine; it resolves to
  `none`.
- A unit whose environment is named in **no** wish is an error, per above.
- An environment name that is not a valid dotenv key after uppercasing
  (`[A-Za-z_][A-Za-z0-9_]*`) is rejected at eval time, rather than emitting a
  dotenv that GitLab will fail to parse or, worse, parse partially.
- No wish at all: the feature is off. No new dotenv keys, no generated YAML, no
  change in behaviour.

## The fold

```
method(env) = none   iff  no unit in env changed          <- tested first
              auto   iff  wish(env)    == auto
                     and  verdict(env) == pass
                     and  no repository-wide condition forced manual
              manual otherwise
```

`verdict(env)` is the worst verdict among the units carrying that environment —
the same `policy.Worse` fold `report.Build` already does per unit, one level up.

**`none` is tested first, and the order is load-bearing.** An environment with
no changed units has a vacuously passing verdict, so testing `auto` first would
resolve every untouched environment to `auto` and generate an apply job for it.

**Repository-wide conditions force `manual` in every environment:**

- **Guarded paths.** A change that edits the rules judging it, or the pipeline
  running them, cannot be applied unattended anywhere. The rules it would be
  applied under are the rules it just rewrote.
- **Uncovered files** (`--require-coverage`). A file no plan accounts for is a
  file no policy judged. Blastdoor genuinely does not know what it does, and
  "unknown" is not a thing to apply without looking.

Both already force at least `review`, so `verdict(env) == pass` would catch them
anyway. They are named explicitly because they must survive someone later
deciding that a `review` verdict alone should not block an unattended apply.

**Ordering matters.** The fold runs *after* `RequireReview` and
`RequireCoverage`, or guards will not be reflected in it. This is a real
ordering dependency in `eval`'s `RunE` and is worth a comment at the call site.

### Why `none` and not `auto`

An environment with nothing to apply is not an environment it is safe to apply
automatically — it is an environment with no apply job at all. Emitting `auto`
would be true but useless, and would generate a job that runs the repository's
apply script against an empty unit list. `none` says the honest thing, and no
job is generated.

This widens the enum from `auto|manual` to `auto|manual|none`.

## Output

### `blastdoor.env`

```
BLASTDOOR_VERDICT=review
BLASTDOOR_UNIT_COUNT=7
BLASTDOOR_PASS_COUNT=5
BLASTDOOR_REVIEW_COUNT=2
BLASTDOOR_DENY_COUNT=0
BLASTDOOR_DEPLOY_INT=auto
BLASTDOOR_DEPLOY_STG=manual
BLASTDOOR_DEPLOY_PRD=none
```

### `report.json`

```go
// Method is how an environment's changes reach the infrastructure.
type Method string

const (
	Auto   Method = "auto"
	Manual Method = "manual"
	None   Method = "none"
)

// EnvDecision is one environment's answer, and why.
type EnvDecision struct {
	Name      string         `json:"name"`
	Wish      Method         `json:"wish"`
	Method    Method         `json:"method"`
	Verdict   policy.Verdict `json:"verdict"`
	UnitCount int            `json:"unit_count"`
	// Reasons says why this is not auto, most specific first. Empty when it is.
	Reasons []string `json:"reasons,omitempty"`
}
```

`Report` gains `Environments []EnvDecision`, **in the order the wish named
them** — not sorted. `int, stg, prd` is a promotion order and reads as one;
sorting alphabetically gives `int, prd, stg`, which puts production in the
middle of the table and invites a reader to skim past it. The order is
deterministic either way, because the wish is a single ordered string from the
pipeline.

`Reasons` is load-bearing, not decoration. "prd is manual" and "prd is manual
because a topic is being deleted in `ops/kafka/prd/topics`" are different facts,
and the second is the one that tells a reader whether the pipeline is working or
misconfigured. It is the same argument `overrideNote` already makes.

### `apply.gitlab-ci.yml`

Blastdoor generates the `when:`. It does **not** generate the apply command —
it has no way to know a repository's image, credentials or whether it applies a
saved plan file or re-plans.

```yaml
# generated by blastdoor; do not edit
include:
  - local: .gitlab/blastdoor-apply.yml

apply:int:
  extends: .blastdoor:apply
  when: on_success
  variables:
    BLASTDOOR_ENV: int

apply:prd:
  extends: .blastdoor:apply
  when: manual
  variables:
    BLASTDOOR_ENV: prd
```

The repository defines `.blastdoor:apply` — image, credentials, script — in the
included file. `--apply-include <path>` names it, defaulting to
`.gitlab/blastdoor-apply.yml`.

**That path must be in the guard list.** It is the script that applies
infrastructure; a merge request that can rewrite it unreviewed can run anything
in a job holding production credentials. The template's default
`BLASTDOOR_GUARD_PATHS` must include it, and every place guards are documented
must say so. This ranks with "the local policy directory must be guarded" in
severity.

An environment resolving to `none` gets no job.

### The summary note

A table above the existing verdict table:

```
| Environment | Apply | Why |
|---|---|---|
| int | ✅ auto | |
| stg | ✋ manual | review required: changing an existing topic |
| prd | — none | no unit changed |
```

A reviewer sees what the apply will do **before** approving the merge, which is
the point at which the information is worth anything.

---

# Part B — applying on the default branch

Part A decides on the merge request. The apply happens on the default branch,
after the merge, in a pipeline that does not exist in the template today.

Part B splits at a natural seam, and **the halves ship separately**. B1 gets you
a correct decision on main and is nearly free. B2 adds tamper detection and
costs considerably more.

## B1 — re-derive on main

The main pipeline runs `plan` → `eval` → `trigger apply`, exactly as the merge
request pipeline does, and derives the decision from its own diff. Nothing
crosses the merge; there is nothing to carry and nothing to trust.

This is correct on its own. A change that was `review` on the merge request is
`review` again on main, so it lands as `manual`. The verdict cannot come out
looser than the merge request's unless the underlying state changed, and if the
state changed then main's answer is the more accurate one.

### The base ref on main

`detect.ResolveBaseRef` falls back to the merge base with the default branch,
which on the default branch *is* `HEAD` — so `ChangedFiles` errors, correctly.
The main pipeline must pass `--base-ref $CI_COMMIT_BEFORE_SHA`.

That variable is all-zeros on a branch's first pipeline. Blastdoor rejects an
all-zeros base ref with a message saying so, rather than resolving it to
something arbitrary and diffing against the wrong thing.

## B2 — verify against what the merge request recorded

Main **also** reads back what the merge request recorded, and if the two
disagree, the apply stops.

Nothing that crossed the merge is trusted to be correct; it is only trusted to
be a claim worth checking against. This is the strictest of the options
considered, and it was chosen deliberately.

**The cost, recorded so nobody is surprised by it.** Legitimate state drift
between the merge request's plan and main's plan will produce disagreements that
are not tampering — usually main being *stricter* (`auto` → `manual`,
`pass` → `review`) because something changed underneath. Under this rule those
stop the apply too. If that proves noisy once B1 is running, the narrowing to
consider is *fail only when main is looser than the merge request said, and
otherwise take the stricter of the two* — always safe, since the stricter answer
is never the dangerous one. Shipping B1 first is what produces the evidence for
that call.

### Recording the decision

`blastdoor gate` already posts a note. It appends a machine-readable block,
invisible in the rendered note:

```html
<!-- blastdoor:decision
{"version":1,"head":"<sha>","verdict":"pass",
 "environments":{"int":"auto","stg":"manual","prd":"none"}}
-->
```

### Reading it back

`blastdoor verify-decision --report .blastdoor/report.json --commit $CI_COMMIT_SHA`

1. `GET /projects/:id/repository/commits/:sha/merge_requests` — find the merge
   request the commit came from.
2. `GET /user` — the token's own user id.
3. `GET /projects/:id/merge_requests/:iid/notes` — take the **latest note
   authored by that user id** and parse its decision block.
4. Compare against the freshly derived `report.json`. Any difference in any
   environment's method, or in the overall verdict, fails the command.

**Why the author filter is the security boundary.** Anyone who can comment can
post a note containing a forged decision block. Nobody but the token's own user
can post a note *authored by* the token's own user. Filtering by author id is
what makes the read-back worth performing at all.

Its limits, for `docs/hardening.md`:

- **A token shared between blastdoor and the merge request author defeats it
  entirely.** Blastdoor's token must belong to a bot account nobody opens merge
  requests with. This must be stated in the setup documentation, because a
  project that gets it wrong looks identical to one that got it right.
- A project maintainer can delete the note. That is handled by failing closed
  (below), not prevented.
- The block is trusted only as far as "blastdoor said this once". It is compared,
  never applied.

### Edge cases

- **Squash merges change the SHA.** Main's commit is not the merge request's
  head. The comparison is therefore on decision *content*, never on SHA equality;
  `head` is recorded so the failure message can name what was reviewed.
- **No merge request found, or no decision note** — refuse to apply. A commit
  pushed straight to the default branch has been reviewed by nobody.
  `--allow-missing-decision` exists for repositories that do not gate on merge
  requests, and is off by default.

### Job order on main

```
plan (per env) → eval → verify-decision → trigger apply
```

`verify-decision` sits between `eval` and the trigger, and the trigger `needs:`
it, so a mismatch stops the child pipeline from being created at all rather than
creating it and failing inside it.

---

## The `manual` rule set, considered and cut

The first version of this design added a fourth Rego rule set beside `allow`,
`review` and `deny`:

```rego
manual contains {"resource": rc.address, "reason": "..."} if { ... }
```

meaning "this may merge, but a person starts the apply". It was cut. The
reasoning is recorded because the idea is an appealing one and will occur to
somebody again.

**It serves a case that does not exist yet.** A `manual` rule only earns its
keep for a change that is simultaneously fine to merge, in an environment whose
wish is already `auto`, and still wants somebody watching the apply. Every
change the existing policies treat as risky is already `review`, which already
forces `manual`; and production is already `manual` by wish. What remains is
"an ACL change in *int* should be hand-applied", which nobody wants — they would
review it instead.

**It cost the most subtle thing in the design.** Verdicts resolve
highest-weight-layer-wins, so a local layer can overrule a company layer. A
`manual` rule set could not work that way: if a high-weight local layer could
overrule `manual` back to `auto`, a repository would be granting itself
unattended production applies, which is the self-approval hole `--guard-path`
exists to close, reopened on the far side of the merge where no reviewer is
watching. So `manual` had to union across layers while verdicts override — an
asymmetry contradicting the resolution rule three files away, needing a test
whose only purpose was to stop a future contributor "fixing" it.

That is a large permanent complication in exchange for speculative capability.

**Adding it later is safe.** `manual` can only tighten, so introducing it to a
running system cannot loosen a decision already being made. The trigger to
revisit: someone wants a specific change type hand-applied *in an environment
whose wish is `auto`*. Until that request is real, the wish plus the verdict
covers what is actually being asked for.

## Also deliberately not in this design

- **Moving the environment `grep` into Go.** The plan job already knows its
  environment. Path-to-environment mapping is a separate change.
- **Blastdoor generating the apply command.** It generates `when:`; the
  repository owns what runs. Whether that re-plans or applies a saved plan file
  is the repository's decision, and the generated job passes `BLASTDOOR_ENV` so
  either works.
- **A wish per unit.** Environments are the granularity asked for. Per-unit
  wishes would need a config file to express, which is exactly where the wish
  must not live.
- **Loosening.** No mechanism here turns `manual` into `auto`. If that is ever
  wanted, it is a new design with its own threat model, not a flag on this one.

## Testing

Following the repository's existing practice — real git repositories in
`t.TempDir()`, `httptest` asserting on method, path and body:

**Part A**

- The ceiling holds: `wish=manual` with an all-pass plan still yields `manual`.
- `wish=auto` with a `review` verdict in that environment yields `manual`.
- Guarded paths and uncovered files force `manual` in every environment.
- An environment with no changed units yields `none` and generates no job —
  including when its wish is `auto` and every other environment passed, which is
  the case a wrongly ordered fold gets wrong.
- Environments appear in the order the wish named them, not alphabetically.
- A unit with no `environment.txt` errors when a wish is stated, and is silent
  when none is.
- An environment name that is not a valid dotenv key is rejected at eval time.
- The generated YAML parses as YAML and carries literal `when:` values.
- The dotenv and the generated YAML never disagree — both derive from one fold,
  and a test asserts it over a table of cases.
- No wish stated: byte-identical output to today.

**Part B**

- B1: an all-zeros base ref is rejected with a message naming the variable.
- B2: agreement passes; a differing method fails; a decision note authored by
  anyone but the token's own user is **ignored**, not trusted; a missing note
  fails unless `--allow-missing-decision`.

## Files touched

**Part A**

| Path | Change |
|---|---|
| `internal/report/report.go` | `Method`, `EnvDecision`, `Report.Environments`, the fold, dotenv keys, summary table |
| `internal/report/apply.go` | new: generate `apply.gitlab-ci.yml` |
| `internal/cli/plan.go` | `--environment`, writes `environment.txt` |
| `internal/cli/eval.go` | `--deployment-method-wish`, `--apply-include`; fold after guards |
| `ci/gitlab/blastdoor.yml` | wish variable, trigger job, guard the apply include |
| `docs/` | `gitlab.md`, `hardening.md`, `verdicts.md` |
| `AGENTS.md` | why the wish is not config; why the fold order is load-bearing |

**Part B1**

| Path | Change |
|---|---|
| `internal/detect/detect.go` | reject an all-zeros base ref |
| `ci/gitlab/blastdoor.yml` | default-branch jobs |

**Part B2**

| Path | Change |
|---|---|
| `internal/cli/gate.go` | append the decision block to the note |
| `internal/cli/verify.go` | new: `verify-decision` |
| `internal/gitlabapi/client.go` | `CurrentUser`, `MergeRequestsForCommit`, `Notes` |
| `docs/hardening.md` | the bot-token requirement |
