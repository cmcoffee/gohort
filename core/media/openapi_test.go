package media

import (
	"strings"
	"testing"
)

const sampleSpec = `{
  "openapi": "3.0.1",
  "info": {"title": "GitLab API", "version": "v4", "description": "Project automation."},
  "servers": [{"url": "https://gitlab.example.com/api/v4"}],
  "paths": {
    "/projects/{id}/merge_requests": {
      "parameters": [
        {"name": "id", "in": "path", "required": true, "schema": {"type": "string"},
         "description": "Project id or URL-encoded path."}
      ],
      "get": {
        "operationId": "listMergeRequests",
        "summary": "List merge requests",
        "description": "Returns merge requests for a project.",
        "tags": ["merge_requests"],
        "parameters": [
          {"name": "state", "in": "query", "schema": {"type": "string", "enum": ["opened", "closed"]},
           "description": "Filter by\nstate."}
        ],
        "responses": {
          "200": {"description": "A list", "content": {"application/json":
            {"schema": {"type": "array", "items": {"$ref": "#/components/schemas/MergeRequest"}}}}},
          "404": {"description": "Not found"}
        }
      },
      "post": {
        "summary": "Create a merge request",
        "requestBody": {"required": true, "content": {"application/json":
          {"schema": {"type": "object", "properties": {"title": {}, "source_branch": {}}}}}},
        "responses": {"201": {"description": "Created"}}
      }
    },
    "/version": {"get": {"summary": "Server version", "responses": {"200": {"description": "ok"}}}}
  }
}`

// TestOpenAPIDetection — on the marker key, not the filename: specs arrive as
// openapi.json, swagger.json, api-docs, or a download with no extension.
func TestOpenAPIDetection(t *testing.T) {
	if !LooksLikeOpenAPI([]byte(sampleSpec)) {
		t.Error("a 3.0 spec was not detected")
	}
	if !LooksLikeOpenAPI([]byte(`{"swagger":"2.0","paths":{}}`)) {
		t.Error("a Swagger 2.0 spec was not detected")
	}
	if LooksLikeOpenAPI([]byte(`{"name":"not a spec","paths":{}}`)) {
		t.Error("ordinary JSON was mistaken for a spec")
	}
	if LooksLikeOpenAPI([]byte(`not json at all`)) {
		t.Error("non-JSON was mistaken for a spec")
	}
}

// TestOperationsBecomeSections — one "## " heading per operation is what makes
// the report chunker produce one chunk per endpoint.
func TestOperationsBecomeSections(t *testing.T) {
	md, err := OpenAPIToMarkdown([]byte(sampleSpec))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var headings []string
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "## ") {
			headings = append(headings, line)
		}
	}
	if len(headings) != 3 {
		t.Fatalf("got %d operation sections, want 3 (GET+POST on merge_requests, GET version):\n%v", len(headings), headings)
	}
	for _, want := range []string{"## GET /projects/{id}/merge_requests", "## POST /projects/{id}/merge_requests", "## GET /version"} {
		found := false
		for _, h := range headings {
			if strings.HasPrefix(h, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing section %q; got %v", want, headings)
		}
	}
}

// TestEverySectionIsSelfContained — the property the whole file exists for. A
// chunk is retrieved alone, so a section that cannot say which endpoint it
// describes answers nothing.
func TestEverySectionIsSelfContained(t *testing.T) {
	md, err := OpenAPIToMarkdown([]byte(sampleSpec))
	if err != nil {
		t.Fatal(err)
	}
	sections := strings.Split(md, "\n## ")[1:] // drop the document header
	if len(sections) != 3 {
		t.Fatalf("expected 3 sections, got %d", len(sections))
	}
	for _, sec := range sections {
		body := "## " + sec
		if !strings.Contains(body, "Endpoint: `") {
			t.Errorf("a section never restates its endpoint in the body:\n%s", body)
		}
		if !strings.Contains(body, "GitLab API") {
			t.Errorf("a section does not name the spec it came from:\n%s", body)
		}
	}
}

// TestPathLevelParametersReachEveryOperation — a parameter declared once on the
// path applies to all its operations. Dropped, a chunk would describe an
// endpoint while omitting its required id.
func TestPathLevelParametersReachEveryOperation(t *testing.T) {
	md, err := OpenAPIToMarkdown([]byte(sampleSpec))
	if err != nil {
		t.Fatal(err)
	}
	for _, sec := range strings.Split(md, "\n## ")[1:] {
		if !strings.Contains("## "+sec, "/projects/{id}/merge_requests") {
			continue
		}
		if !strings.Contains(sec, "`id`") || !strings.Contains(sec, "required") {
			t.Errorf("path-level required parameter missing from a section:\n%s", sec)
		}
	}
}

// TestRenderedDetail — the things a caller needs in order to actually make the
// request.
func TestRenderedDetail(t *testing.T) {
	md, err := OpenAPIToMarkdown([]byte(sampleSpec))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"listMergeRequests",                 // operation id
		"one of: opened, closed",            // enum
		"array of MergeRequest",             // $ref named, not inlined
		"object {source_branch, title}",     // inline body shape
		"(required)",                        // required request body
		"`404`",                             // error responses
		"https://gitlab.example.com/api/v4", // base URL
		"Tags: merge_requests",              // tags
	} {
		if !strings.Contains(md, want) {
			t.Errorf("rendered spec is missing %q", want)
		}
	}
	// A multi-line description must not break the bullet it sits in.
	if strings.Contains(md, "Filter by\nstate.") {
		t.Error("a multi-line parameter description was not flattened")
	}
}

// TestRenderIsStable — re-ingesting the same spec must produce the same text,
// or every upload looks like a wholly new document.
func TestRenderIsStable(t *testing.T) {
	first, err := OpenAPIToMarkdown([]byte(sampleSpec))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := OpenAPIToMarkdown([]byte(sampleSpec))
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Fatal("rendering is unstable across runs — map iteration is leaking into the output")
		}
	}
}

// TestEmptyAndBrokenSpecs — refused with a reason rather than yielding an empty
// document that ingests as nothing.
func TestEmptyAndBrokenSpecs(t *testing.T) {
	if _, err := OpenAPIToMarkdown([]byte(`{"openapi":"3.0.0","paths":{}}`)); err == nil {
		t.Error("a spec with no paths was accepted")
	}
	if _, err := OpenAPIToMarkdown([]byte(`{"openapi":"3.0.0","paths":{"/x":{"summary":"no ops"}}}`)); err == nil {
		t.Error("a spec with paths but no operations was accepted")
	}
	if _, err := OpenAPIToMarkdown([]byte(`{`)); err == nil {
		t.Error("malformed JSON was accepted")
	}
}

// TestSwagger2BaseURL — 2.0 says host/basePath instead of servers.
func TestSwagger2BaseURL(t *testing.T) {
	md, err := OpenAPIToMarkdown([]byte(`{"swagger":"2.0","info":{"title":"Old"},
		"host":"api.example.com","basePath":"/v1",
		"paths":{"/ping":{"get":{"summary":"Ping","responses":{"200":{"description":"ok"}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "api.example.com/v1") {
		t.Errorf("Swagger 2.0 host/basePath was not rendered:\n%s", md)
	}
}
