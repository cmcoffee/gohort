package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The value↔spec mapping (ReadValues/BuildSpec) now lives in core with the
// templates and is tested there; this covers the admin-only Edit-spec transforms.
func TestNestAndStringifyComfyWorkflow(t *testing.T) {
	// A spec whose comfy_workflow is a string holding pretty JSON.
	spec, _ := json.Marshal(RestImageSpec{
		SubmitURL:     "http://x/prompt",
		ComfyWorkflow: "{\n  \"6\": {\n    \"class_type\": \"CLIPTextEncode\"\n  }\n}",
	})

	// Edit-spec view: comfy_workflow becomes a NESTED object, not an escaped string.
	nested, ok := nestComfyWorkflow(spec)
	if !ok {
		t.Fatal("expected nesting to apply")
	}
	if strings.Contains(string(nested), `\"6\"`) || strings.Contains(string(nested), `\n`) {
		t.Errorf("workflow still escaped in Edit-spec view:\n%s", nested)
	}
	if !strings.Contains(string(nested), "\"comfy_workflow\": {") {
		t.Errorf("comfy_workflow not rendered as a nested object:\n%s", nested)
	}

	// Save path: an edited nested object folds back to a string, content survives.
	backToString := stringifyComfyWorkflow(nested)
	var out RestImageSpec
	if err := json.Unmarshal(backToString, &out); err != nil {
		t.Fatalf("stringified spec invalid: %v\n%s", err, backToString)
	}
	if !strings.Contains(out.ComfyWorkflow, "CLIPTextEncode") {
		t.Errorf("workflow content lost on round-trip: %q", out.ComfyWorkflow)
	}
	if !strings.Contains(out.ComfyWorkflow, "\n") {
		t.Errorf("workflow should be pretty (multi-line) after round-trip: %q", out.ComfyWorkflow)
	}

	// A non-image spec (no comfy_workflow) is passed through untouched.
	if _, ok := nestComfyWorkflow([]byte(`{"credential":"no_auth"}`)); ok {
		t.Error("nesting should not apply to a spec without comfy_workflow")
	}
}

// A service connector's row says how it is doing and asks before a test what
// the test should cost, and an image service is offered as the image
// provider once approved.
func TestAServiceConnectorRowAndThePicker(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	spec := `{"provider":{"transport":"http","base_url":"https://api.example.test","auth":{"credential":"acme_key"},
		"operations":{"submit":{"method":"POST","path":"/v1/images","body":{"prompt":"{prompt}"}},"models":{"path":"/v1/models"}},
		"health":{"call":"models","expect":"data"}},
		"provides":[{"capability":"image.generate","steps":[{"call":"submit"}],
		"output":{"image":{"path":"data.0.b64_json","as":"base64"}},"cost":{"per_call":0.04}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "acme-images", Kind: "service", Spec: json.RawMessage(spec)}); err != nil {
		t.Fatal(err)
	}
	if q := testConfirm("acme-images"); !strings.Contains(q, "$0.0400") || !strings.Contains(q, "daily test budget") {
		t.Errorf("the question before a test: %q", q)
	}
	conn, _ := GetConnector(RootDB, "acme-images")
	if status, hasHealth := connectorStatus(conn); !hasHealth || status != "not checked yet" {
		t.Errorf("status %q, has health %v", status, hasHealth)
	}
	offered := func() bool {
		for _, o := range imageProviderOptions() {
			if o.Value == "acme-images" {
				return true
			}
		}
		return false
	}
	if offered() {
		t.Error("an unapproved service is offered as the image provider")
	}
	if err := ApproveConnector(RootDB, "acme-images"); err != nil {
		t.Fatal(err)
	}
	if !offered() {
		t.Error("an approved image service is not offered as the image provider")
	}

	// Its Endpoints: each one as it is sent and read, and what uses it.
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	mux := http.NewServeMux()
	a.registerConnectorsRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/connectors/endpoints?name=acme-images", nil))
	var eps []service.Endpoint
	json.Unmarshal(w.Body.Bytes(), &eps)
	if len(eps) != 2 || eps[0].Address != "POST /v1/images" || eps[0].Sends != "json: prompt ← {prompt}" ||
		eps[0].Reads != "data.0.b64_json → image (a file, from base64)" || eps[1].UsedBy != "health check" {
		t.Errorf("endpoints: %d %s", w.Code, w.Body.String())
	}
	rows, _ := a.handleConnectors(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/connectors", nil)})
	if b, _ := json.Marshal(rows); !strings.Contains(string(b), `"name":"acme-images"`) || !strings.Contains(string(b), `"endpoints":true`) {
		t.Errorf("a service row offers its Endpoints: %s", b)
	}
}
