package core

import (
	"encoding/json"
	"strings"
	"testing"
)

const openapi3Spec = `{
  "openapi":"3.0.0",
  "servers":[{"url":"https://api.example.com/v1"}],
  "paths":{
    "/pets/{id}":{"get":{"operationId":"get_pet","summary":"Get a pet","parameters":[{"name":"id","in":"path","required":true}]}},
    "/pets":{"post":{"operationId":"create_pet","summary":"Create a pet","requestBody":{"content":{}}}},
    "/search":{"get":{"operationId":"search","summary":"Search","parameters":[{"name":"q","in":"query","required":true}]}}
  }
}`

func actionByName(tb TempTool, name string) (TempToolAction, bool) {
	for _, a := range tb.Actions {
		if a.Name == name {
			return a, true
		}
	}
	return TempToolAction{}, false
}

func TestOpenAPIImportToolbox(t *testing.T) {
	tpl, ok := GetTemplate(TargetTool, "openapi_tool")
	if !ok {
		t.Fatal("openapi_tool template not registered")
	}
	art, warns, err := tpl.BuildSpec(map[string]any{"spec": openapi3Spec, "credential": "example"})
	if err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "action") {
		t.Errorf("expected an import-count warning, got %v", warns)
	}
	var tb TempTool
	if err := json.Unmarshal(art, &tb); err != nil {
		t.Fatal(err)
	}
	if tb.Mode != TempToolModeToolbox || tb.Credential != "example" {
		t.Errorf("wrong toolbox mode/cred: mode=%q cred=%q", tb.Mode, tb.Credential)
	}
	if len(tb.Actions) != 3 {
		t.Fatalf("expected 3 actions, got %d", len(tb.Actions))
	}
	// GET with a path param.
	if a, ok := actionByName(tb, "get_pet"); !ok {
		t.Error("get_pet action missing")
	} else {
		if a.Method != "GET" || a.URLTemplate != "https://api.example.com/v1/pets/{id}" {
			t.Errorf("get_pet wrong: %+v", a)
		}
		if _, ok := a.Params["id"]; !ok {
			t.Errorf("path param id not derived: %v", a.Params)
		}
	}
	// POST with a request body → a {body} argument.
	if a, ok := actionByName(tb, "create_pet"); !ok {
		t.Error("create_pet action missing")
	} else {
		if a.Method != "POST" || a.BodyTemplate != "{body}" {
			t.Errorf("create_pet body wrong: %+v", a)
		}
		if _, ok := a.Params["body"]; !ok {
			t.Errorf("body param not derived: %v", a.Params)
		}
	}
	// GET with a required query param → appended to the URL.
	if a, ok := actionByName(tb, "search"); !ok {
		t.Error("search action missing")
	} else if a.URLTemplate != "https://api.example.com/v1/search?q={q}" {
		t.Errorf("query param not appended: %q", a.URLTemplate)
	}
}

func TestOpenAPIDetectAndFilter(t *testing.T) {
	tpl, _ := GetTemplate(TargetTool, "openapi_tool")
	if !tpl.HasDetect() {
		t.Fatal("openapi_tool should have Detect")
	}
	vals, warns, err := tpl.Detect(map[string]any{"spec": openapi3Spec})
	if err != nil {
		t.Fatal(err)
	}
	if vals["base_url"] != "https://api.example.com/v1" {
		t.Errorf("base_url not detected: %v", vals["base_url"])
	}
	if ops, _ := vals["operations"].(string); !strings.Contains(ops, "GET /pets/{id}") || !strings.Contains(ops, "POST /pets") {
		t.Errorf("operations preview wrong: %v", vals["operations"])
	}
	if len(warns) == 0 {
		t.Error("expected an operation-count note")
	}
	// The path filter narrows the import.
	art, _, err := tpl.BuildSpec(map[string]any{"spec": openapi3Spec, "credential": "x", "filter": "/pets"})
	if err != nil {
		t.Fatal(err)
	}
	var tb TempTool
	_ = json.Unmarshal(art, &tb)
	if len(tb.Actions) != 2 { // /pets/{id} + /pets, not /search
		t.Errorf("filter=/pets should yield 2 actions, got %d", len(tb.Actions))
	}
}

func TestOpenAPISwagger2BaseURL(t *testing.T) {
	spec := `{"swagger":"2.0","host":"api.x.com","basePath":"/v2","schemes":["https"],
	  "paths":{"/a":{"get":{"operationId":"a"}}}}`
	base, ops, err := parseOpenAPI(spec)
	if err != nil {
		t.Fatal(err)
	}
	if base != "https://api.x.com/v2" {
		t.Errorf("swagger 2.0 base = %q, want https://api.x.com/v2", base)
	}
	if len(ops) != 1 || ops[0].Method != "GET" {
		t.Errorf("ops = %+v", ops)
	}
}
