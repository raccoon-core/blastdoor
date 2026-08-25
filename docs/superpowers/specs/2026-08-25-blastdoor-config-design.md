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

**An unhandled key rejects the whole file, and the command fails.** Not a
warning, not that key skipped, and above all not "carry on without the config":
a run with no config is a run with no guards and no ignore list, which is the
most dangerous way to respond to a file blastdoor did not fully understand. A
misspelled `ingore:` must stop the pipeline, not produce one that looks
configured and is not.

This also covers a config written for a newer blastdoor than the image running
it. The older binary does not know the key, cannot honour what it asks for, and
says so rather than judging the change by rules it only partly read.

### Deliberately not in the file

- **Credentials** (`--token`). Secrets belong in CI variables.
- **CI-provided facts** (`--api-url`, `--project-id`, `--branch`, `--base-ref`,
  `--head-ref`). The pipeline knows these; the repository does not.
- **Job wiring** (`--out-dir`, `--plan-dir`, `--units-file`, `--report`,
  `--summary`). These describe how jobs pass artifacts to each other, which is
  the template's business, not the repository's.

## Precedence

One rule for every setting, with no exceptions:

**the flag if it was given, otherwise the config, otherwise the default.**

The config is the repository's baseline — global, in the sense that it
describes the repository once. The pipeline is local to the run, and what it
states replaces the baseline rather than adding to it.

| Setting | Resolved from |
|---|---|
| `root`, `tool`, `manager`, `terragrunt_tf_path` | `--root`, `--tool`, `--manager`, `--terragrunt-tf-path` |
| `policy` | the `--policy` list, if any was given |
| `guard` | the `--guard-path` list, if any was given |
| `ignore` | the `--ignore-path` list, if any was given |
| `require_coverage` | `--require-coverage` |
| `approver_group_ids`, `rule_name`, `auto_merge`, `squash` | the matching gate flags |

A list is replaced whole, never merged. Half a guard list from the pipeline and
half from the repository would leave nobody able to say what is guarded.

"Given" means `cmd.Flags().Changed(name)`, not "differs from the default". A
flag left alone must not out-rank the config just because its default is
non-empty — `--root` defaults to `.`, and that default silently beating a
config that says `terraform` would make the file useless.

### What this costs, and what pays for it

Override means the repository's own file can state a **shorter** guard list
than the pipeline would have. That is not a hole in practice, for two reasons
that must both hold:

1. The pipeline always passes `--guard-path`, and flags win. A config's
   `guard:` is only ever consulted when the pipeline states none — which the
   template never does.
2. The config self-guards. Editing `.blastdoor.yml` to shorten its own guard
   list forces a review of that very edit.

The consequence for anyone wiring blastdoor up by hand is blunt, and belongs
next to the existing warning in [hardening.md](../../hardening.md): **a
pipeline that passes no `--guard-path` hands the guard list to the branch.**
The template's list is not a convenience, it is the control.

## Self-guarding, and the floor beneath it

**When a config file is loaded, its path is added to the guard paths, always.**
Not by the template — by the tool, and not optionally.

This is the one thing that is *not* subject to the precedence rule above. It is
not a setting either source states, so neither can replace it: the path is
appended to whatever guard list won. Without it the config is itself the
bypass — a merge request edits `.blastdoor.yml` to ignore the tree it is
changing, and nothing forces anyone to look at it. Since guards are otherwise
an override, this is the only guarantee that survives a config which names no
guards at all.

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
side. The floor holds because flags beat the config: a repository cannot shorten
a list the pipeline states.

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

Every one of these rejects the file and fails the command. None of them falls
back to defaults and continues, because continuing means running with no guards.

- Unparseable YAML: fail, naming the file and the line.
- Unknown key: fail, naming the key. The file is rejected as a whole, not
  loaded minus the offending key.
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
- Precedence, one rule, checked per setting: a flag beats the config for
  `root`, `policy`, `guard`, `ignore` and `require_coverage`.
- A guard list is **replaced**, not merged: a config naming two guards and a
  flag naming one leaves exactly the one.
- A flag left at its default does not beat the config.
- The config path is guarded whenever a config is loaded — including when the
  config names no guards, and when a `--guard-path` flag replaced its list.
- A deleted `.blastdoor.yml` still trips the template's floor guard.
- Unknown key, wrong type, unparseable YAML and a missing `--config` each fail
  with a message naming the cause, and none of them falls back to running
  unconfigured.
- No config file: every command behaves exactly as it does now.
- A `**/README.md` pattern in the config survives to the matcher intact — the
  regression this file exists to prevent.

## Consequences

The `for` loops and `set -f` in the templates go away, and with them the class
of bug where the shell rewrites a pattern before blastdoor sees it.

A second place exists where a guard list can come from, and the two do not
combine — the pipeline's list simply wins. That is the accepted cost of one
precedence rule a person can hold in their head, and of not breaking existing
pipelines. What keeps it safe is that the template always states its list, and
that the config always guards itself.

The corollary is worth repeating because it is the only sharp edge here: **a
pipeline that passes no `--guard-path` lets the repository decide what is
guarded.** That belongs in hardening.md alongside the existing note that a
pipeline which drops the blastdoor jobs entirely has no gate at all.
