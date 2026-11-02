package recipes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core"
)
// The REST call template reaches the form's own strategy through the form
// helper, because the tool's arguments come from the {placeholders} in the
// address it is given, which no file can know in advance.
func TestTheRESTCallTemplateBuildsTheFormsTool(t *testing.T) {
	r, _, ok := Get(testDB(t), "rest-call")
	if !ok {
		t.Fatal("rest-call is missing")
	}
	bundle, _, _, err := Fill(r, map[string]string{
		"name": "acme_get_order", "description": "Look up one order.",
		"url": "https://api.acme.example/v1/orders/{id}", "credential": "acme",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Artifacts) != 1 || bundle.Artifacts[0].Type != "tool" || bundle.Artifacts[0].Name != "acme_get_order" {
		t.Fatalf("one tool: %+v", bundle.Artifacts)
	}
	var tt core.TempTool
	if err := json.Unmarshal(bundle.Artifacts[0].Recipe, &tt); err != nil {
		t.Fatal(err)
	}
	if tt.Name != "acme_get_order" || tt.Template != "rest_call" || tt.Credential != "acme" || tt.Method != "GET" ||
		tt.CommandTemplate != "https://api.acme.example/v1/orders/{id}" || len(tt.Required) != 1 || tt.Required[0] != "id" {
		t.Errorf("the tool the form builds, named and recorded as the form's: %+v", tt)
	}
	if _, ok := core.TemplateForTool(tt); !ok {
		t.Error("Configure would not open the REST call form")
	}
	db := testDB(t)
	res, err := Install(db, "rest-call", "admin", map[string]string{"name": "acme_get_order", "description": "Look up one order.", "url": "https://api.acme.example/v1/orders/{id}"})
	if err != nil || res.Imported != 1 {
		t.Fatalf("the tool lands: %+v %v", res, err)
	}
}

// The OpenAPI template turns a pasted document into one toolbox through the
// same helper; a document that cannot be read is refused before anything
// lands.
func TestTheOpenAPITemplateBuildsAToolbox(t *testing.T) {
	doc := `{"openapi":"3.0.0","info":{"title":"Pets","version":"1"},"servers":[{"url":"https://pets.example/v1"}],
	"paths":{"/pets":{"get":{"operationId":"listPets","summary":"List pets","parameters":[{"name":"limit","in":"query","schema":{"type":"integer"}}]}},
	"/pets/{petId}":{"get":{"operationId":"showPet","summary":"One pet","parameters":[{"name":"petId","in":"path","required":true,"schema":{"type":"string"}}]}}}}`
	r, _, _ := Get(testDB(t), "openapi")
	bundle, _, _, err := Fill(r, map[string]string{"name": "petstore", "openapi": doc})
	if err != nil {
		t.Fatal(err)
	}
	var tt core.TempTool
	if err := json.Unmarshal(bundle.Artifacts[0].Recipe, &tt); err != nil {
		t.Fatal(err)
	}
	if tt.Name != "petstore" || tt.Template != "openapi_tool" || len(tt.Actions) != 2 {
		t.Errorf("a two-action toolbox recorded as the OpenAPI form's: name=%q template=%q actions=%d", tt.Name, tt.Template, len(tt.Actions))
	}
	if !strings.Contains(string(bundle.Artifacts[0].Recipe), "pets.example/v1") {
		t.Error("the document's server is the base address")
	}
	if _, _, _, err := Fill(r, map[string]string{"name": "broken", "openapi": "{not json"}); err == nil {
		t.Error("an unreadable document was accepted")
	}
}

// The form helper names what it needs.
func TestTheFormHelperRefusesAnUnknownForm(t *testing.T) {
	h, ok := LookupHelper("form")
	if !ok {
		t.Fatal("the form helper is not registered")
	}
	for _, with := range []map[string]string{{"form": "tool/no_such_form"}, {"form": "rest_call"}, {}} {
		if _, _, err := h.Run("x", with); err == nil {
			t.Errorf("%v was accepted", with)
		}
	}
}
