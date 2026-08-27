# Hardening

Blastdoor judges plans. It is not, on its own, a security boundary. Everything
below is about the gap between "the verdict was right" and "the change could
not get in anyway".

Read this before relying on the gate for anything that matters.

## What blastdoor enforces itself

- A change no rule matches is **denied**, decided by blastdoor from the plan
  itself rather than by a policy rule that has to fire.
- Within a layer, verdicts never weaken by addition: when several rules match
  one change the most severe wins, so **deleting** a rule makes a change *worse*
  (denied, for want of a rule), never better. Across layers this is deliberately
  not true — see below.
- A `deny` also fails the job, so approving alone does not clear it.
- A document that is not plan JSON is an error, never a plan with nothing to judge.
- `blastdoor plan` wipes its output directory first, so a `plan.json` committed
  to the repository cannot be passed off as a plan this pipeline produced.
- Raising the gate withdraws blastdoor's own earlier approval, so an approval
  earned by a safe push does not carry over to a riskier one.
- `--guard-path` forces review when the change edits the paths that judge it.

That last one matters most. A merge request that adds

```rego
package blastdoor
allow contains {"resource": rc.address, "reason": "trust me"} if {
	some rc in input.resource_changes
}
```

to its own policy directory passes everything. A bypass has to say "allow" in
as many words, which is at least glaring in a diff — but it is still a bypass.
The GitLab template guards `.blastdoor.yml`, `policy/` and `.gitlab-ci.yml` by
default. **If you wire the commands up yourself, pass `--guard-path` or you
have no gate.**

### Layered policies let a repository override its company's rules

`policies` in `.blastdoor.yml` orders policy sources by weight, and the
highest-weight layer that judges a change decides it. A repository's own layer
at weight 99 can `allow` what the company layer at weight 0 `deny`s.

That is the point of tiering, and it means **a shared policy is only as binding
as the guards around the layer list.** Three things have to hold together:

- `.blastdoor.yml` is self-guarded, so adding a layer, or moving a weight,
  forces a person to review that commit.
- **The local policy directory must be in `--guard-path`.** Otherwise a merge
  request adds a permissive local rule and nothing forces anyone to look at it.
  This is the one that is easy to forget, and it is the whole containment.
- The note names the deciding layer and what it overrode, so a reviewer reading
  `pass` can see it was a repository overriding a company `deny` rather than
  the company rules agreeing.

If you need a company rule that no repository may override, blastdoor has no
such marker today. Do not approximate it with weights.

### A pipeline that states no guards hands the list to the branch

`.blastdoor.yml` can carry a `guard:` list, and guards are an override rather
than a merge: the `--guard-path` flags win when given, and the config's list is
used when they are not. A pipeline that passes no `--guard-path` therefore lets
the repository being judged decide what is guarded — and the repository is the
merge request.

Two things keep this closed, and both have to hold:

- The template always passes `--guard-path`, so the config's list is never the
  one in force. Keep `BLASTDOOR_GUARD_PATHS` set.
- Blastdoor guards its own config file whenever it loads one, whatever the
  config says. That is outside the override rule, so a config naming no guards
  still cannot edit itself unseen.

Self-guarding catches an edit, not a deletion — a config that is gone cannot
ask to be guarded — which is why the template names `.blastdoor.yml`
explicitly as well.

## What blastdoor cannot enforce

### The pipeline definition is attacker-controlled

GitLab runs the `.gitlab-ci.yml` from the merge request's own branch. A change
can delete the blastdoor jobs, point `BLASTDOOR_IMAGE` at an image that always
passes, or repoint `BLASTDOOR_POLICY_DIR` at a permissive directory. Guarding
the path catches an *edit* — it cannot catch a *deletion*, because the job that
would report it no longer exists.

Fix this outside the repository:

- Put the jobs in a **compliance pipeline** (GitLab Ultimate), which the
  project's own `.gitlab-ci.yml` cannot override. This is the only real fix.
- Otherwise: protect the branch, require a pipeline to succeed, and keep the
  policies in a **separate repository** that the change's authors cannot write
  to — pull them in at job time rather than reading `policy/` from the repo
  under test.

### Planning runs the change's code

`terragrunt init` fetches modules the merge request chose, and `plan` executes
providers, `data "external"` programs and any plan-time hook. Whoever opens the
merge request runs code on your runner, with the runner's Vault, Consul and
Kafka credentials.

Version resolution is a second way in, which is why `blastdoor prepare` runs
mise with `MISE_SAFE=1`: a repository's own `mise.toml` can otherwise run hooks,
tasks and `exec()` while its tool versions are being worked out, before any
plan starts.

A gate bypass is the smaller problem here: an attacker who can plan can read
those credentials directly. Run the plan job on an isolated runner with
narrowly scoped, short-lived credentials, and treat plan-time as untrusted
execution.

### GitLab settings the gate depends on

First there has to be a rule at all: `gate` writes one only when it is passed
`--approval-rule`. Without it a `review` verdict posts a summary and nothing
else, and the change can merge unapproved. The GitLab template passes it; a
pipeline wired up by hand has to.

The rule is then only as strong as the project settings around it:

| Setting | Why |
|---|---|
| **Prevent editing approval rules** | Otherwise the author deletes the `blastdoor` rule and merges |
| **Remove all approvals when commits are pushed** | Otherwise a human approval of safe code survives a later risky push |
| **Require the pipeline to succeed** | A `deny` fails the job; without this the red pipeline does not block the merge |
| **Prevent approval by the author** | Otherwise the author approves their own high-risk change |
| Set `BLASTDOOR_APPROVER_GROUP_IDS` | An unscoped rule accepts *any* eligible approver, including the CI user |

Without the last one blastdoor warns, and the rule it writes can be satisfied
by whoever happens to be able to approve.

### The token is as powerful as the gate

`BLASTDOOR_GITLAB_TOKEN` can approve and merge. Anyone who can read it can do
both directly. Keep it masked and protected, scope it to the project, and
prefer a dedicated bot account over a person's token.

### Things a verdict cannot see

- **Salami slicing.** Verdicts are per change and per merge request. Ten
  separately-allowed grants still add up to something nobody approved as a
  whole. Write rules about the shape of a change, not only its size.
- **A rule wider than its reason.** An `allow` that matches more than the
  reason justifies is the main way changes slip through. Keep every match as
  narrow as the sentence next to it.
- **Detection gaps.** A unit is pulled in by a changed `.hcl`, `.tf`,
  `.tfvars`, `.tf.json` or `.tfvars.json` under it or an ancestor. A plan that
  depends on some other file — YAML read at Terragrunt evaluation time, a
  template — will not be replanned when only that file changes. Pass `--unit`
  explicitly where that is the case.

## A reasonable deployment

1. Policies in a separate, protected repository; fetch them in the eval job.
2. Jobs in a compliance pipeline, not the project's `.gitlab-ci.yml`.
3. `--guard-path` covering anything policy-adjacent that does stay in-repo.
4. `--approval-rule` passed by the gate job, and
   `BLASTDOOR_APPROVER_GROUP_IDS` set to a real group of people.
5. The four GitLab settings above turned on.
6. Plan job on an isolated runner with short-lived credentials.
7. `--auto-merge` off until you have watched the verdicts on real changes for a
   while.
