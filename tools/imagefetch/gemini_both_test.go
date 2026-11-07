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
	if d := a.backendParamDesc(); !strings.Contains(d, "gemini-images: generates from text alone, or works from pictures") {
		t.Errorf("the backend note: %s", d)
	}
}
