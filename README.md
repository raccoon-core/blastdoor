# Blastdoor

**Blastdoor** is a Terraform/OpenTofu plan checker that evaluates planned infrastructure changes against policies you write using Open Policy Agent (OPA). The tool categorizes changes as **pass**, **review**, or **deny**, specifying why each category is assigned.

**[Documentation →](https://raccoon-core.github.io/blastdoor/)**

### Why use Blastdoor?
- Automates plan evaluations to identify compliant and non-compliant changes.
- Provides clear, actionable output for reviewers.

### Quick Start:
1. **Docker**: Run Blastdoor via Docker using bundled example plans and policies.
2. **Installation**: Install via `docker pull` or `go install`. Pre-built binaries for various platforms are also available.

### Key Features:
- Flexible policy creation using Rego.
- Transparent verdicts (pass, review, deny).
- Tools for various infrastructures, like Terraform, OpenTofu, and Terragrunt.

---

## Install

```console
docker pull raccooncore/blastdoor
# or
go install github.com/raccoon-core/blastdoor/cmd/bl