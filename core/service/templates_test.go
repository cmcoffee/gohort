package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// templateSpec is the service spec a built-in template installs, its
// questions answered with their defaults, pointed at base with no key: the
// request and reply shapes are what is under test, not the provider.
func templateSpec(t *testing.T, id, base string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../recipes/builtin/" + id + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var tmpl struct {
		Questions []struct{ Name, Default string } `json:"questions"`
		Bundle    struct {
			Artifacts []struct {
				Type   string          `json:"type"`
				Recipe json.RawMessage `json:"recipe"`
			} `json:"artifacts"`
		} `json:"bundle"`
	}
	json.Unmarshal(raw, &tmpl)
	for _, a := range tmpl.Bundle.Artifacts {
		if a.Type != "connector" {
			continue
		}
		text := string(a.Recipe)
		for _, q := range tmpl.Questions {
			text = strings.ReplaceAll(text, "{{"+q.Name+"}}", q.Default)
		}
		var rec struct {
			Spec map[string]any `json:"spec"`
		}
		json.Unmarshal([]byte(text), &rec)
		p := rec.Spec["provider"].(map[string]any)
		p["base_url"], p["auth"] = base, map[string]any{"none": true}
		return rec.Spec
	}
	t.Fatalf("%s has no connector", id)
	return nil
}

// Gemini's picture comes back among text parts; a wide picture asks for 16:9.
func TestTheGeminiTemplateReadsItsReply(t *testing.T) {
	world(t)
	img := pngBytes(t)
	var path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			w.Write([]byte(`{"models":[{"name":"m"}]}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		path, body = r.URL.Path, string(b)
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"Here is your picture."},{"inlineData":{"mimeType":"image/png","data":"` + b64(img) + `"}}]}}]}`))
	}))
	defer srv.Close()
	install(t, "gemini-images", templateSpec(t, "gemini-images", srv.URL))
	res, err := GenerateImageWithBackend(context.Background(), "gemini-images", "a lighthouse", true)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(res.URL); string(got) != string(img) {
		t.Error("the picture is not the one in the reply")
	}
	if path != "/v1beta/models/gemini-2.5-flash-image:generateContent" || !strings.Contains(body, `"aspectRatio":"16:9"`) || !strings.Contains(body, `"text":"a lighthouse"`) {
		t.Errorf("request: %s %s", path, body)
	}
	if h, err := CheckHealth(context.Background(), "gemini-images"); err != nil || !h.OK {
		t.Errorf("health: %+v %v", h, err)
	}
}

// OpenAI's wide picture is 1792x1024, a square one 1024x1024.
func TestTheOpenAITemplateReadsItsReply(t *testing.T) {
	world(t)
	img := pngBytes(t)
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			w.Write([]byte(`{"data":[{"id":"dall-e-3"}]}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Write([]byte(`{"data":[{"b64_json":"` + b64(img) + `"}]}`))
	}))
	defer srv.Close()
	install(t, "openai-images", templateSpec(t, "openai-images", srv.URL))
	for _, wide := range []bool{true, false} {
		if _, err := GenerateImageWithBackend(context.Background(), "openai-images", "a lighthouse", wide); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 2 || !strings.Contains(bodies[0], `"size":"1792x1024"`) || !strings.Contains(bodies[1], `"size":"1024x1024"`) || !strings.Contains(bodies[0], `"model":"dall-e-3"`) {
		t.Errorf("requests: %v", bodies)
	}
	if h, err := CheckHealth(context.Background(), "openai-images"); err != nil || !h.OK {
		t.Errorf("health: %+v %v", h, err)
	}
}
