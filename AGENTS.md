# AGENTS.md

Notes for AI agents working on blastdoor. [README.md](README.md) says what the
tool does; this file is about the things that are easy to get wrong, and the
decisions that look like accidents but are not.

Read [the load-bearing decisions](#load-bearing-decisions-do-not-quietly-undo)
before changing anything in `internal/policy`, `internal/report`, or
`.github/workflows/release.yml`.

## Build and test

```console
make check    # fmt, vet, test — run this before you call anything done
make build    # ./bin/blastdoor
make image    # local docker image
```

`go.mod` says `go 1.27.0`. If the machine has an older Go, the toolchain
downloads 1.27 automatically — **do not lower the `go` directive to match the
installed compiler.** That is not a fix, it is a downgrade.

Tests run real things: real git repositories in `t.TempDir()`, real Rego
compilation, `httptest` servers asserting on method, path and body. Keep it
that way. If you add behaviour, add a test that would fail without it.

## The model, in one paragraph

Every resource change in a plan gets one verdict: `pass`, `review` or `deny`.
Policies are Rego in `package blastdoor` contributing to `allow`, `review` and
`deny` rule sets, each entry an object with `resource` and `reason`. The worst
verdict of any change decides the plan. A change no rule matches is denied.

## Load-bearing decisions, do not quietly undo

Each of these has bitten someone, or is the whole point of the tool.

### There is no score and no threshold

An earlier version scored changes 0–100 and compared a sum against a threshold.
It was removed on purpose: summing invented precision that did not exist, the
threshold was a magic number, and three unrelated changes could add past it.

**Do not reintroduce scores, weights or thresholds.** If a change seems to need
one, the real question is which of the three verdicts it deserves.

### A change no rule matches is denied, and Go decides that

`policy.Evaluate` computes the unmatched set from the plan itself. It is
deliberately *not* a Rego rule, because a rule that fails to fire — or that
someone disables — must not be the difference between judged and waved through.

Keep that computation in Go. There is no "default policy" file any more, and
adding one back would move the tool's central guarantee into something a
policy author can switch off.

### The most severe matching verdict wins

`policy.Worse`. This is what makes rules safe to add: a new rule can only ever
tighten the outcome, never loosen it, so a contributor cannot weaken the gate
by adding to it. Deleting a rule makes a change *worse* (denied for want of a
rule), which is the right direction.

Anything that lets one rule override a more severe one breaks this.

### `deny` fails the job; `review` does not

`review` is a question for a person, and approving answers it. `deny` says this
should not happen, so approving alone does not settle it — the plan or the
policy has to change. That is why `gate` exits non-zero on `deny` and zero on
`review`. Both create the approval rule.

This asymmetry is intentional. Do not "simplify" them into one path.

### Fail closed, everywhere

- `policy.ValidatePlan` rejects anything that is not plan JSON. A truncated
  file, an error message, `{}`, or state output must never read as a plan with
  nothing in it. Do not relax this to "just skip files we cannot parse".
- `blastdoor plan` wipes its output directory first, so a `plan.json` committed
  to the repo cannot be passed off as one this pipeline produced.
- `gate` abstains when zero units were scored — no approval, no merge. Zero
  units is also what a misconfigured root looks like.
- A `--policy` path is searched recursively, and **only `.rego` files are
  loaded** (`policy.keepRego`). The OPA loader would otherwise read `.json` and
  `.yaml` under it as data documents, which makes a policy repository's own
  fixtures part of the evaluation: one malformed fixture fails it outright, and
  a `data.json` landing on `data.blastdoor` collides with the rule sets — a way
  to disable policies with a file that never looks like one. Do not widen this
  filter to "load the data files too" without answering that.
- `--policy` paths that contain no `.rego` are an error. Compiling an empty
  rule set would deny every change for want of a rule, which reads as a verdict
  on the plan rather than as the mistyped path it is.
- `--require-coverage` forces review for changed files that select no unit
  (`detect.Uncovered`). `affectsPlan` only accepts `.hcl`, `.tf` and `.tfvars`,
  so everything else — a `topics.yaml` a unit reads, a `.terragrunt-version`
  deciding the binary that applies every unit below it, a deleted unit — is
  planned by nothing and judged by nothing. `detect.Uncovered` deliberately
  reports files outside `--root` too: which paths may go unplanned is a fact
  about one repository's layout, so it belongs in that repository's
  `--ignore-path` list where it is visible, not in a silent rule here.
- A 401/403 from GitLab is fatal. A token that cannot reach the API must not be
  mistaken for a change that needs no gate.
- `gitlabapi.Unapprove` tolerates 404 and 401 (nothing to withdraw) but **not**
  403 (cannot act). That distinction is deliberate.
- Raising the gate calls `Unapprove` first, so an approval earned by an earlier
  safe push does not carry over to a worse one.

### The tool is resolved from the repository, never assumed

`runner.PinnedTool` walks up from the unit for `.terraform-version` /
`.opentofu-version`, because that is how tenv resolves and the file is usually
several directories above the unit. `runner.TerragruntTF` uses it to decide
which binary Terragrunt wraps.

Both matter: a Terragrunt unit short-circuits tool detection, so without this
a Terraform repository gets planned with OpenTofu — quietly, since Terragrunt
runs whatever `TG_TF_PATH` says. Do not reintroduce a hardcoded default here;
the fallback to OpenTofu applies only when nothing is pinned anywhere.

### The base ref is resolved, never assumed

`detect.ResolveBaseRef` tries `--base-ref`, then
`CI_MERGE_REQUEST_DIFF_BASE_SHA`, then the merge base with the default branch.
The branch case is the one people get wrong: `CI_COMMIT_SHA` is `HEAD`, so
using it as a base yields an empty diff, no units, and a pipeline that gates
nothing while looking green. `ChangedFiles` errors when the base resolves to
HEAD instead of reporting "no changes".

The diff uses three dots (`base...head`) so work that landed on the default
branch after the fork is not attributed to this change. Do not "simplify" it to
two dots.

### mise runs with MISE_SAFE=1

A repository's `mise.toml` can execute code during version resolution — hooks,
tasks, `_.source`, `exec()` in templates. Blastdoor resolves versions from
configuration written by whoever opened the merge request, which is exactly the
case mise's safe mode exists for.

`runner.miseEnv` sets `MISE_SAFE=1`, and the image sets it too. Do not remove
either, and do not add a flag to turn it off.

### `--guard-path` is the only thing stopping self-approval

Policies usually live in the repository they gate, so a merge request can
rewrite the rules judging it in the same commit. `--guard-path` forces at least
`review` when the change touches those paths, and `RequireReview` never softens
a `deny`.

The GitLab template sets `BLASTDOOR_GUARD_PATHS` by default. Do not remove that
default. See [docs/hardening.md](docs/hardening.md) for what the tool still
cannot defend against.

### release.yml must not be turned into a tag trigger

A tag pushed by `GITHUB_TOKEN` does not start another workflow. An
`on: push: tags` publish job would therefore **never fire** — the release looks
green and nothing ships. Publishing hangs off the same run as semantic-release
via `needs`, gated on its `new_release_published` output.

This looks like something worth simplifying. It is not.

## Rego gotchas

OPA 1.x parses Rego v1, so policies need the modern keywords:

```rego
# right
allow contains {"resource": rc.address, "reason": "..."} if {
	some rc in input.resource_changes
}

# wrong — v0 syntax, fails to compile
allow[{"resource": rc.address}] {
	rc := input.resource_changes[_]
}
```

Every judgement needs both `resource` and `reason`. No resource and it cannot
be attached to a change; no reason and the reviewer has nothing to read. Both
are errors rather than warnings, by design.

## Layout

| Path | What |
|---|---|
| `cmd/blastdoor` | Entry point |
| `internal/cli` | One file per command: detect, prepare, plan, eval, gate |
| `internal/policy` | Rego evaluation, verdicts, plan validation |
| `internal/report` | Folding verdicts into one, JSON/Markdown/dotenv output |
| `internal/detect` | Which units a git diff touches |
| `internal/runner` | Shelling out to tofu/terraform/terragrunt; `toolchain.go` picks tenv or mise |
| `internal/gitlabapi` | The GitLab REST calls the gate needs |
| `examples` | Worked policies and plans, verified by `examples_test.go` |
| `ci/gitlab` | The template consumers include |
| `docs/hardening.md` | Threat model, and what the tool cannot enforce |

Dependencies are `opa` and `cobra`, on purpose. The GitLab client is hand-rolled
because it needs six endpoints. Think before adding a third dependency.

## When you change an example policy

`examples/examples_test.go` asserts the verdict of every plan in
`examples/plans/`, and fails if a plan has no expected verdict. Update both it
and the table in `examples/README.md`. The test exists so the docs cannot drift.

## Commits

Conventional Commits, because releases are generated from them:

- `fix:` → patch, `feat:` → minor, `feat!:` or `BREAKING CHANGE:` → major
- `chore:`, `docs:`, `refactor:`, `test:`, `ci:` → no release

Do not tag by hand; merging to `main` releases. Do not commit or push unless
you were asked to.

## Working style that suits this repo

- **Verify rather than assert.** Much of this tool was built by running the
  attack and watching it succeed before fixing it. `blastdoor eval --plan
  <fixture> --policy <dir>` is fast; use it.
- **Be adversarial about the gate.** When you touch anything in the path from
  plan to verdict to merge request, ask how someone opening a merge request
  would get around it, and test that.
- Report honestly what you did and did not verify. Several guarantees here rest
  on external behaviour (GitLab settings, runner isolation) that the test suite
  cannot check.
