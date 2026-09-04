# Examples

A worked policy in [policies/kafka.rego](policies/kafka.rego), the smallest
possible one in [policies/data-reads.rego](policies/data-reads.rego), one plan
per scenario in [plans/](plans/), and every setting blastdoor understands in
[blastdoor.yml](blastdoor.yml). Every verdict below is asserted by
`examples_test.go`, so they cannot drift from the policy.

| Plan | Verdict | Why |
|---|---|---|
| `kafka-topic-create.json` | pass | Additive and easy to undo |
| `data-source-read.json` | pass | Reading a data source changes nothing |
| `no-op.json` | pass | Nothing changes, so nothing needs a rule |
| `kafka-topic-delete.json` | review | Destroys data — recoverable, but somebody should say so |
| `kafka-acl-wildcard.json` | **deny** | `User:*` on `*` grants unbounded access |
| `unmatched-resource.json` | review | No rule matches an `aws_s3_bucket` |
| `managed-resource-read-lookalike.json` | review | A *managed* resource, so the data-read rule does not apply |

The last two are the interesting ones. Nothing matches an `aws_s3_bucket`, so
it is sent to review for want of a rule rather than by one. And
`managed-resource-read-lookalike.json` has the same `["read"]` action as
`data-source-read.json` but `mode: "managed"` — the allow rule checks both, so
it does not match, and the change goes to review rather than passing.

## Try it

```console
blastdoor eval --plan plans/kafka-topic-delete.json --policy policies
```

## The config

[blastdoor.yml](blastdoor.yml) is a reference rather than a starting point: it
sets every key blastdoor understands, each with what it does and what it
defaults to, so you can see the whole surface in one place. Copy it to
`.blastdoor.yml` at the root of your repository and delete everything you do
not need — the config a reviewer can read is the one that only says what this
repository actually changes.

Two tests keep it honest. It is loaded through the real loader, so a layer
missing a weight fails here rather than in a pipeline; and its top-level keys
are compared against the config struct, so a setting added to blastdoor and
not to this file fails too. It does not run as it stands — the policy layers
name a host that does not exist, and a source that cannot be fetched fails the
command.

## The loop for writing a policy

1. Get a plan to work against: `tofu show -json tfplan > plan.json`, or copy one
   from `plans/` and edit it.
2. Write the rule, then run `blastdoor eval --plan plan.json --policy .`
3. Still denied? Nothing matched it. The summary prints the address and
   `no policy judges this change`, which is the rule you still owe.

## Choosing a verdict

Ask what should happen, not how bad it is:

| Ask | Verdict |
|---|---|
| Would I be happy for this to merge with nobody looking? | `allow` |
| Is this fine, but somebody should know and sign it off? | `review` |
| Should this never happen without changing the policy first? | `deny` |

`review` and `deny` differ in how they are cleared. A review is a question for a
person, and approving answers it. A denial says this should not happen — it is
cleared by changing the plan, or by changing the rule, not by approving.

Rules for the same change combine, and **the most severe wins**. So a broad
`review` plus a narrow `deny` for the dangerous case reads exactly as you would
hope, and adding a rule can never weaken an existing one.

## Allowing

`data-reads.rego` is the smallest complete example. Keep the match as narrow as
the reason justifying it: it checks both `mode == "data"` and
`actions == ["read"]`, so a managed resource with the same action is untouched
by it and stays denied.

Most modules read something, so without a rule like this every plan is denied
and the gate stops meaning anything. Copy it as-is if that fits.

## What no rule can do

- **Nothing weakens by addition.** The most severe matching verdict wins, so a
  new rule can only ever tighten the outcome.
- **A judgement must name a `resource` and a `reason.`** No resource and it
  cannot be attached to a change; no reason and the reviewer has nothing to
  read. Both are errors, not warnings.
- **A change no rule matches is denied**, and that is decided by blastdoor from
  the plan itself, not by a rule that has to fire.
- **Anything that isn't plan JSON is an error**, so a truncated file or state
  output can't look like a clean plan.
