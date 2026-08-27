# .blastdoor.yml

Settings that describe a repository can live in a `.blastdoor.yml` beside it,
rather than being repeated as flags in every job:

```yaml
root: terraform
policies:
  local:
    repository: .
    directory: policy
    weight: 0
require_coverage: true
guard:
  - policy
  - .gitlab-ci.yml
ignore:
  - ansible
  - "**/README.md"
variables:
  max_partitions: 32
```

That is the useful half of it. Every key blastdoor understands, with what each
one does and defaults to, is in
[examples/blastdoor.yml](https://github.com/raccoon-core/blastdoor/blob/main/examples/blastdoor.yml).

Blastdoor reads it from the directory it runs in. There is no search upwards
and no per-directory config: the file that judges a change must be one a
reviewer can find.

`--config` names a different file, or `BLASTDOOR_CONFIG` does for a job that
cannot choose its working directory; the flag wins over the variable. A config
named either way has to exist — asking for one and silently getting none would
run with no guards and no ignore list.

## Precedence

One rule decides every setting: **the flag if it was given, otherwise the
config, otherwise the default.** Lists are replaced whole, never merged.

## Variables

`variables` is the exception in shape rather than precedence: its keys belong to
whoever wrote the policies, so it is the one place unknown names are not an
error. Policies read them as `data.variables`, which is what lets a shared rule
carry a default a repository can move:

```rego
default max_partitions := 10

max_partitions := data.variables.max_partitions if {
	data.variables.max_partitions
}
```

They are mounted at `data.variables` and never at the root, so a variable cannot
land on `data.blastdoor` and displace the rules themselves — the reason this
exists rather than letting the loader read `.json` out of a policy directory.
Nothing caps what a repository may set: the config guards itself, so raising a
limit forces a person to look at the commit that raised it.

## Two things that do not follow the precedence rule

Both on purpose:

- **A config file guards itself.** Its path is added to the guard list whatever
  the config says, so a merge request cannot quietly edit `.blastdoor.yml` to
  excuse the tree it is changing.
- **A key blastdoor does not understand rejects the whole file** and fails the
  command. Carrying on would mean running with no guards and no ignore list —
  and that is also what a config written for a newer blastdoor looks like.

Because guards are an override, a pipeline that passes no `--guard-path` lets
the repository decide what is guarded. The GitLab template always passes one;
see [Hardening](hardening.md) if you wire the commands up yourself.
