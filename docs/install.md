# Install

```console
docker pull raccooncore/blastdoor
# or
go install github.com/raccoon-core/blastdoor/cmd/blastdoor@latest
```

Binaries for linux, macOS and Windows are attached to each
[release](https://github.com/raccoon-core/blastdoor/releases).

## What each one can do

The image bundles tenv and mise, so `prepare` and `plan` work out of the box. A
bare binary gives you `eval` and `gate`; `prepare` needs tenv or mise, and
`plan` needs tofu/terraform/terragrunt reachable one way or another.

| | Image | Bare binary |
|---|---|---|
| `detect` | ✅ | ✅ |
| `eval` | ✅ | ✅ |
| `gate` | ✅ | ✅ |
| `prepare` | ✅ | needs tenv or mise on `PATH` |
| `plan` | ✅ | needs tofu/terraform/terragrunt on `PATH` |

In a GitLab pipeline you rarely install it yourself — the
[ready-made jobs](gitlab.md) pull the image for you.
