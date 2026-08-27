# Gating

What the gate does to a merge request is opt-in, key by key. Nothing here is on
until asked for: blastdoor posts its summary and fails the job on `deny`
regardless, and a project that wants no more than that sets none of these.

```yaml
approval_rule: true          # on review and deny, require an approval
reviewers: true              # on review and deny, add the approver groups' members
approver_group_ids: [15685]  # who may approve; without it, any eligible approver can
auto_merge: true             # when everything passes, queue the merge
squash: true                 # squash when auto-merging (default)
```

`auto_merge` queues the merge once the pipeline succeeds, and **only when every
change passed**. A `review` or a `deny` never merges itself, which is also what
stops a merge request that trips a `guard` path, or that edits files no plan
covers, from going in unseen — both come back as `review`. A run that scored no
units merges nothing either: zero units is what a misconfigured `root` looks
like, and merging on the strength of having read nothing is the worst case.
These are pinned by
[`gate_test.go`](https://github.com/raccoon-core/blastdoor/blob/main/internal/cli/gate_test.go).

!!! tip "Turn it on in the config, not in a CI variable"

    Prefer turning it on here rather than with a CI variable. `.blastdoor.yml`
    is guarded, so the commit that enables auto-merge is itself reviewed by a
    person — which is the one review you would otherwise never get.
