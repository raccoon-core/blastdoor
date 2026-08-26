package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/raccoon-core/blastdoor/internal/config"
	"github.com/raccoon-core/blastdoor/internal/fetch"
	"github.com/raccoon-core/blastdoor/internal/policy"
	"github.com/raccoon-core/blastdoor/internal/report"
)

// PolicyCacheDir is where remote policy layers are checked out. Under the
// working directory so CI can cache it the way it caches a toolchain.
const PolicyCacheDir = ".blastdoor-policies"

// resolveLayers turns the configured policy sources into layers on disk.
//
// Every source is fetched, and a failure to fetch any of them fails the
// command. Evaluating with the layers that happened to arrive would drop a
// company layer's deny rules whenever its host was briefly unreachable, and a
// gate that gets more permissive when the network fails is not a gate.
func resolveLayers(ctx context.Context, sources map[string]config.Source, log *os.File) ([]policy.Layer, []report.Layer, error) {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)

	fetcher := fetch.Fetcher{CacheDir: PolicyCacheDir, Log: log}

	var layers []policy.Layer
	var provenance []report.Layer

	for _, name := range names {
		src := sources[name]

		dir := "."
		commit := ""
		if !src.Local() {
			got, err := fetcher.Get(ctx, fetch.Source{Repository: src.Repository, Ref: src.Ref})
			if err != nil {
				return nil, nil, fmt.Errorf("policy layer %q: %w", name, err)
			}
			dir, commit = got.Dir, got.Commit
		}

		path := dir
		if src.Directory != "" {
			path = filepath.Join(dir, src.Directory)
		}

		layers = append(layers, policy.Layer{
			Name:   name,
			Weight: *src.Weight,
			Paths:  []string{path},
		})
		provenance = append(provenance, report.Layer{
			Name:       name,
			Repository: src.Repository,
			Ref:        src.Ref,
			Commit:     commit,
			Weight:     *src.Weight,
		})
	}

	// Report them the way they are resolved: highest weight first.
	sort.Slice(provenance, func(i, j int) bool { return provenance[i].Weight > provenance[j].Weight })
	return layers, provenance, nil
}
