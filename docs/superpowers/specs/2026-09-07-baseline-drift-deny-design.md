# Denying on an unapplied baseline

Status: proposed
Date: 2026-09-07

## What this is for

Blastdoor detects which *units* a merge request touches, then plans them. A plan
is desired-state against live-state, not "what this merge request changed", so
anything already pending on that unit rides along in the note and, after merge,
in the apply.

The case that prompted this, on SCP's provisioning repository:

- `b8e3290 fix(kafka_g2): adjust scp.example.v1 retention` merged to `main`,
  changing `retention.ms` for a topic in all three environments.
- `topic-grow-retention.rego` allows it with
  `{"int": "auto", "stg": "auto", "prd": "manual"}`. int and stg applied
  themselves; prd needed a person to press apply, and nobody did.
- MR !176 (`fix(kafka_g2): add v2 topic`) then touched the same units. Its diff
  adds `example.topic.v2` and nothing else. Its note listed **four** changes —
  the three topic creations, plus the `scp.example.v1` retention update in prd
  that appears in no diff anywhere.

Nothing in the tool could tell the reviewer that the fourth row was not theirs.
`base-ref` is used in exactly one place — choosing which units to plan
(`detect.Changed`, from `cli/plan.go` and `cli/eval.go`) — and never to attribute
an individual `resource_change` to this merge request rather than to a backlog.

MR !176 got lucky: the retention rule says prd is `manual`, so prd degraded to
manual and a person stayed in the loop. Had the leftover been a change that rule
allows to auto-apply in prd, !176 would have applied a change that appeared in
nobody's diff, under an approval given for something else.

## The rule

**If the target branch has changes waiting to be applied, deny.**

Not review. `review` is a question for a person, and approving answers it.
Approving MR !176 does nothing whatsoever about the prd backlog — the only thing
that settles it is applying or reverting on `main`. That is the definition of
`deny` in [verdicts.md](../../verdicts.md) and in AGENTS.md: *"approving alone
does not settle it — the plan or the policy has to change."*

Scoped to the units where it can actually do harm: **for each unit whose head
plan has changes, the baseline plan on the target branch must have none.**

The scope clause is load-bearing in two directions.

It is what stops the rule deadlocking. Out-of-band drift is sometimes fixed by
*codifying* the drifted value in HCL, and that merge request touches the dirty
unit. Under an unscoped rule it would be denied, and the only exit would be
applying `main` — reverting the very change you meant to keep. Scoped, such a
merge request has an empty head plan for that unit, needs no baseline, and
merges. No override path, no escape hatch, no variable anyone has to be trusted
with.

It is also still safe. An empty head plan means nothing is pending at head, so
the apply that follows the merge does nothing for that unit. There is nothing to
ride along with.

## What this deliberately does not add

**No escape hatch.** No CI/CD variable, no merge request label, no accept-list in
the policy layer. Considered and rejected: the backlog case always has an exit
that is not a merge request (press apply), and the drift case is handled by the
scope clause above. An override would be one more thing to audit in a tool whose
central claim is that it cannot be talked out of a verdict.

**No Rego.** This lives in Go, for the same reason the unmatched-change
computation does: a repository must not be able to write a rule that switches off
the check on its own backlog. See AGENTS.md, *"A change no rule matches is sent
to review, and Go decides that."*

**No per-change attribution.** An earlier draft classified every
`resource_change` as introduced-by-this-MR or pre-existing, by comparing the two
plans address by address. It is strictly more information, and it is not needed:
once a dirty baseline denies outright, nothing downstream has to reason about
which changes were whose. Dropped on YAGNI grounds.

**No cross-pipeline state.** The baseline is planned in the merge request's own
pipeline. A baseline published by the `main` pipeline as an artifact would be
nearly free per merge request, but it introduces a stored fact that can go stale
or missing, and every consumer of it then needs a fail-closed path. Blastdoor
derives its facts; keep it that way. Revisit only if plan time becomes the
binding constraint.

## Producing the baseline

`blastdoor plan` gains `--baseline-ref`. Empty is the off switch: no worktree, no
sidecars, no behaviour change for existing consumers.

When set, the worktree is created once, eagerly, before any unit is planned —
not lazily on the first unit whose head plan turns out to have applicable
changes. Then, after each unit's `plan.json` is written:

1. If that plan has no applicable changes, skip the unit. Nothing to ride along
   with, and this is where most of the per-unit cost is avoided.
2. Otherwise plan `<worktree>/<unit>` with the same `runner.Options`, against
   the worktree already checked out.
3. Write `<out-dir>/<unit>/baseline.json`.

The worktree is removed when the run ends.

Eager, not lazy, on purpose: an unresolvable ref is the shallow-clone
misconfiguration `NewWorktree` already guards against (see "Failing closed"),
and it should fail the run before any unit is planned, not after the first one
that happens to have changes. The trade-off is real and accepted: a change
whose every unit applies nothing now still pays for a worktree checkout on a
shallow clone where, planned lazily, it would never have needed one. Fast,
loud failure on a real misconfiguration was judged worth that cost.

Planning in the same job as the head plan, rather than a parallel
`blastdoor:baseline` job, is deliberate on two counts: that job already holds the
provider credentials and the installed toolchain, and two concurrent plans of one
unit would contend on the state lock.

### Where the sidecar goes, and why

`<out-dir>/<unit>/baseline.json`, beside `plan.json` — the same per-unit shape as
`engine.txt` and `environment.txt`, and for the same reason those are per unit
rather than per run: `eval` runs in a different job, and when plans are split
across a `parallel:matrix` their artifacts are merged. One file per unit merges.
One file per run collides, and the survivor is whichever leg finished last.

It records the baseline **commit**, not only the ref. A ref moves; a verdict
cannot be explained afterwards without knowing what it pointed at. Same reasoning
as `report.Layer.Commit`.

```json
{
  "ref": "origin/main",
  "commit": "2907a6899eda3dfb5a06647963bf5b5759eca6e6",
  "state": "dirty",
  "addresses": ["kafka_topic.topics[\"scp.example.v1\"]"]
}
```

`state` is one of `clean`, `dirty`, or `absent`. Absent is what a unit the merge
request *creates* records — it does not exist at the baseline, and that is clean,
not an error.

### Resolving the ref

`origin/$CI_MERGE_REQUEST_TARGET_BRANCH_NAME`, falling back to
`origin/$CI_DEFAULT_BRANCH`.

The target branch tip, **not** the merge base — and this is the one place the new
flag deliberately disagrees with `detect.ResolveBaseRef`. Three-dot merge-base is
right for "which units does this branch touch", because work that landed on the
default branch after the fork is not this branch's doing. It is wrong here: a
branch that is behind `main` would have a merge base predating the backlog, and
the baseline would come back clean while the backlog is still very much waiting.
The question this flag asks is "what is pending if this merge request does not
exist", and the tip is what answers it.

Same flag name, opposite correct answer. Worth a line in AGENTS.md so nobody
"fixes" one to match the other.

### What counts as changes to be applied

One predicate, used both for "the head plan has changes" in step 1 and for
`state: dirty` in step 3, so the two cannot disagree about what an empty plan is.

A change counts as applicable unless it is exactly `["no-op"]`, or exactly
`["read"]` on a resource whose `mode` is `data`.

The `mode` clause is not decoration, and it is why this cannot be the obvious
"anything other than `no-op` or `read`". The repository already draws this
distinction deliberately, and tests it: `examples/plans/data-source-read.json`
(`mode: "data"`, actions `["read"]`) passes, while
`examples/plans/managed-resource-read-lookalike.json` — the same action on a
*managed* resource — gets `review`, because a managed resource being read is not
a data lookup. Filtering all `read` actions here would let a unit whose only
pending change is a managed-resource read skip its baseline entirely. Reads on
data sources are inapplicable; everything else is applicable, including anything
unrecognised. Fail closed.

Fail closed also covers the shape of `actions` itself, not just its value: a
missing `change.actions`, an empty array, or one whose only entries are not
strings, is not either of the two named inapplicable shapes — both require
exactly one recognised action — so it reads as applicable, the same as an
unrecognised action does. A malformed `actions` must never read as "nothing to
do here."

Without the `no-op` clause a resource with a perpetual diff would deny every
merge request forever.

## Reaching the verdict

`blastdoor eval` gains `--require-clean-baseline`, mirroring `--require-coverage`
in both spelling and shape. It reads the sidecars and calls:

```go
func (r *Report) RequireCleanBaseline(dirty []BaselineUnit)
```

Same shape as `RequireReview` and `RequireCoverage`: record why, worsen the
verdict via `policy.Worse`, never soften. It worsens to `policy.Deny` where those
two worsen to `policy.Review`. The units land on a new `Report.Baseline` field so
`report.json` carries the whole story.

One consequence arrives without new code:

- `gate` already exits non-zero on `Deny`, and already calls `Unapprove` before
  raising the gate — so an approval earned by an earlier, green push is withdrawn
  rather than carried over onto a pipeline that now denies.

Stopping the auto-apply is not free, though, and needs its own line of code.
`RequireCleanBaseline` sets `r.Verdict` and `r.Baseline`; it does not touch any
`Unit.Verdict`. `Decide` computes each environment's method from a rollup of
*per-unit* verdicts, plus a `wide` slice of repository-wide facts it cannot get
from that rollup — `Guarded` and `Uncovered` are named there today. Without
`Baseline` named alongside them, a dirty baseline is invisible to `Decide`
entirely, and an environment whose units all pass an auto-vouching rule
resolves to `Auto` on a report whose own verdict is `Deny`. So `Decide` must
add `Baseline` to `wide` — this is not a side effect of anything already
written, it is a required line in this design.

### Failing closed

- Flag on, unit's head plan has changes, `baseline.json` missing → **deny**. A
  missing fact is not a clean one. This is the case where an artifact did not
  merge, or an older `blastdoor plan` wrote no sidecar.
- The worktree cannot be created, or the baseline plan errors → **fail the
  command**. Not "assume clean, carry on". Same rule as a policy source that
  cannot be fetched: a gate that gets more permissive when something breaks is
  not a gate.
- `--baseline-ref` unset → nothing is read, nothing is written, nothing changes.
  The flag is off in the binary; the template is what turns it on (below).

## The note

A new section alongside the guarded and uncovered blocks, in the same voice:

> `main` has changes that have not been applied yet, so this merge request cannot
> be approved on its own. Apply or revert them on `main` first.
>
> - `terraform/components/kafka/instances/g2/config/prd` —
>   `kafka_topic.topics["scp.example.v1"]` (at `2907a68`)

One existing line needs fixing alongside it. `verdictSentence` renders a deny as
*"N change(s) a policy does not allow"*, which reads as "0 change(s) a policy
does not allow" when the deny comes from the baseline rather than from a scored
change — directly above the list of what is actually wrong. `Review` already
carries exactly this special case for the same reason (a review forced by a
guarded path rather than by a scored change); `Deny` needs its counterpart.

## The CI template

`ci/gitlab/blastdoor.yml`:

- `blastdoor:plan` passes `--baseline-ref`, resolved as above:
  `origin/$CI_MERGE_REQUEST_TARGET_BRANCH_NAME` on a merge request pipeline, else
  `origin/$CI_DEFAULT_BRANCH`. Only when not on the default branch — on `main`
  the baseline would be `main` itself, which is both meaningless and, since
  `main`'s own plan is exactly the backlog, a guaranteed self-deny.
- `blastdoor:eval` passes `--require-clean-baseline`.

Both are gated behind `BLASTDOOR_BASELINE_ENABLED`, defaulting to on and
settable to `""` to stop the line without reverting a template bump — a repo that
discovers a permanently dirty unit needs that lever.

On by default is a behaviour change for existing template consumers, and it is
the right one on the same grounds as `BLASTDOOR_GUARD_PATHS`, which the template
also sets by default: a template that gates is the point of the template. The
flag stays off in the binary, so anyone driving blastdoor directly is unaffected.
This is a `feat!:` for the template.

## Consequences to accept

This is a stop-the-line rule. An unapplied `prd: manual` change parks every
subsequent merge request that touches that unit until a person applies it. For a
repository provisioned by Backstage and copier, where merge requests are opened
by automation and merged by nobody in particular, that means a queue of blocked
provisioning merge requests and no obvious owner to unblock them.

That is the intended trade — the alternative is what MR !176 did — but it makes
"who watches for a manual apply that never ran" a real operational question this
design does not answer. Worth an alert on `main`'s plan being non-empty,
separately from this work.

## Testing

Real git repositories in `t.TempDir()` for worktree creation and ref resolution;
fixture plan JSON for classification and the `eval` wiring. Per AGENTS.md, each
of these must fail without the change:

- Dirty baseline on a unit with a non-empty head plan → `deny`.
- Empty head plan, dirty baseline → baseline never planned, verdict unaffected.
  (The revert-merge-request case. This is the one that proves the deadlock relief
  is real.)
- Unit absent at the baseline → `absent`, treated as clean.
- Non-empty head plan, `baseline.json` missing, flag on → `deny`.
- `--baseline-ref` unset → no worktree, no sidecar, byte-identical behaviour.
- Baseline ref resolves to a branch tip, not to the merge base, when the branch
  is behind the target.
- Deny headline does not say "0 change(s)".
- A unit whose only pending change is `["read"]` on a **managed** resource is
  applicable, and does get a baseline; the same action on a `mode: "data"`
  resource does not.

**No `examples/` entry.** `examples_test.go` judges each plan in
`examples/plans/` through Rego and asserts the verdict. A baseline deny is a
report-level verdict that no policy produces, so it cannot be expressed as a
plan fixture there, and adding one would only assert what the policies say about
its contents. The predicate above is tested in `internal/policy`, and the deny in
`internal/report`.

## Decided during implementation: no refusal when `--baseline-ref` resolves to `HEAD`

Considered: whether `blastdoor plan` should refuse `--baseline-ref` when it
resolves to the same commit as `HEAD`, the way `ChangedFiles` refuses a base
ref equal to head. It is the same class of misconfiguration — a baseline that
is trivially clean, gating nothing while looking green — but on the default
branch it is the normal state rather than a mistake, so the check would have
to know which it is looking at.

Decided against a check in the binary. It stays a template-level concern: the
binary does not compare the resolved commit to `HEAD` at all, and
`ci/gitlab/blastdoor.yml`'s `blastdoor:plan` job avoids the case structurally,
with its `elif` guarding `--baseline-ref` behind "not on the default branch" —
on the default branch, `baseline_arg` is never set, so `--baseline-ref` is
never passed resolving to `HEAD` in the first place. A binary-level refusal
would have to rediscover which branch it is on to tell the mistake from the
normal case, duplicating a distinction the template already draws for free.
Revisit only if a consumer drives `blastdoor plan --baseline-ref` directly,
outside the template's guard.
