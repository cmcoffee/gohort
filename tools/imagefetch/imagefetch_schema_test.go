package imagefetch

// The grouped `image` tool advertises only the actions whose backing config
// exists. Offering `find` with no serper key produced a call that could only
// fail — and the model, having no way to predict that, retried it.
//
// The rules are tested through imageActions/imageSchemaFor rather than through
// live config, so a machine with (or without) a search provider gets the same
// answer.

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func schemaFor(a imageActions) imageSchema { return imageSchemaFor(a) }

func TestActionsNarrowToWhatIsConfigured(t *testing.T) {
	cases := []struct {
		name string
		set  imageActions
		want []string
	}{
		// `help`, `keep` and `forget` ride along on every non-empty set: all
		// three are framework-side (the image space), so no backend config can
		// take them away. help is how the model finds out which pictures it can
		// still reference; keep/forget are how it decides which ones outlive
		// the ring.
		{"all configured", imageActions{find: true, fetch: true, generate: true}, []string{"find", "fetch", "generate", "help", "keep", "label", "forget"}},
		{"no search provider", imageActions{fetch: true, generate: true}, []string{"fetch", "generate", "help", "keep", "label", "forget"}},
		{"no image gen", imageActions{find: true, fetch: true}, []string{"find", "fetch", "help", "keep", "label", "forget"}},
		{"fetch only", imageActions{fetch: true}, []string{"fetch", "help", "keep", "label", "forget"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := schemaFor(c.set)
			enum := s.params["action"].Enum
			if !slices.Equal(enum, c.want) {
				t.Errorf("action enum = %v, want %v", enum, c.want)
			}
			// A param for an action that can't run is dead weight in the schema.
			if _, ok := s.params["query"]; ok != c.set.find {
				t.Errorf("query param present = %v, want %v", ok, c.set.find)
			}
			if _, ok := s.params["prompt"]; ok != c.set.generate {
				t.Errorf("prompt param present = %v, want %v", ok, c.set.generate)
			}
			if _, ok := s.params["url"]; !ok {
				t.Error("url param must always be present — fetch needs no config")
			}
		})
	}
}

func TestNoActionsMeansUnavailable(t *testing.T) {
	// Never ship an empty enum: it invalidates the whole tool payload for the
	// turn. Nil params tells the catalog to drop the tool instead.
	s := schemaFor(imageActions{})
	if s.params != nil {
		t.Errorf("params = %v, want nil so the tool is dropped", s.params)
	}
	if s.desc != "" {
		t.Errorf("desc = %q, want empty", s.desc)
	}
}

func TestDescriptionNeverNamesAnUnavailableAction(t *testing.T) {
	// The prose and the enum have to agree — a description mentioning `generate`
	// while the enum omits it is worse than either alone.
	s := schemaFor(imageActions{find: true, fetch: true})
	for _, banned := range []string{"generate (", "→ generate"} {
		if containsStr(s.desc, banned) {
			t.Errorf("description names the unavailable generate action (%q):\n%s", banned, s.desc)
		}
	}
	if !containsStr(s.desc, "find (") || !containsStr(s.desc, "fetch (") {
		t.Errorf("description omits an available action:\n%s", s.desc)
	}
}

func TestSchemaIsDeterministic(t *testing.T) {
	// Tool schemas sit at the front of the prompt; drift between turns re-pays
	// cold prefill. The map iteration inside imageSchemaFor must not leak out.
	set := imageActions{find: true, fetch: true, generate: true}
	first, err := json.Marshal(schemaFor(set).params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	firstDesc := schemaFor(set).desc
	for i := 0; i < 20; i++ {
		next, err := json.Marshal(schemaFor(set).params)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(first) != string(next) {
			t.Fatalf("params drifted:\n first: %s\n  next: %s", first, next)
		}
		if d := schemaFor(set).desc; d != firstDesc {
			t.Fatalf("description drifted:\n first: %s\n  next: %s", firstDesc, d)
		}
	}
}

func TestStaticSchemaKeepsFullShape(t *testing.T) {
	// Desc()/Params() feed the global semantic tool index and the session-less
	// pickers, which must see every action regardless of local config.
	tool := &ImageTool{}
	enum := tool.Params()["action"].Enum
	if !slices.Equal(enum, []string{"find", "fetch", "generate", "help", "keep", "label", "forget"}) {
		t.Errorf("static action enum = %v, want all three backend actions plus the three framework-side ones", enum)
	}
	if tool.Desc() == "" {
		t.Error("static description must not be empty — the tool index embeds it")
	}
}

func TestUnavailableActionExplainsItself(t *testing.T) {
	// A model can name an action that wasn't in its enum (stale context, a
	// copied call). The refusal has to say WHY and whether retrying helps.
	tool := &ImageTool{}
	if !isDynamic(tool) {
		t.Fatal("ImageTool must implement core.DynamicChatTool")
	}
	// generate is gated on live config; when it's off the error must name the
	// cause rather than fall through to "unknown action".
	if !ImageGenerationAvailable() {
		_, err := tool.RunWithSession(map[string]any{"action": "generate", "prompt": "x"}, nil)
		if err == nil {
			t.Fatal("generate with no provider must fail")
		}
		if !containsStr(err.Error(), "no image-generation provider is configured") {
			t.Errorf("error = %q, want the configuration reason", err)
		}
	}
	_, err := tool.RunWithSession(map[string]any{"action": "bogus"}, nil)
	if err == nil || !containsStr(err.Error(), "unknown action") {
		t.Errorf("error = %v, want an unknown-action error", err)
	}
}

func isDynamic(v any) bool {
	_, ok := v.(DynamicChatTool)
	return ok
}

func containsStr(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func TestEveryActionTheDescriptionAdvertisesIsCallable(t *testing.T) {
	// The invariant that broke. The description had said "help." since it was
	// written, the enum never listed it, and on a grammar-constrained backend an
	// enum is not advice — the sampler cannot emit a value that isn't in it. So
	// the documented way to ask "which pictures can I still reference?" was
	// unreachable, and a turn that needed it went guessing at filenames instead.
	//
	// Checked as a rule rather than for `help` alone: prose and enum drifting
	// apart is the failure, and it can drift on any action.
	for _, set := range []imageActions{
		{find: true, fetch: true, generate: true, edit: true},
		{fetch: true},
		{generate: true, edit: true},
	} {
		s := schemaFor(set)
		enum := s.params["action"].Enum
		for _, name := range []string{"find", "fetch", "generate", "edit", "help"} {
			// "name (" is how each action introduces itself in the prose.
			if containsStr(s.desc, name+" (") && !slices.Contains(enum, name) {
				t.Errorf("description advertises %q but the enum omits it (enum=%v)", name, enum)
			}
		}
	}
}

func TestHelpNeverResurrectsAnUnavailableTool(t *testing.T) {
	// help needs no backend, so appending it to the enum must not turn a
	// deployment with nothing wired into a tool that can only describe itself.
	if s := schemaFor(imageActions{}); s.params != nil {
		t.Errorf("params = %v, want nil — help alone is not a working image tool", s.params)
	}
}
