package imagefetch

// The `prompt` param used to be added only when generation was configured, and
// labelled "(generate)". On an edit the model read it as another action's field
// and left it out — so a blend ran with no instructions and came back as the
// backend's guess at what was wanted rather than what was asked for.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestPromptIsOfferedForEditNotJustGenerate(t *testing.T) {
	cases := []struct {
		name string
		set  imageActions
		want []string // substrings the prompt description must carry
	}{
		{"edit only", imageActions{fetch: true, edit: true,
			editors: []ImageBackendChoice{{Name: "blend", MaxImages: 2}}}, []string{"(edit)", "combine"}},
		{"generate only", imageActions{fetch: true, generate: true,
			backends: []ImageBackendChoice{{Name: "gen"}}}, []string{"(generate)"}},
		{"both", imageActions{fetch: true, generate: true, edit: true,
			backends: []ImageBackendChoice{{Name: "gen"}},
			editors:  []ImageBackendChoice{{Name: "blend", MaxImages: 2}}}, []string{"(generate)", "(edit)", "combine"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, ok := imageSchemaFor(c.set).params["prompt"]
			if !ok {
				t.Fatal("no prompt param offered at all — the model cannot say how to blend")
			}
			for _, want := range c.want {
				if !strings.Contains(p.Description, want) {
					t.Errorf("prompt description missing %q: %s", want, p.Description)
				}
			}
		})
	}
	// Neither action configured ⇒ no prompt param; it would name nothing.
	if _, ok := imageSchemaFor(imageActions{fetch: true}).params["prompt"]; ok {
		t.Error("prompt offered with neither generate nor edit configured")
	}
}

// The edit action's own line should say a prompt normally belongs with it —
// that is where the model is choosing an action, before it reads any param.
func TestEditActionMentionsThePrompt(t *testing.T) {
	s := imageSchemaFor(imageActions{fetch: true, edit: true,
		editors: []ImageBackendChoice{{Name: "blend", MaxImages: 2}}})
	if !strings.Contains(s.desc, "prompt") {
		t.Errorf("the edit action description never mentions a prompt:\n%s", s.desc)
	}
}
