package service

import (
	"encoding/json"
	"strings"
	"testing"
)

// imageSpec is a valid image service: submit, wait on its status, the image
// at a URL. base is the provider's address.
func imageSpec(base string) map[string]any {
	return map[string]any{
		"provider": map[string]any{
			"transport": "http",
			"base_url":  base,
			"auth":      map[string]any{"none": true},
			"operations": map[string]any{
				"submit": map[string]any{"method": "POST", "path": "/v1/jobs", "send": "json",
					"body": map[string]any{"prompt": "{prompt}", "size": "{width}x{height}", "w": "{width}"}},
				"status": map[string]any{"path": "/v1/jobs/{id}"},
				"models": map[string]any{"path": "/v1/models"},
			},
			"health": map[string]any{"call": "models", "expect": "data", "every_mins": 30},
		},
		"provides": []any{map[string]any{
			"capability": "image.generate",
			"steps": []any{
				map[string]any{"call": "submit", "save": map[string]any{"id": "job.id"}},
				map[string]any{"wait": "status", "until": "state == done", "fail": "state == failed",
					"reason": "error.message", "every_secs": 1, "max_secs": 30},
			},
			"output":   map[string]any{"image": map[string]any{"path": "outputs[type=image].url", "as": "url"}},
			"defaults": map[string]any{"width": 64, "height": 64},
			"cost":     map[string]any{"per_call": 0.04, "from_reply": "usage.cost_usd"},
		}},
	}
}

func specFrom(t *testing.T, v map[string]any) (Spec, error) {
	t.Helper()
	b, _ := json.Marshal(v)
	s, err := Parse(b)
	if err != nil {
		return s, err
	}
	return s, s.Validate()
}

func TestAValidServiceSpecPasses(t *testing.T) {
	if _, err := specFrom(t, imageSpec("https://api.example.test")); err != nil {
		t.Fatal(err)
	}
}

// A spec that names something it does not mean is refused at save, with
// what is wrong, rather than accepted and silently not applied.
func TestAServiceSpecIsRefusedForWhatItGetsWrong(t *testing.T) {
	cases := []struct {
		name string
		edit func(m map[string]any)
		want string
	}{
		{"an unknown field", func(m map[string]any) { m["providr"] = 1 }, "unknown field"},
		{"an unknown capability", func(m map[string]any) { binding(m)["capability"] = "image.paint" }, "unknown capability"},
		{"a transport not built yet", func(m map[string]any) { provider(m)["transport"] = "mcp" }, "stage 6"},
		{"an http service with network", func(m map[string]any) { provider(m)["network"] = true }, "for the command transport"},
		{"run lines on an http service", func(m map[string]any) { op(m, "models")["run"] = []any{"echo x"} }, "for the command transport"},
		{"a stream send not built yet", func(m map[string]any) { op(m, "submit")["send"] = "stream" }, "stage 5"},
		{"multipart with a file reply", func(m map[string]any) { op(m, "submit")["send"] = "multipart"; op(m, "submit")["read"] = "file" }, "not supported yet"},
		{"a file send of a text input", func(m map[string]any) { op(m, "submit")["send"] = "file"; op(m, "submit")["body"] = "{prompt}" }, "not one of image.generate's file inputs"},
		{"a file send that is not one token", func(m map[string]any) { op(m, "submit")["send"] = "file"; op(m, "submit")["body"] = "x{prompt}" }, "one file input's token"},
		{"a path naming a variable nothing gives", func(m map[string]any) {
			binding(m)["output"] = map[string]any{"image": map[string]any{"path": "outputs.{job}.url", "as": "url"}}
		}, "{job}"},
		{"a stream read not built yet", func(m map[string]any) { op(m, "status")["read"] = "stream:sse" }, "stage 5"},
		{"auth in a header", func(m map[string]any) { provider(m)["headers"] = map[string]any{"Authorization": "Bearer x"} }, "comes from the credential"},
		{"no auth said either way", func(m map[string]any) { provider(m)["auth"] = map[string]any{} }, "auth needs"},
		{"a token nothing gives", func(m map[string]any) { op(m, "status")["path"] = "/v1/jobs/{job_ref}" }, "{job_ref}"},
		{"a step naming no operation", func(m map[string]any) { steps(m)[1].(map[string]any)["wait"] = "poll" }, "not an operation"},
		{"a wait with no until", func(m map[string]any) { delete(steps(m)[1].(map[string]any), "until") }, "no until"},
		{"a broken path", func(m map[string]any) {
			binding(m)["output"] = map[string]any{"image": map[string]any{"path": "outputs[].url", "as": "url"}}
		}, "selector"},
		{"a file output read as text", func(m map[string]any) {
			binding(m)["output"] = map[string]any{"image": map[string]any{"path": "a", "as": "text"}}
		}, "as must be one of base64, url"},
		{"no image output", func(m map[string]any) { binding(m)["output"] = map[string]any{} }, "required"},
		{"a cost unit the contract does not count", func(m map[string]any) {
			binding(m)["cost"] = map[string]any{"per_unit": 0.01, "unit": "second"}
		}, "not one image.generate counts"},
		{"a health check too often", func(m map[string]any) { provider(m)["health"].(map[string]any)["every_mins"] = 1 }, "at least 5"},
		{"a health check that needs an input", func(m map[string]any) { provider(m)["health"].(map[string]any)["call"] = "status" }, "has no inputs"},
		{"a default for no input", func(m map[string]any) { binding(m)["defaults"] = map[string]any{"style": "x"} }, "not one of"},
	}
	for _, c := range cases {
		m := imageSpec("https://api.example.test")
		c.edit(m)
		_, err := specFrom(t, m)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error with %q", c.name, err, c.want)
		}
	}
}

func provider(m map[string]any) map[string]any { return m["provider"].(map[string]any) }
func op(m map[string]any, name string) map[string]any {
	return provider(m)["operations"].(map[string]any)[name].(map[string]any)
}
func binding(m map[string]any) map[string]any { return m["provides"].([]any)[0].(map[string]any) }
func steps(m map[string]any) []any            { return binding(m)["steps"].([]any) }

// A body template keeps a value's type when a token is the whole string, and
// writes it into a longer one.
func TestABodyTemplateKeepsTypes(t *testing.T) {
	got := renderValue(map[string]any{"prompt": "{prompt}", "size": "{width}x{height}", "w": "{width}", "keep": []any{"{seed}"}},
		map[string]any{"prompt": `say "hi"`, "width": 64, "height": 32, "seed": 7})
	b, _ := json.Marshal(got)
	want := `{"keep":[7],"prompt":"say \"hi\"","size":"64x32","w":64}`
	if string(b) != want {
		t.Errorf("got %s\nwant %s", b, want)
	}
}

func TestConditions(t *testing.T) {
	var node any
	json.Unmarshal([]byte(`{"state":"Done","n":0,"err":""}`), &node)
	for s, want := range map[string]bool{
		"state == done":        true,
		"state == queued|done": true,
		"state != running":     true,
		"state != done":        false,
		"state":                true,
		"err":                  false,
		"missing != x":         false,
	} {
		c, err := parseCond(s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if c.holds(node, nil) != want {
			t.Errorf("%q holds = %v, want %v", s, !want, want)
		}
	}
	for _, bad := range []string{"state ==", "a b", "x[].z == 1"} {
		if _, err := parseCond(bad); err == nil {
			t.Errorf("%q passed as a condition", bad)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// validateSpec parses and checks a spec as saving it does.
func validateSpec(t *testing.T, spec map[string]any) error {
	s, err := Parse([]byte(mustJSON(t, spec)))
	if err != nil {
		return err
	}
	return s.Validate()
}

// A bedrock binding says which API; a Bedrock model with no secret cannot
// sign, and another dialect takes no api.
func TestTheBedrockDialectIsChecked(t *testing.T) {
	spec := templateSpec(t, "bedrock-model", "https://bedrock-runtime.us-east-1.amazonaws.com")
	if err := validateSpec(t, spec); err == nil || !strings.Contains(err.Error(), "Bedrock signs every request") {
		t.Errorf("no credential: %v", err)
	}
	spec["provider"].(map[string]any)["auth"] = map[string]any{"secret": "aws_keys"}
	if err := validateSpec(t, spec); err != nil {
		t.Errorf("the template: %v", err)
	}
	b := spec["provides"].([]any)[0].(map[string]any)
	b["defaults"].(map[string]any)["api"] = "chat"
	if err := validateSpec(t, spec); err == nil || !strings.Contains(err.Error(), "converse, messages and invoke") {
		t.Errorf("an api Bedrock lacks: %v", err)
	}
	b["defaults"].(map[string]any)["api"] = "converse"
	b["dialect"] = "openai"
	spec["provider"].(map[string]any)["base_url"] = "https://api.example.com/v1"
	if err := validateSpec(t, spec); err == nil || !strings.Contains(err.Error(), "model and context") {
		t.Errorf("api on another dialect: %v", err)
	}
}
