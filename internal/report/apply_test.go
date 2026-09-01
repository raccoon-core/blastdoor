package report

import (
	"strings"
	"testing"

	"github.com/raccoon-core/blastdoor/internal/policy"
	"gopkg.in/yaml.v3"
)

func applyYAML(t *testing.T, rep Report) (string, map[string]any) {
	t.Helper()
	var b strings.Builder
	if err := rep.WriteApplyYAML(&b, ApplyInclude{File: ".gitlab/blastdoor-apply.yml"}); err != nil {
		t.Fatalf("WriteApplyYAML: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(b.String()), &doc); err != nil {
		t.Fatalf("generated YAML does not parse: %v\n%s", err, b.String())
	}
	return b.String(), doc
}

func job(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	raw, ok := doc[name]
	if !ok {
		t.Fatalf("no job %q in %v", name, doc)
	}
	j, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("job %q is %T, want a mapping", name, raw)
	}
	return j
}

func TestWriteApplyYAMLCarriesALiteralWhen(t *testing.T) {
	rep := decided(t, "int=auto,stg=manual", []Unit{
		{Path: "ops/int/a", Environment: "int", Changes: []policy.Change{autoChange("x", "int")}},
		{Path: "ops/stg/a", Environment: "stg", Changes: []policy.Change{change("y", policy.Pass, "fine")}},
	})

	_, doc := applyYAML(t, rep)

	if got := job(t, doc, "apply:int")["when"]; got != "on_success" {
		t.Errorf("apply:int when = %v, want on_success", got)
	}
	if got := job(t, doc, "apply:stg")["when"]; got != "manual" {
		t.Errorf("apply:stg when = %v, want manual", got)
	}
	if got := job(t, doc, "apply:int")["extends"]; got != ".blastdoor:apply" {
		t.Errorf("apply:int extends = %v, want .blastdoor:apply", got)
	}
}

// Nothing to apply, so no job: an empty one would run the repository's apply
// script against no units.
func TestWriteApplyYAMLSkipsEnvironmentsWithNothingToApply(t *testing.T) {
	rep := decided(t, "int=auto,prd=auto", []Unit{
		{Path: "ops/int/a", Environment: "int", Changes: []policy.Change{change("x", policy.Pass, "fine")}},
	})

	_, doc := applyYAML(t, rep)

	if _, found := doc["apply:prd"]; found {
		t.Error("apply:prd was generated for an environment where nothing changed")
	}
	if _, found := doc["apply:int"]; !found {
		t.Error("apply:int is missing")
	}
}

// A GitLab child pipeline needs at least one job, or the trigger job that
// would run it fails to create it at all. When every environment resolves to
// none — the ordinary case for a docs-only or CI-only change — the file must
// still be a pipeline GitLab can build: a single placeholder job, and no
// include: for a .blastdoor:apply job there is nothing here to extend.
func TestWriteApplyYAMLPlaceholdersWhenNothingResolvesToApply(t *testing.T) {
	rep := decided(t, "int=auto,prd=auto", nil)

	text, doc := applyYAML(t, rep)

	if len(doc) != 1 {
		t.Fatalf("generated pipeline has %d jobs, want exactly 1:\n%s", len(doc), text)
	}
	if _, found := doc["blastdoor:nothing-to-apply"]; !found {
		t.Errorf("no placeholder job in:\n%s", text)
	}
	if strings.Contains(text, "include:") {
		t.Errorf("placeholder pipeline should not include the repository's apply file:\n%s", text)
	}
}

func TestWriteApplyYAMLIncludesTheRepositoryJob(t *testing.T) {
	rep := decided(t, "int=auto", []Unit{
		{Path: "ops/int/a", Environment: "int", Changes: []policy.Change{change("x", policy.Pass, "fine")}},
	})

	text, doc := applyYAML(t, rep)

	if _, found := doc["include"]; !found {
		t.Errorf("no include in:\n%s", text)
	}
	if !strings.Contains(text, ".gitlab/blastdoor-apply.yml") {
		t.Errorf("the include does not name the file:\n%s", text)
	}
	if got := job(t, doc, "apply:int")["variables"]; got == nil {
		t.Error("apply:int has no variables block naming its environment")
	}
}

func TestWriteApplyYAMLCanIncludeAProject(t *testing.T) {
	rep := decided(t, "int=auto", []Unit{
		{Path: "ops/int/a", Environment: "int", Changes: []policy.Change{change("x", policy.Pass, "fine")}},
	})

	var b strings.Builder
	err := rep.WriteApplyYAML(&b, ApplyInclude{File: "apply.yml", Project: "group/shared-ci", Ref: "1-latest"})
	if err != nil {
		t.Fatalf("WriteApplyYAML: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(b.String()), &doc); err != nil {
		t.Fatalf("generated YAML does not parse: %v\n%s", err, b.String())
	}

	includes, ok := doc["include"].([]any)
	if !ok || len(includes) != 1 {
		t.Fatalf("include = %v, want one entry", doc["include"])
	}
	entry, ok := includes[0].(map[string]any)
	if !ok {
		t.Fatalf("include entry = %v, want a mapping", includes[0])
	}
	if entry["project"] != "group/shared-ci" || entry["ref"] != "1-latest" || entry["file"] != "apply.yml" {
		t.Errorf("include entry = %v, want project/ref/file set", entry)
	}
	if _, found := entry["local"]; found {
		t.Errorf("include entry = %v, should not carry local when Project is set", entry)
	}
}

// The dotenv and the YAML come from one fold and must never disagree.
func TestApplyYAMLAgreesWithTheDotenv(t *testing.T) {
	tests := []struct {
		name, wish string
		units      []Unit
	}{
		{"all auto", "int=auto", []Unit{{Path: "a", Environment: "int", Changes: []policy.Change{change("x", policy.Pass, "fine")}}}},
		{"review tightens", "int=auto", []Unit{{Path: "a", Environment: "int", Changes: []policy.Change{change("x", policy.Review, "look")}}}},
		{"manual wish", "prd=manual", []Unit{{Path: "a", Environment: "prd", Changes: []policy.Change{change("x", policy.Pass, "fine")}}}},
		{"nothing changed", "int=auto,prd=auto", []Unit{{Path: "a", Environment: "int", Changes: []policy.Change{change("x", policy.Pass, "fine")}}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := decided(t, tc.wish, tc.units)

			var env strings.Builder
			if err := rep.WriteEnv(&env); err != nil {
				t.Fatalf("WriteEnv: %v", err)
			}
			_, doc := applyYAML(t, rep)

			for _, e := range rep.Environments {
				line := "BLASTDOOR_DEPLOY_" + strings.ToUpper(e.Name) + "=" + string(e.Method) + "\n"
				if !strings.Contains(env.String(), line) {
					t.Errorf("dotenv missing %q", line)
				}

				name := "apply:" + e.Name
				_, generated := doc[name]
				switch e.Method {
				case None:
					if generated {
						t.Errorf("%s generated, but the dotenv says none", name)
					}
				case Auto:
					if !generated || job(t, doc, name)["when"] != "on_success" {
						t.Errorf("%s does not match the dotenv's auto", name)
					}
				case Manual:
					if !generated || job(t, doc, name)["when"] != "manual" {
						t.Errorf("%s does not match the dotenv's manual", name)
					}
				}
			}
		})
	}
}
