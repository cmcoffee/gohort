package recipes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/oddjob/core"
)

// The Automatic1111 forms are templates now, written as data: adding one
// lands the connector the form would have made, recorded as that form's so
// Configure on its row still opens the form for the defaults.
func TestTheAutomatic1111TemplatesMatchTheirForms(t *testing.T) {
	for _, c := range []struct{ id, form, name, path string }{
		{"a1111", "a1111", "a1111", "/sdapi/v1/txt2img"},
		{"a1111-img2img", "a1111_img2img", "a1111_edit", "/sdapi/v1/img2img"},
	} {
		db := testDB(t)
		res, err := Install(db, c.id, "admin", map[string]string{"base_url": "http://192.168.1.20:7860/", "prompt_suffix": "crisp"})
		if err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		if res.Imported != 1 {
			t.Fatalf("%s: one connector lands: %+v", c.id, res)
		}
		conn, ok := core.GetConnector(db, c.name)
		if !ok || conn.Kind != core.RestImageConnectorKind || conn.Template != c.form || conn.Approved {
			t.Fatalf("%s: an unapproved image connector recorded as the %s form's: %+v", c.id, c.form, conn)
		}
		tpl, ok := core.TemplateForConnector(conn)
		if !ok || tpl.Name != c.form {
			t.Errorf("%s: Configure would not open the %s form", c.id, c.form)
		}
		// Byte for byte what the form builds from the same answers.
		want, _, err := tpl.BuildSpec(map[string]any{"base_url": "http://192.168.1.20:7860", "prompt_suffix": "crisp"})
		if err != nil {
			t.Fatal(err)
		}
		var got, exp core.RestImageSpec
		json.Unmarshal(conn.Spec, &got)
		json.Unmarshal(want, &exp)
		if got.SubmitURL != "http://192.168.1.20:7860"+c.path || got.SubmitURL != exp.SubmitURL || got.SubmitBody != exp.SubmitBody ||
			got.ImageB64Path != exp.ImageB64Path || got.DefaultSteps != exp.DefaultSteps || got.PromptSuffix != "crisp" || got.Credential != "no_auth" {
			t.Errorf("%s: the template's connector differs from the form's:\n got %+v\nwant %+v", c.id, got, exp)
		}
	}
}

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
