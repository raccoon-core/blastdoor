# Commands

| Command | What it does |
|---|---|
| `blastdoor detect` | Lists the units a change touches, from the git diff |
| `blastdoor prepare` | Installs the tool versions those units need |
| `blastdoor plan` | Runs init/plan/show for each unit, saving plan JSON |
| `blastdoor eval` | Judges plan JSON, writing `report.json`, `summary.md`, `blastdoor.env` |
| `blastdoor gate` | Posts the summary on a GitLab merge request and gates it |

`--help` on any of them has the flags.

## What counts as a unit

A unit is a directory with a `terragrunt.hcl` or `.tf` files. `detect` treats a
unit as affected when a `.hcl`, `.tf`, `.tfvars`, `.tf.json` or `.tfvars.json`
file changed in the unit *or in any parent directory*, matching how Terragrunt's
`find_in_parent_folders()` shares config — so editing one `component.hcl` plans
every environment under it.

!!! tip "Files that select no unit"

    A `topics.yaml` a unit reads, or a `.terragrunt-version` deciding the binary
    that applies everything below it, is not `.hcl` or `.tf` — so it selects no
    unit, is planned by nothing, and is judged by nothing. `--require-coverage`
    turns that into a `review` rather than letting it through unseen. See
    [`.blastdoor.yml`](configuration.md).

## Baseline drift

- `--baseline-ref` (`plan`) — plan each changed unit against this ref as well,
  and record what is still waiting to be applied there. Use the tip of the
  branch the change targets, `origin/main` for most repositories. Off when
  empty. Costs a second plan per unit that has changes.
- `--require-clean-baseline` (`eval`) — deny when a changed unit's baseline
  still has changes waiting to be applied. Needs `blastdoor plan --baseline-ref`
  to have run; a unit with changes and no baseline recorded denies too.
