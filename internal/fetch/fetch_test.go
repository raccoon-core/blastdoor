package fetch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// originRepo builds a real git repository to fetch from, on a branch and a tag.
func originRepo(t *testing.T) (dir, commit string) {
	t.Helper()
	dir = t.TempDir()

	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if err := os.MkdirAll(filepath.Join(dir, "policies"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "policies", "company.rego"), []byte("package blastdoor\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	run("add", ".")
	run("commit", "-qm", "policies")
	run("tag", "v1")

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, string(out[:40])
}

func TestGetChecksOutABranch(t *testing.T) {
	origin, commit := originRepo(t)
	f := Fetcher{CacheDir: t.TempDir()}

	got, err := f.Get(context.Background(), Source{Repository: origin, Ref: "main"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Commit != commit {
		t.Errorf("commit = %q, want %q", got.Commit, commit)
	}
	if _, err := os.Stat(filepath.Join(got.Dir, "policies", "company.rego")); err != nil {
		t.Errorf("the policy is not in the checkout: %v", err)
	}
}

// A tag and a commit sha both have to work: clone --branch rejects a sha,
// which is why this fetches by ref instead.
func TestGetChecksOutATagAndASha(t *testing.T) {
	origin, commit := originRepo(t)

	for _, ref := range []string{"v1", commit} {
		t.Run(ref, func(t *testing.T) {
			f := Fetcher{CacheDir: t.TempDir()}
			got, err := f.Get(context.Background(), Source{Repository: origin, Ref: ref})
			if err != nil {
				t.Fatalf("Get %s: %v", ref, err)
			}
			if got.Commit != commit {
				t.Errorf("commit = %q, want %q", got.Commit, commit)
			}
		})
	}
}

// Fetching twice into the same cache must not fail on the second run — the
// remote is already there.
func TestGetIsRepeatable(t *testing.T) {
	origin, _ := originRepo(t)
	f := Fetcher{CacheDir: t.TempDir()}

	first, err := f.Get(context.Background(), Source{Repository: origin, Ref: "main"})
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	second, err := f.Get(context.Background(), Source{Repository: origin, Ref: "main"})
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if first.Dir != second.Dir {
		t.Errorf("cache dir moved between runs: %q then %q", first.Dir, second.Dir)
	}
}

// An unreachable source is an error, never an empty directory that would
// evaluate as a layer with no rules.
func TestGetFailsOnAnUnreachableSource(t *testing.T) {
	f := Fetcher{CacheDir: t.TempDir()}

	if _, err := f.Get(context.Background(), Source{
		Repository: filepath.Join(t.TempDir(), "nope"),
		Ref:        "main",
	}); err == nil {
		t.Fatal("want an error for a repository that does not exist")
	}
}

// Different repositories and refs get different cache directories, so one
// cannot be served the other's checkout.
func TestCacheKeySeparatesSources(t *testing.T) {
	a := cacheKey(Source{Repository: "https://example.com/a", Ref: "main"})
	b := cacheKey(Source{Repository: "https://example.com/b", Ref: "main"})
	c := cacheKey(Source{Repository: "https://example.com/a", Ref: "v2"})

	if a == b || a == c || b == c {
		t.Errorf("cache keys collide: %q %q %q", a, b, c)
	}
}
