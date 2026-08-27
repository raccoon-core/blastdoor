# The three verdicts

| Verdict | Meaning | What the gate does |
|---|---|---|
| `pass` | A rule allows it | Approves, and with `--auto-merge` queues the merge |
| `review` | A rule wants a person | Posts the summary, and with `--approval-rule` requires a human approval |
| `deny` | A rule forbids it, or no rule matched it | The same **and** fails the job, so the pipeline is red |

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

## Denied by default

| | |
|---|---|
| A change no rule matches | `deny`, reason "no policy judges this change" |
| A type with rules, in a shape none of them match | `deny` |
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
