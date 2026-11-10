package imagefetch

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	_ "github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The Gemini template, saved as a connector and made the default, gives an
// agent's image tool both actions: a picture from a prompt alone, and edits
// and blends of the pictures it passes. Its backend note says both. It said
// only "works from pictures you pass in images", and an agent with Gemini told
// its person it could restyle or combine pictures but not make one from a
// prompt.
func TestTheGeminiTemplateGeneratesAndEditsForAnAgent(t *testing.T) {
	prevRoot, prevAuth, prevProvider := RootDB, AuthDB, ImageProviderFunc
	RootDB = &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	ImageProviderFunc = func() string { return "gemini-images" }
	t.Cleanup(func() { RootDB, AuthDB, ImageProviderFunc = prevRoot, prevAuth, prevProvider })

	raw, err := os.ReadFile("../../core/recipes/builtin/gemini-images.json")
	if err != nil {
		t.Fatal(err)
	}
	var tmpl struct {
		Bundle struct {
			Artifacts []struct {
				Recipe struct {
					Spec json.RawMessage `json:"spec"`
				} `json:"recipe"`
			} `json:"artifacts"`
		} `json:"bundle"`
	}
	if err := json.Unmarshal(raw, &tmpl); err != nil {
		t.Fatal(err)
	}
	if err := SaveConnector(RootDB, Connector{Name: "gemini-images", Kind: "service", Template: "gemini-images", Spec: tmpl.Bundle.Artifacts[0].Recipe.Spec}); err != nil {
		t.Fatal(err)
	}
	RootDB.Set(ImageTable, "provider", "gemini-images")
	t.Cleanup(func() { UnapproveConnector(RootDB, "gemini-images") })

	a := liveImageActions(&ToolSession{})
	if !a.generate || !isGenerator(a, "gemini-images") {
		t.Errorf("Gemini does not generate for an agent: generate=%v backends=%+v", a.generate, a.backends)
	}
	if !a.edit || !isEditor(a, "gemini-images") {
		t.Errorf("Gemini does not edit for an agent: edit=%v editors=%+v", a.edit, a.editors)
	}
	if d := a.backendParamDesc(); !strings.Contains(d, "gemini-images: generates from text alone or from references, and edits a picture you pass") {
		t.Errorf("the backend note: %s", d)
	}

	// The same calls an agent makes whatever the backend is: an edit names
	// the picture to change and the references to take from, and a model
	// that reads plain language is told which is which, the picture first.
	sess := &ToolSession{}
	edit, err := planEdit(sess, map[string]any{"action": "edit", "picture": "image#scene", "references": []any{"image#face"},
		"prompt": "swap the man on the left for the person in the reference"}, a)
	if err != nil {
		t.Fatalf("an edit naming its picture: %v", err)
	}
	if strings.Join(edit.refs, ",") != "image#scene,image#face" || !strings.HasPrefix(edit.prompt, "Picture 1 is the photo to edit.") ||
		!strings.Contains(edit.prompt, "Pictures 2 to 2 are references") || !strings.Contains(edit.prompt, "The change: swap the man on the left") {
		t.Errorf("the edit as Gemini gets it: %v %q", edit.refs, edit.prompt)
	}
	// No face pass runs on a service connector, so none is offered or said.
	if edit.refineFaces {
		t.Error("a face pass was planned on Gemini, where none runs")
	}
	if _, ok := imageSchemaFor(a).params["preserve_faces"]; ok {
		t.Error("preserve_faces is offered where no face pass can run")
	}
	twoSlotGraph := imageActions{edit: true, editors: []ImageBackendChoice{{Name: "comfy", Edits: true, MaxImages: 2, Graph: true}}}
	if _, ok := imageSchemaFor(twoSlotGraph).params["preserve_faces"]; !ok {
		t.Error("a two-slot graph, where the face pass runs, is not offered it")
	}
	fresh, err := planEdit(sess, map[string]any{"action": "generate", "references": []any{"image#a", "image#b"}, "prompt": "the two of them on a beach"}, a)
	if err != nil || edit.backend != fresh.backend || !strings.HasPrefix(fresh.prompt, "Make a new picture from the request below.") {
		t.Errorf("a new picture from references: %v %q", err, fresh.prompt)
	}
	if _, err := planEdit(sess, map[string]any{"action": "edit", "references": []any{"image#a"}, "prompt": "x"}, a); err == nil || !strings.Contains(err.Error(), "name it as picture") {
		t.Errorf("an edit with nothing named to change: %v", err)
	}
	// A graph is told nothing: its prompt is a diffusion prompt.
	graph := a
	graph.editors = append([]ImageBackendChoice(nil), a.editors...)
	for i := range graph.editors {
		graph.editors[i].Graph = true
	}
	if g, err := planEdit(sess, map[string]any{"action": "edit", "picture": "image#scene", "prompt": "make it snowy"}, graph); err != nil ||
		g.prompt != "make it snowy"+editCompositingGuard() {
		t.Errorf("a graph's prompt was framed: %v %q", err, g.prompt)
	}
	// Framed, the guard is said by role: the photo being edited is the
	// result, so it is not told it is only a reference.
	if strings.Contains(edit.prompt, "The supplied pictures are references") || !strings.Contains(edit.prompt, "the photo to edit, changed. The references must not appear") {
		t.Errorf("the guard on a framed edit: %q", edit.prompt)
	}
}
