// Package cli wires blastdoor's commands together.
package cli

import (
	"fmt"
	"os"
	"strconv"

	"github.com/raccoon-core/blastdoor/internal/config"
	"github.com/spf13/cobra"
)

// Version is stamped at build time with -ldflags.
var Version = "dev"

// NewRootCmd builds the blastdoor command tree.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "blastdoor",
		Short: "Judge Terraform/OpenTofu plans against OPA policies and gate merge requests",
		Long: `Blastdoor judges a Terraform/OpenTofu/Terragrunt plan against Rego policies
and decides whether a change can merge on its own or needs a human.

Every change comes back pass, review or deny, and the worst one decides the
plan. A change no policy matches is denied, so a plan touching something no
policy covers is never waved through.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
		// One place resolves the configuration, so every subcommand sees the
		// same one and precedence is applied in one way.
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			return loadConfig()
		},
	}

	root.PersistentFlags().StringVar(&configPath, "config", "",
		"config file to read (default "+config.FileName+" in the working directory, if present)")

	root.AddCommand(
		newDetectCmd(),
		newPrepareCmd(),
		newPlanCmd(),
		newEvalCmd(),
		newGateCmd(),
	)
	return root
}

// Execute runs the root command, reporting errors on stderr.
func Execute() int {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "blastdoor:", err)
		return 1
	}
	return 0
}

// envOr returns the environment variable, or fallback when it is unset.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envInt reads an int from the environment, falling back when unset or unparseable.
func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
