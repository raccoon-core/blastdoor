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
