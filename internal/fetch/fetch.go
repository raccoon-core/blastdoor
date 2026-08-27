// Package fetch gets policy sources out of git repositories.
//
// Credentials come from the environment — a ~/.netrc, or a token in the URL —
// the same way a Terraform module source is fetched. Blastdoor does not grow
// a credential store of its own.
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Source is a repository and a ref to read it at.
type Source struct {
	Repository string
	Ref        string
}

// Result is where a source landed and what it actually was.
type Result struct {
	// Dir is the checkout on disk.
	Dir string
	// Commit is the sha the ref resolved to. A ref like "main" moves, so a
	// verdict cannot be explained after the fact without recording this.
	Commit string
}

// Fetcher checks sources out into a cache directory.
type Fetcher struct {
	// CacheDir holds the checkouts. One directory per repository and ref.
	CacheDir string
	// Log receives git's output.
	Log *os.File
}

// Get checks out one source, reusing the cache when the ref already resolves
// to the same commit.
//
// A failure here is returned, never swallowed. Evaluating with the layers that
// happened to fetch would drop a company layer's deny rules the moment its
// host was unreachable, and a gate that gets more permissive when the network
// fails is not a gate.
func (f Fetcher) Get(ctx context.Context, src Source) (Result, error) {
	dir := filepath.Join(f.CacheDir, cacheKey(src))

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("preparing the policy cache: %w", err)
	}

	if err := f.git(ctx, dir, "init", "--quiet"); err != nil {
		return Result{}, fmt.Errorf("preparing %s: %w", src.Repository, err)
	}
	// The remote is already there on the second run through this cache
	// directory, and saying so is not worth a line of output.
	_ = exec.CommandContext(ctx, "git", "-C", dir, "remote", "remove", "origin").Run()
	if err := f.git(ctx, dir, "remote", "add", "origin", src.Repository); err != nil {
		return Result{}, fmt.Errorf("pointing at %s: %w", src.Repository, err)
	}

	// fetch by ref rather than clone --branch, which rejects a commit sha.
	if err := f.git(ctx, dir, "fetch", "--depth", "1", "--quiet", "origin", src.Ref); err != nil {
		return Result{}, fmt.Errorf("fetching %s at %s: %w", src.Repository, src.Ref, err)
	}
	if err := f.git(ctx, dir, "checkout", "--quiet", "--force", "FETCH_HEAD"); err != nil {
		return Result{}, fmt.Errorf("checking out %s at %s: %w", src.Repository, src.Ref, err)
	}

	commit, err := f.output(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return Result{}, fmt.Errorf("reading the commit of %s: %w", src.Repository, err)
	}
	return Result{Dir: dir, Commit: commit}, nil
}

// cacheKey names a checkout directory for a repository and ref.
//
// Hashed rather than derived from the URL: a repository URL holds slashes, and
// may hold a token, which has no business being a path on disk.
func cacheKey(src Source) string {
	sum := sha256.Sum256([]byte(src.Repository + "\x00" + src.Ref))
	return hex.EncodeToString(sum[:])[:16]
}

func (f Fetcher) git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if f.Log != nil {
		cmd.Stderr = f.Log
	}
	return cmd.Run()
}

func (f Fetcher) output(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
