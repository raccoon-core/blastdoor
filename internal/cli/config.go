package cli

import (
	"strconv"

	"github.com/raccoon-core/blastdoor/internal/config"
	"github.com/spf13/cobra"
)

// loaded holds the config for the current invocation, resolved once in the
// root command so every subcommand sees the same one.
var loaded *config.Config

// configPath is the --config flag.
var configPath string

// loadConfig resolves and reads the config file, if there is one.
func loadConfig() error {
	path, err := config.Find(configPath)
	if err != nil {
		return err
	}
	if path == "" {
		loaded = &config.Config{}
		return nil
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	loaded = cfg
	return nil
}

// cfg returns the loaded config, never nil.
func cfg() *config.Config {
	if loaded == nil {
		return &config.Config{}
	}
	return loaded
}

// One rule decides every setting: the flag if it was given, otherwise the
// config, otherwise the flag's own default.
//
// "Given" is Flags().Changed, not "differs from the default". A flag left
// alone must not out-rank the config because its default happens to be
// non-empty — --root defaults to ".", and that default beating a config that
// says "terraform" would make the file useless.

// pickString returns the flag when it was given, else the config value when
// it is set, else what the flag already holds.
func pickString(cmd *cobra.Command, flag, flagValue, configValue string) string {
	if cmd.Flags().Changed(flag) || configValue == "" {
		return flagValue
	}
	return configValue
}

// pickList returns the flag list when any was given, else the config list.
//
// A list is replaced whole, never merged. Half a guard list from the pipeline
// and half from the repository would leave nobody able to say what is guarded.
func pickList(cmd *cobra.Command, flag string, flagValue, configValue []string) []string {
	if cmd.Flags().Changed(flag) || configValue == nil {
		return flagValue
	}
	return configValue
}

// pickBool returns the flag when it was given, else the config when it is set.
func pickBool(cmd *cobra.Command, flag string, flagValue bool, configValue *bool) bool {
	if cmd.Flags().Changed(flag) || configValue == nil {
		return flagValue
	}
	return *configValue
}

// groupIDStrings renders config group ids the way the flag carries them.
//
// The config takes them as numbers because that is what they are; the flag is
// a string list because its default comes from a CI variable.
func groupIDStrings(ids []int) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.Itoa(id))
	}
	return out
}

// guardPathsFor resolves the guard list and appends the config's own path.
//
// Self-guarding sits outside the precedence rule above: it is not a setting
// either source states, so neither can replace it. The path is appended to
// whichever list won, an empty one included. Without it a config that names
// no guards would guard nothing — itself least of all — and a merge request
// could edit .blastdoor.yml to ignore the very tree it is changing.
//
// stated reports whether anyone asked for guards, as opposed to blastdoor
// adding its own config. It decides what happens when the guards cannot be
// checked: see requireGuards.
func guardPathsFor(cmd *cobra.Command, flagValue []string) (guards []string, stated bool) {
	guards = pickList(cmd, "guard-path", flagValue, cfg().Guard)
	stated = len(guards) > 0

	if path := cfg().Path; path != "" {
		guards = append(append([]string{}, guards...), path)
	}
	return guards, stated
}
