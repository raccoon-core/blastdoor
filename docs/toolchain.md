# Terraform, OpenTofu, Terragrunt

Which version runs is a version manager's job, and the image ships both:

| Manager | Reads | Used when |
|---|---|---|
| [tenv](https://github.com/tofuutils/tenv) | `.opentofu-version`, `.terraform-version`, `.terragrunt-version`, `terragrunt.hcl` constraints | The default |
| [mise](https://mise.jdx.dev) | `mise.toml`, `.tool-versions` | The unit or an ancestor is a mise project |

`blastdoor prepare` installs those versions before anything is planned. Run it
in the same job as `plan` — as its own step, so a toolchain that will not
install does not look like a plan that will not run. `--manager` overrides the
choice (`auto`, `tenv`, `mise`, `none`); `none` uses whatever is on `PATH`.

!!! warning "mise runs with `MISE_SAFE=1`"

    That stops a repository's own `mise.toml` executing code — hooks, tasks,
    `[env]` injection, `exec()` in templates — while resolving versions.
    Blastdoor judges merge requests it does not trust, so this is on by default
    and should stay on.

## Which binary plans a unit

`--tool` picks the binary; the default `auto` uses Terragrunt for a Terragrunt
unit, and otherwise whichever of OpenTofu/Terraform the repository pins.

Terragrunt drives one of the two, and blastdoor works out which from the same
pins — the nearest `.terraform-version` or `.opentofu-version` at or above the
unit, so a file at the repository root still governs a unit several directories
down. For a mise project it asks mise instead. With nothing pinned it uses
OpenTofu. Every plan logs which it chose and what decided it; override with
`--terragrunt-tf-path`.

Whichever it lands on is recorded beside the plan and titles the summary, so
the note on a merge request says **Terraform Blastdoor** or **OpenTofu
Blastdoor** without anyone opening the job log. A repository part-way between
the two gets both. The plan JSON cannot answer this on its own — it carries a
`terraform_version` key whichever tool wrote it.
