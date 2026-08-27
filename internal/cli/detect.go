package cli

import (
	"fmt"

	"github.com/raccoon-core/blastdoor/internal/detect"
	"github.com/spf13/cobra"
)

func newDetectCmd() *cobra.Command {
	var opts detect.Options

	cmd := &cobra.Command{
		Use:   "detect",
		Short: "List the units a change touches",
		Long: `Prints one unit directory per line: every unit whose own directory, or any
ancestor directory up to --root, has a changed .hcl or .tf file. Shared files
such as component.hcl therefore pull in every unit beneath them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.Root = pickString(cmd, "root", opts.Root, cfg().Root)

			units, err := detect.Changed(opts)
			if err != nil {
				return err
			}
			for _, u := range units {
				fmt.Fprintln(cmd.OutOrStdout(), u)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&opts.Root, "root", ".", "directory to scan for units")
	cmd.Flags().StringVar(&opts.BaseRef, "base-ref", "", "git ref to diff from (default: the merge request base, else the merge base with the default branch)")
	cmd.Flags().StringVar(&opts.HeadRef, "head-ref", "HEAD", "git ref to diff to")
	cmd.Flags().StringVar(&opts.RepoDir, "repo-dir", "", "repository directory (default current directory)")

	return cmd
}
