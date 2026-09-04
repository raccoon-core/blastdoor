# Contributing

Working on this with an AI agent? [AGENTS.md](AGENTS.md) covers the decisions
that look like accidents but are not.

## Getting set up

Go 1.27+ and Docker. Nothing else — the Go toolchain line in `go.mod` pulls the
right compiler on its own.

```console
make check   # what CI runs: fmt, vet, test
make build   # ./bin/blastdoor
make image   # local docker image
```

## Where things live

| Path | What |
|---|---|
| `cmd/blastdoor` | Entry point |
| `internal/cli` | One file per command |
| `internal/policy` | OPA evaluation and the three verdicts |
| `internal/report` | Folding verdicts into one, and the JSON/Markdown/dotenv output |
| `internal/detect` | Which units a git diff touches |
| `internal/runner` | Shelling out to tofu/terraform/terragrunt |
| `internal/gitlabapi` | The GitLab REST calls the gate needs |
| `examples` | Worked policy and plans, verified by `examples_test.go` |
| `ci/gitlab` | The template consumers include |

## Adding a command

Add `internal/cli/<name>.go` with a `new<Name>Cmd()` returning a
`*cobra.Command`, then register it in `NewRootCmd`. Keep the logic in an
`internal/` package so it can be tested without going through the CLI.

## Tests

Table-driven where it helps, real behaviour over mocks:

- `internal/detect` builds actual git repositories in `t.TempDir()`.
- `internal/gitlabapi` uses `httptest`, asserting on method, path and body.
- `internal/policy` compiles real Rego.
- `examples/examples_test.go` asserts the documented example verdicts.

If you change an example policy, update the table in `examples/README.md` and
the verdicts in `examples_test.go` — the test fails if you add a plan without
one.

## Changing the verdict model

Two invariants carry the whole tool, and both live in `internal/policy`:

- **A change no rule matches is sent to review, never passed.** `Evaluate`
  decides that from the plan itself, not from a policy rule that has to
  fire — a rule that does not run must not be the difference between judged
  and waved through unattended.
- **The most severe matching verdict wins** (`Worse`). This is what makes rules
  safe to add: a new one can only ever tighten the outcome, so nobody weakens
  the gate by contributing.

Be deliberate about anything that could let a change through unjudged, and add
a test that would fail if it did. There is no score and no threshold; if you
find yourself reaching for one, the question is probably which of the three
verdicts a change deserves.

## Writing rules that scale

Two different costs live in `internal/policy`, and they scale differently as
a policy repository grows:

- **Compiling a layer** happens once per `blastdoor eval` run, and its cost is
  the total size of that layer's `.rego` — every file under a configured
  `directory:` gets parsed and type-checked, whether or not it applies to
  this plan. `compileLayer` asks all three rule sets (`allow`/`review`/`deny`)
  in one combined query, so a layer's modules are compiled once rather than
  three times, but that only removes a constant factor — it does not make
  compile time independent of file count. The actual lever is scoping each
  layer's `directory:` to the resource types a pipeline provisions, rather
  than pointing every consumer at one monolithic `policies/`.
- **Evaluating a layer** happens once per unit, against that unit's whole
  plan. OPA indexes rule bodies that lead with a simple equality on an
  unnested reference (`rc.type == "kafka_topic"`, the shape every rule in
  `examples/policies/kafka.rego` already uses) so it can skip whole rule
  bodies that cannot match a given change, instead of evaluating every rule
  against every `resource_change`. Keep that equality first in a rule body;
  put helper predicates like `wildcard()` or `shrinks()` after it, not
  instead of it, or they lose the benefit of being skippable.

## Adding support for another forge

`internal/gitlabapi` is deliberately small and hand-rolled. A second forge
should be its own package with the same shape, chosen behind a flag in
`internal/cli/gate.go`. Only GitLab exists today.

## Commit messages

Releases come from the commits, so the subject line matters:

| Prefix | Effect |
|---|---|
| `fix:` | Patch release |
| `feat:` | Minor release |
| `feat!:`, or a `BREAKING CHANGE:` footer | Major release |
| `chore:`, `docs:`, `refactor:`, `test:`, `ci:` | No release |

```
feat(policy): deny replace actions on prod topics

fix(gate): treat 403 as fatal instead of skipping the gate
```

## Preview images

Opening a pull request publishes a throwaway image so you can run your change
in a real pipeline before merging:

```console
docker pull ghcr.io/raccoon-core/blastdoor:pr-42
```

To try it in a GitLab pipeline, point the jobs at it:

```yaml
variables:
  BLASTDOOR_IMAGE: ghcr.io/raccoon-core/blastdoor:pr-42
```

The tag moves as you push, and the exact reference is printed in the CI run
summary. `pr-image-cleanup.yml` deletes the image when the pull request closes.

Two limits worth knowing:

- **linux/amd64 only.** It is a preview, not a release; `release.yml` builds
  both architectures for the real thing.
- **A pull request from a fork gets no image.** Forked runs receive a read-only
  token and cannot write packages, so the publish step is skipped rather than
  failing the run.

## Releasing

Releases are automatic — do not tag by hand. Merge to `main` and
[.github/workflows/release.yml](.github/workflows/release.yml) does the rest:

1. **semantic-release** reads the commits since the last tag, works out the
   version, updates `CHANGELOG.md`, tags, and creates the GitHub Release.
2. If it released something, the publish job checks out that tag and:
   - **GoReleaser** attaches binaries and `checksums.txt` to the release
     (linux/darwin/windows × amd64/arm64), and
   - pushes `raccooncore/blastdoor` for linux/amd64 and linux/arm64, tagged
     `X.Y.Z`, `vX` and `latest`.

Publishing hangs off the same workflow run rather than a tag push on purpose: a
tag created with `GITHUB_TOKEN` does not start another workflow, so an
`on: push: tags` job would never fire.

Nothing releases when no commit warrants it — the publish job is skipped.

### What it needs

- `DOCKER_PAT` repository secret: a Docker Hub token for `raccooncore`.
- `main` must accept the changelog commit semantic-release pushes. If you
  protect the branch, give the release workflow (or a bot) permission to push
  to it, or drop `@semantic-release/changelog` and `@semantic-release/git`
  from [.releaserc.yml](.releaserc.yml) and keep the notes on the release only.

`GITHUB_TOKEN` is provided by Actions; no setup needed.
