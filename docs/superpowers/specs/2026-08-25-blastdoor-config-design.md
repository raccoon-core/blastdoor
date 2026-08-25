# `.blastdoor.yml` — a repository's own configuration

Status: proposed
Date: 2026-08-25

## The problem

Every setting that describes a repository is passed to blastdoor as a flag,
and the GitLab template carries those flags as space-separated environment
variables:

```yaml
BLASTDOOR_GUARD_PATHS: "$BLASTDOOR_POLICY_DIR .gitlab-ci.yml"
BLASTDOOR_IGNORE_PATHS: "ansible roles **/README.md"
```

```sh
for p in $BLASTDOOR_IGNORE_PATHS; do ignores="$ignores --ignore-path $p"; done
```

Three problems come out of that shape.

**The shell reads the values before blastdoor does.** The loop above is
unquoted, so `**/README.md` is glob-expanded against the job's working
directory and reaches blastdoor as whatever paths happen to exist — in the
provisioning repository, `ansible/README.md policy/README.md`. The patterns
that were meant to be interpreted never arrive. This is fixed today with
`set -f`, which is a workaround for a list that was never a list.

**A local run cannot reproduce CI.** Judging a change by hand means restating
`--root`, `--policy`, `--guard-path`, `--ignore-path` and `--require-coverage`
consistently with the pipeline. They disagree quietly.

**`--root` is stated more than once.** The plan job and the eval job each pass
it, and nothing checks that they match. A repository is one shape; it should be
described once.

## What this is not

**Not per-directory configuration, and not a hierarchical merge.** A config
file discovered by walking up from each unit was considered and rejected.

[hardening.md](../../hardening.md) records that the configuration is
attacker-controlled: GitLab runs the `.gitlab-ci.yml` from the merge request's
own branch, and `--guard-path` is the mechanism that contains it — an edit to
the rules that judge a change forces a person to look. Per-directory discovery
reopens that in a worse form. A merge request adds one file several levels down
in `terraform/components/…` naming an ignore that covers its own subtree, and
the coverage check is off for exactly the directory being changed. Editing
`.gitlab-ci.yml` to achieve the same thing is glaring in a diff; a three-line
file four levels down is not.

It also destroys the report's legibility. "Which config produced this verdict?"
must have one answer, or the summary posted on a merge request cannot be
trusted by the person reading it.

The one thing that genuinely varies per directory — which tool and which
version a unit uses — is already resolved per unit from the files the ecosystem
already defines (`.terraform-version`, `.terragrunt-version`, `mise.toml`),
read by `blastdoor prepare`. There is no remaining gap for a per-directory
config to fill.

## The file

Name: `.blastdoor.yml`, YAML, in the directory blastdoor is run from — which
in every CI job is the root of the repository.

That is a deliberate distinction. Blastdoor looks in one place rather than
resolving the repository root itself, because "the config that applied" should
be answerable by looking at the working directory in the job log, without
knowing how the tool searches.

YAML because it is already in the module graph through OPA, so it costs no new
dependency, and because it is the format the settings live in today — the
migration is a copy, not a translation.

It must not live inside `.blastdoor/`. That directory is `--out-dir`, and
`blastdoor plan` calls `os.RemoveAll` on it before every run: configuration
placed there would be deleted by the job that needs it.

### Discovery

- `--config <path>` names the file explicitly.
- Otherwise `.blastdoor.yml` in the current working directory is used if it
  exists.
- No search upwards, and no search in any other directory. A file that is not
  where blastdoor looked does not silently take effect.
- No config file is not an error. Every setting keeps its current default, and
  blastdoor behaves exactly as it does today.
- `--config` naming a file that does not exist **is** an error. Asking for a
  configuration and silently getting none is the failure this whole document
  is about.

### Schema

Every key is optional, so an empty file is valid and changes nothing. Types are
fixed; a list is always a list.

```yaml
# What describes the repository
root: terraform                 # string  — where units are (detect, prepare, plan, eval)
tool: auto                      # string  — auto | tofu | terraform | terragrunt
manager: auto                   # string  — auto | tenv | mise | none
terragrunt_tf_path: auto        # string  — auto | tofu | terraform

# What judges it
policy:                         # []string — directories or .rego files
  - policy
  - .policy-common
require_coverage: true          # bool
guard:                          # []string — paths that force review when changed
  - policy
  - .gitlab-ci.yml
ignore:                         # []string — paths allowed to go unplanned
  - ansible
  - "**/README.md"

# What the gate does
approver_group_ids: [1234]      # []int
rule_name: blastdoor            # string
auto_merge: false               # bool
squash: true                    # bool
```

Unknown keys are an error, not a warning. A misspelled `ingore:` that is
quietly ignored produces a pipeline that looks configured and is not.

### Deliberately not in the file

- **Credentials** (`--token`). Secrets belong in CI variables.
- **CI-provided facts** (`--api-url`, `--project-id`, `--branch`, `--base-ref`,
  `--head-ref`). The pipeline knows these; the repository does not.
- **Job wiring** (`--out-dir`, `--plan-dir`, `--units-file`, `--report`,
  `--summary`). These describe how jobs pass artifacts to each other, which is
  the template's business, not the repository's.

## Precedence

Flags win — with one exception, which is the point of the whole mechanism.

Settings split into two classes.

**Restrictions accumulate. Neither source can weaken the other.**

| Setting | Rule |
|---|---|
| `guard` | union of the config list and the `--guard-path` flags |
| `require_coverage` | on if either the config or the flag turns it on |

A guard is a restriction. If a flag list replaced the config list, a pipeline
that passes `--guard-path .gitlab-ci.yml` would silently drop the config's
guard on `policy/`, and the config could not be trusted to mean anything. The
same reasoning applies in reverse, so the answer is a union: both sources may
add, neither may remove.

**Everything else: the flag wins when it was actually given.**

| Setting | Rule |
|---|---|
| `root`, `tool`, `manager`, `terragrunt_tf_path` | flag if set, else config, else default |
| `policy` | flag list if any `--policy` was given, else config |
| `ignore` | flag list if any `--ignore-path` was given, else config |
| `approver_group_ids`, `rule_name`, `auto_merge`, `squash` | flag if set, else config, else default |

"Actually given" means `cmd.Flags().Changed(name)`, not "differs from the
default". A flag left alone must not out-rank the config just because its
default is non-empty — `--root` defaults to `.`, and that default silently
beating a config that says `terraform` would make the file useless.

`ignore` relaxes rather than restricts, which is why it is override and not
union: a pipeline that states an ignore list gets exactly that list, and a
branch cannot widen it by adding entries to the config.

## Self-guarding, and the floor beneath it

**When a config file is loaded, its path is added to the guard paths, always.**
Not by the template — by the tool, and not optionally. Without it the config is
itself the bypass: a merge request edits `.blastdoor.yml` to ignore the tree it
is changing, and nothing forces anyone to look at it.

Self-guarding catches an **edit**. It cannot catch a **deletion**: a config
that is gone cannot ask to be guarded, and blastdoor then loads no config and
knows of no guard. hardening.md already documents this shape for the pipeline
definition itself.

So the GitLab template keeps an explicit floor:

```yaml
BLASTDOOR_GUARD_PATHS: ".blastdoor.yml $BLASTDOOR_POLICY_DIR .gitlab-ci.yml"
```

`--guard-path` matches a path that changed, and a deletion is a change, so a
merge request that removes `.blastdoor.yml` trips the guard from the pipeline
side. Since guards are a union, this floor cannot be weakened by the config.

## Per-command behaviour

| Command | Reads |
|---|---|
| `detect` | `root` |
| `prepare` | `root`, `tool`, `manager`, `terragrunt_tf_path` |
| `plan` | `root`, `tool`, `manager`, `terragrunt_tf_path` |
| `eval` | `root`, `policy`, `require_coverage`, `guard`, `ignore` |
| `gate` | `approver_group_ids`, `rule_name`, `auto_merge`, `squash` |

Loading is one call in the root command's `PersistentPreRunE`, so every
subcommand sees the same resolved configuration and there is one place where
precedence is applied.

## Errors

- Unparseable YAML: fail, naming the file and the line.
- Unknown key: fail, naming the key.
- Wrong type (`ignore: "ansible roles"` instead of a list): fail, saying which
  key and what was expected. This is the exact mistake the string-splitting
  variables invite, so it must be caught loudly rather than coerced.
- `--config` pointing at a missing file: fail.
- A missing default `.blastdoor.yml`: not an error.

## Migration

Both mechanisms keep working. The environment variables in the template are
unchanged and continue to override, so no existing pipeline changes behaviour
when it upgrades.

The template gains `.blastdoor.yml` in `BLASTDOOR_GUARD_PATHS` (the deletion
floor), and its documentation recommends the config file for new repositories.

The provisioning repository migrates in a separate change: the guard, ignore,
policy, root and coverage settings move out of `.gitlab-ci.yml` and into
`.blastdoor.yml`, and the `set -f` workaround and both `for` loops disappear
with them. The variables it stops setting are the ones the config now carries.

## Testing

Real files in `t.TempDir()`, matching how the repository already tests.

- Each key loads and reaches the command that consumes it.
- Precedence, per class: a flag beats the config for `root` and `ignore`; a
  flag and a config **combine** for `guard`; `require_coverage` turns on from
  either side alone.
- A flag left at its default does not beat the config.
- The config path is guarded whenever a config is loaded.
- A deleted `.blastdoor.yml` still trips the template's floor guard.
- Unknown key, wrong type, unparseable YAML and a missing `--config` each fail
  with a message naming the cause.
- No config file: every command behaves exactly as it does now.
- A `**/README.md` pattern in the config survives to the matcher intact — the
  regression this file exists to prevent.

## Consequences

The `for` loops and `set -f` in the templates go away, and with them the class
of bug where the shell rewrites a pattern before blastdoor sees it.

A second place exists where a guard list can come from. That ambiguity is the
accepted cost of not breaking existing pipelines; the union rule makes it safe
in the direction that matters, since neither source can remove a restriction
the other added.
