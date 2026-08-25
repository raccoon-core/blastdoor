package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/config"
)

// authoringDir is a directory with a config, a policy and a plan, and no git
// repository — someone writing a rule and trying it against a saved plan.
func authoringDir(t *testing.T, configBody string) string {
	t.Helper()
	dir := t.TempDir()

	write := func(name, body string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(config.FileName, configBody)
	write("policy/p.rego", `package blastdoor

allow contains {"resource": rc.address, "reason": "fine"} if {
	some rc in input.resource_changes
}
`)
	write("plan.json", `{"format_version":"1.2","resource_changes":[
		{"address":"kafka_topic.x","mode":"managed","type":"kafka_topic",
		 "change":{"actions":["create"],"after":{}}}]}`)
	return dir
}

// Having a config must not, on its own, demand a git diff. The config path is
// guarded implicitly, and a guard is a statement about a merge request — with
// no diff there is no merge request, nothing to gate, and nothing to guard.
func TestSelfGuardAloneDoesNotRequireADiff(t *testing.T) {
	t.Chdir(authoringDir(t, "policy:\n  - policy\nvariables:\n  max_partitions: 32\n"))
	configPath = ""
	prev := loaded
	t.Cleanup(func() { loaded = prev })

	if err := run(t, "eval", "--plan", "plan.json", "--out-dir", "out"); err != nil {
		t.Fatalf("eval should work while authoring policies: %v", err)
	}
}

// Asking for guards and not being able to check them is different: the caller
// wanted a guarantee blastdoor cannot give, so it says so rather than
// reporting a verdict that skipped the check.
func TestExplicitGuardStillRequiresADiff(t *testing.T) {
	t.Chdir(authoringDir(t, "policy:\n  - policy\n"))
	configPath = ""
	prev := loaded
	t.Cleanup(func() { loaded = prev })

	err := run(t, "eval", "--plan", "plan.json", "--out-dir", "out", "--guard-path", "policy")
	if err == nil {
		t.Fatal("want an error: --guard-path was asked for and cannot be checked")
	}
	if !strings.Contains(err.Error(), "diff") {
		t.Errorf("error should explain the missing diff, got: %v", err)
	}
}

// A guard list in the config is just as explicit as the flag: someone wrote
// it down, so failing to check it is still an error.
func TestGuardFromConfigStillRequiresADiff(t *testing.T) {
	t.Chdir(authoringDir(t, "policy:\n  - policy\nguard:\n  - policy\n"))
	configPath = ""
	prev := loaded
	t.Cleanup(func() { loaded = prev })

	if err := run(t, "eval", "--plan", "plan.json", "--out-dir", "out"); err == nil {
		t.Fatal("want an error: the config asked for a guard that cannot be checked")
	}
}
