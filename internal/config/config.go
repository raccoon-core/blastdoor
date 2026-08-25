// Package config reads a repository's own blastdoor settings.
//
// The settings describing a repository — where its units are, what judges
// them, what may go unplanned — used to travel as flags, which the GitLab
// template carried as space-separated environment variables. The shell read
// those before blastdoor did: an unquoted loop glob-expands a pattern like
// **/README.md into whatever paths happen to exist, so the pattern that was
// meant to be interpreted never arrived. A list in a file is a list.
//
// One file, in the directory blastdoor runs from. Deliberately no search
// upwards and no per-directory configuration: the configuration is
// attacker-controlled (see docs/hardening.md), and a file discovered by
// walking up lets a merge request disable the checks for the subtree it is
// changing, in something far less visible in a diff than .gitlab-ci.yml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the config blastdoor looks for in the working directory.
const FileName = ".blastdoor.yml"

// namedTypeErrors rewrites yaml's type errors to say which key was wrong.
//
// yaml reports "line 3: cannot unmarshal !!str into []string", which leaves
// the reader counting lines. The mistake this catches most often is a list
// written as a string — the exact shape the space-separated variables
// invited — so the message has to name the key that has to change.
func namedTypeErrors(raw []byte, typeErr *yaml.TypeError) string {
	keys := topLevelKeys(raw)

	lineRef := regexp.MustCompile(`^line (\d+): `)
	// yaml phrases an unknown key as "field x not found in type
	// config.Config", which tells a reader about blastdoor's Go types rather
	// than about their file.
	unknownField := regexp.MustCompile(`field (\S+) not found in type \S+`)

	out := make([]string, 0, len(typeErr.Errors))
	for _, msg := range typeErr.Errors {
		named := unknownField.MatchString(msg)
		msg = unknownField.ReplaceAllString(msg, `unknown key "$1"`)

		m := lineRef.FindStringSubmatch(msg)
		if m == nil {
			out = append(out, msg)
			continue
		}
		// An unknown key already says which key it is; prefixing it with the
		// same name again reads as a stutter.
		if named {
			out = append(out, lineRef.ReplaceAllString(msg, ""))
			continue
		}
		line, _ := strconv.Atoi(m[1])
		if key := keys.at(line); key != "" {
			out = append(out, fmt.Sprintf("%s: %s", key, lineRef.ReplaceAllString(msg, "")))
			continue
		}
		out = append(out, msg)
	}
	return strings.Join(out, "; ")
}

// keyRange is the span of lines one top-level key covers, its value included.
type keyRange struct {
	name       string
	from, thru int
}

type keyRanges []keyRange

func (k keyRanges) at(line int) string {
	for _, r := range k {
		if line >= r.from && line <= r.thru {
			return r.name
		}
	}
	return ""
}

// topLevelKeys maps each top-level key to the lines it spans, so an error
// reported against a value can be attributed to the key that owns it.
func topLevelKeys(raw []byte) keyRanges {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}

	var out keyRanges
	for i := 0; i+1 < len(root.Content); i += 2 {
		out = append(out, keyRange{name: root.Content[i].Value, from: root.Content[i].Line, thru: math.MaxInt})
	}
	// Each key runs until the next one starts.
	for i := 0; i+1 < len(out); i++ {
		out[i].thru = out[i+1].from - 1
	}
	return out
}

// Config is a repository's settings. Every field is optional; a zero value
// means "not stated", and the caller keeps whatever it would have used.
//
// The bools are pointers so that absent can be told from false. The squash
// flag defaults to true, and a config saying false has to be able to win.
type Config struct {
	Root             string `yaml:"root"`
	Tool             string `yaml:"tool"`
	Manager          string `yaml:"manager"`
	TerragruntTFPath string `yaml:"terragrunt_tf_path"`

	Policy          []string `yaml:"policy"`
	RequireCoverage *bool    `yaml:"require_coverage"`
	Guard           []string `yaml:"guard"`
	Ignore          []string `yaml:"ignore"`

	ApproverGroupIDs []int  `yaml:"approver_group_ids"`
	RuleName         string `yaml:"rule_name"`
	AutoMerge        *bool  `yaml:"auto_merge"`
	Squash           *bool  `yaml:"squash"`

	// Path is where this config was read from, empty when none was found.
	// Callers guard it: a config that can rewrite the rules judging a change
	// has to be looked at by a person when it changes.
	Path string `yaml:"-"`
}

// Find returns the config path to load, or "" when there is none.
//
// An explicitly named file must exist. Asking for a configuration and
// silently getting none is the failure this package exists to prevent, and it
// would run with no guards and no ignore list.
func Find(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("reading config %s: %w", explicit, err)
		}
		return explicit, nil
	}

	if _, err := os.Stat(FileName); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("reading config %s: %w", FileName, err)
	}
	return FileName, nil
}

// Load reads and validates a config file.
//
// Anything it cannot fully understand is rejected outright: not the offending
// key skipped, and not "carry on without the config", because a run with no
// config is a run with no guards. That also covers a config written for a
// newer blastdoor than the binary reading it — an unknown key means the file
// asks for something this build cannot honour, so it says so rather than
// judging a change by rules it only partly read.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		// An empty file decodes to EOF rather than an empty document.
		if errors.Is(err, io.EOF) {
			cfg.Path = path
			return &cfg, nil
		}
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			return nil, fmt.Errorf("reading config %s: %s", path, namedTypeErrors(raw, typeErr))
		}
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	cfg.Path = path
	return &cfg, nil
}
