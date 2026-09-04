# The three verdicts

| Verdict | Meaning | What the gate does |
|---|---|---|
| `pass` | A rule allows it | Approves, and with `--auto-merge` queues the merge |
| `review` | A rule wants a person, or no rule matched it | Posts the summary, and with `--approval-rule` requires a human approval |
| `deny` | A rule forbids it | The same **and** fails the job, so the pipeline is red |

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

## Sent to review by default

| | |
|---|---|
| A change no rule matches | `review`, reason "no policy judges this change" |
| A type with rules, in a shape none of them match | `review` |
| A judgement with no `resource` or no `reason` | an error — it cannot be attached or explained |
| Anything that isn't plan JSON | an error, never an empty plan that passes |
| A run with no units at all | the gate abstains — no approval, no merge |

A `no-op` is the only change needing no rule. These are pinned by
[`policy_test.go`](https://github.com/raccoon-core/blastdoor/blob/main/internal/policy/policy_test.go).

!!! warning "Guard the rules that judge the change"

    Verdicts are not the whole story: a change that can edit the policies
    judging it, or delete the job that runs them, does not need to beat them.
    Pass `--guard-path` (the GitLab template does by default) and read
    [Hardening](hardening.md) before relying on the gate.

## From verdict to deployment method

A verdict says whether a change may merge. It says nothing about whether the
result may be *applied* without somebody watching — that is a second,
per-environment question, and `eval` answers it by folding each environment's
verdict against a wish stated by the pipeline
(`--deployment-method-wish`/`BLASTDOOR_DEPLOYMENT_METHOD_WISH`, e.g.
`int=auto,stg=auto,prd=manual`; see [GitLab](gitlab.md#the-deployment-method)):

```
method(env) = none   iff  no unit in env changed          <- tested first
              auto   iff  wish(env)    == auto
                     and  verdict(env) == pass
                     and  no repository-wide condition forced manual
              manual otherwise
```

`verdict(env)` is the worst verdict among the units carrying that environment —
the same fold that decides the whole plan's verdict, one level up. Guarded
paths and uncovered files (`--require-coverage`) force `manual` in every
environment even when they would already have forced at least `review`: they
are named explicitly because a change that rewrites the rules judging it must
not be applied unattended anywhere, and that has to survive someone later
deciding a bare `review` should not block an unattended apply.

**`none` is tested first, and the order is load-bearing.** An environment with
no changed units has a vacuously passing verdict — there is nothing to deny —
so testing `auto` first would resolve every environment the change did not
touch to `auto`, and generate an apply job for it.

**The wish is a ceiling, not a default.** It states the best a pipeline is
willing to accept; nothing in the fold can turn a stated `manual` into an
unattended apply, only the reverse — a wish of `auto` tightening to `manual`
when the verdict, a guard, or a coverage gap says so. `method(env)` never
exceeds `wish(env)`.
