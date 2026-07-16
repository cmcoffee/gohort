package temptool

import (
	"encoding/json"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// TestSubstituteJSONOptionalDrop verifies that an optional (non-required) body
// field drops out cleanly when omitted — the moltbook "post" failure where a
// text post errored with `missing arg "url"` because url (link-only) was baked
// into the body template. Provided values and required params behave as before.
func TestSubstituteJSONOptionalDrop(t *testing.T) {
	params := map[string]ToolParam{
		"title":        {Type: "string"},
		"content":      {Type: "string"},
		"url":          {Type: "string"},
		"submolt_name": {Type: "string"},
	}
	required := []string{"title", "submolt_name"}

	cases := []struct {
		name string
		tmpl string
		args map[string]any
		// wantKeys: keys expected present in the produced JSON object.
		wantKeys []string
		// absentKeys: keys expected NOT present.
		absentKeys []string
		wantErr    bool
	}{
		{
			name:       "url middle, omitted → dropped, others intact",
			tmpl:       `{"title":{title},"content":{content},"url":{url},"submolt_name":{submolt_name}}`,
			args:       map[string]any{"title": "Hi", "content": "body", "submolt_name": "general"},
			wantKeys:   []string{"title", "content", "submolt_name"},
			absentKeys: []string{"url"},
		},
		{
			name:       "url last, omitted → dropped",
			tmpl:       `{"title":{title},"submolt_name":{submolt_name},"url":{url}}`,
			args:       map[string]any{"title": "Hi", "submolt_name": "general"},
			wantKeys:   []string{"title", "submolt_name"},
			absentKeys: []string{"url"},
		},
		{
			name:       "url provided → kept",
			tmpl:       `{"title":{title},"url":{url},"submolt_name":{submolt_name}}`,
			args:       map[string]any{"title": "Hi", "url": "https://x.test", "submolt_name": "general"},
			wantKeys:   []string{"title", "url", "submolt_name"},
			absentKeys: []string{},
		},
		{
			name:       "quote-wrapped placeholder, omitted → dropped",
			tmpl:       `{"title":{title},"url":"{url}","submolt_name":{submolt_name}}`,
			args:       map[string]any{"title": "Hi", "submolt_name": "general"},
			wantKeys:   []string{"title", "submolt_name"},
			absentKeys: []string{"url"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, err := substituteJSON(c.tmpl, params, required, c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got body %q", body)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v (body=%q)", err, body)
			}
			var m map[string]any
			if jerr := json.Unmarshal([]byte(body), &m); jerr != nil {
				t.Fatalf("produced invalid JSON: %v\nbody: %s", jerr, body)
			}
			for _, k := range c.wantKeys {
				if _, ok := m[k]; !ok {
					t.Errorf("expected key %q present; body: %s", k, body)
				}
			}
			for _, k := range c.absentKeys {
				if _, ok := m[k]; ok {
					t.Errorf("expected key %q absent; body: %s", k, body)
				}
			}
		})
	}
}

// TestSubstituteJSONRequiredMissingStillErrors: a missing REQUIRED field must
// still error — optional-drop must not swallow a genuinely required omission.
func TestSubstituteJSONRequiredMissingStillErrors(t *testing.T) {
	params := map[string]ToolParam{
		"title":        {Type: "string"},
		"submolt_name": {Type: "string"},
	}
	required := []string{"title", "submolt_name"}
	_, err := substituteJSON(`{"title":{title},"submolt_name":{submolt_name}}`, params, required, map[string]any{"title": "Hi"})
	if err == nil {
		t.Fatal("missing required submolt_name should error")
	}
}
