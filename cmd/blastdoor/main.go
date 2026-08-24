// Command blastdoor scores Terraform/OpenTofu plans against OPA policies and
// gates merge requests on the result.
package main

import (
	"os"

	"github.com/raccoon-core/blastdoor/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
