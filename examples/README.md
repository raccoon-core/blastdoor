# Examples

A worked policy in [policies/kafka.rego](policies/kafka.rego) and one plan per
scenario in [plans/](plans/). Every score below is asserted by
`examples_test.go`, so they cannot drift from the policy.

| Plan | Score | Why |
|---|---|---|
| `kafka-topic-create.json` | 0 | Additive and easy to undo |
| `data-source-read.json` | 0 | Reading a data source changes nothing |
| `no-op.json` | 0 | Nothing changes |
| `kafka-topic-delete.json` | 80 | Destroys the topic's data |
| `kafka-acl-wildcard.json` | 90 | `User:*` on `*` grants far more than it looks like |
| `unclassified-resource.json` | 100 | No policy claims `aws_s3_bucket` |
| `managed-resource-read-lookalike.json` | 100 | A *managed* resource, so the data-read rule does not apply |

The last two are the interesting ones. Nothing green-flags an `aws_s3_bucket`,
so it scores the maximum. And `managed-resource-read-lookalike.json` has the
same `["read"]` action as `data-source-read.json` but `mode: "managed"` — the
green-flag rule checks both, so it does not match.

## Try it

```console
blastdoor eval --plan plans/kafka-topic-delete.json --policy policies
```

Add `--no-base-policy` to see only your own rules fire, which is useful when
working out why something scored 100.

## The loop for writing a policy

1. Get a plan to work against: `tofu show -json tfplan > plan.json`, or copy one
   from `plans/` and edit it.
2. Write the rule, then run `blastdoor eval --plan plan.json --policy .`
3. Score still 100? The change isn't in `classified` yet — the backstop is
   still counting it.

## The two halves of a rule

Scoring and claiming are separate on purpose:

```rego
deny contains {"msg": ..., "score": 80, "resource": rc.address} if { ... }
classified contains rc.address if { ... }
```

`deny` says how risky a change is. `classified` says "a rule has looked at
this". Without the second half, your score is *added to* the backstop's 100
rather than replacing it.

That split is what makes a gap in your rules visible: in `kafka.rego`,
`classified` only claims create/update/delete, so a Kafka topic *replace* — a
shape none of the rules score — still comes out at 100 rather than silently
scoring 0.

## Green-flagging

A score of 0 is how a policy says "this is fine". `data-reads.rego` is the
smallest complete example: score it 0, then claim it.

Keep the claim as narrow as the rule. `data-reads.rego` checks both
`mode == "data"` and `actions == ["read"]`, so it green-flags a data source
read and nothing else — a managed resource with the same action still scores
100. A claim broader than the rule that justifies it is how changes slip
through unscored.

Most modules read something, so without a rule like this every plan needs
review and the gate stops meaning anything. Copy it as-is if that fits.

## What can never be green-flagged

The backstop is not a policy you can outrank. It fires for anything no rule
claims, and:

- **A score can't be negative.** Scores add up, so a negative one would mask
  risk found elsewhere in the plan. Blastdoor rejects it.
- **A fraction rounds away from 0.** `0.6` becomes `1`, never the `0` that
  means allowed.
- **A finding with no `score` is 100**, not 0.
- **Anything that isn't plan JSON is an error**, not a score of 0 — so a
  truncated file or state output can't look like a clean plan.

`--no-base-policy` turns the backstop off. It exists for debugging your own
rules in isolation; nothing scores 100 by default once you pass it, so keep it
out of CI.

## Scoring scale

Scores sum across every change in the merge request, so blast radius adds up.
The default threshold is 50.

| Kind of change | Score |
|---|---|
| Additive, easily reversed | 0–10 |
| Widens access, but scoped | 20 |
| Reduces redundancy, deletes a user | 60 |
| Destroys data | 80 |
| Wildcard grant, shrinks a topic | 90 |
| Nothing classified it | 100 |
