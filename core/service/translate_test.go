package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func restImageConnector(t *testing.T, preset string, base string) Connector {
	t.Helper()
	spec, err := ApplyRestImagePreset(preset, RestImageSpec{Credential: "no_auth", PromptSuffix: "high detail"}, map[string]string{"base_url": base})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(spec)
	return Connector{Name: preset + "-old", Kind: RestImageConnectorKind, Spec: raw}
}

// An Automatic1111 backend reads as one synchronous call: its unquoted
// number tokens keep their types, the prompt suffix rides on the prompt, and
// the seed defaults to random as it did.
func TestAnA1111BackendTranslatesAndRuns(t *testing.T) {
	world(t)
	img := pngBytes(t)
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(strings.Builder)
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		b.Write(buf[:n])
		body = b.String()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"images":["` + b64(img) + `"]}`))
	}))
	defer srv.Close()
	s, notes, err := FromRestImage(restImageConnector(t, "a1111", srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Errorf("notes: %v", notes)
	}
	out, err := s.run(context.Background(), nil, "image.generate", map[string]any{"prompt": "a fox"})
	if err != nil {
		t.Fatal(err)
	}
	if out.result["image"].File == nil {
		t.Fatal("no image")
	}
	for _, want := range []string{`"prompt":"a fox, high detail"`, `"width":512`, `"steps":20`, `"seed":-1`, `"negative_prompt":""`} {
		if !strings.Contains(body, want) {
			t.Errorf("the request lacks %s: %s", want, body)
		}
	}
}

// A ComfyUI backend reads as submit, wait, fetch: the finished poll's fields
// are saved (an empty subfolder included) and the picture fetched from the
// URL they build.
func TestAComfyUIBackendTranslatesAndRuns(t *testing.T) {
	world(t)
	img := pngBytes(t)
	var mu sync.Mutex
	polls := 0
	var viewed string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/prompt":
			w.Write([]byte(`{"prompt_id":"p1"}`))
		case "/history/p1":
			polls++
			if polls == 1 {
				w.Write([]byte(`{}`))
				return
			}
			w.Write([]byte(`{"p1":{"outputs":{"9":{"images":[{"filename":"g.png","subfolder":"","type":"output"}]}}}}`))
		case "/view":
			viewed = r.URL.RawQuery
			w.Header().Set("Content-Type", "image/png")
			w.Write(img)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, _, err := FromRestImage(restImageConnector(t, "comfyui", srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(s.Provides[0].Steps); n != 3 {
		t.Fatalf("%d steps, want submit, wait, view", n)
	}
	if _, err := s.run(context.Background(), nil, "image.generate", map[string]any{"prompt": "a fox"}); err != nil {
		t.Fatal(err)
	}
	if viewed != "filename=g.png&subfolder=&type=output" {
		t.Errorf("view query: %q", viewed)
	}
}

// What a Go helper does stays its own kind, and says why.
func TestWhatOnlyAHelperDoesStaysItsOwnKind(t *testing.T) {
	c := restImageConnector(t, "a1111_img2img", "http://lan.test:7860")
	if _, _, err := FromRestImage(c); err == nil || !strings.Contains(err.Error(), "inline") {
		t.Errorf("an inline-image backend: %v", err)
	}
	spec := RestImageSpec{Credential: "no_auth", SubmitURL: "http://lan.test/prompt", ComfyWorkflow: `{"1":{}}`, ImageB64Path: "x"}
	raw, _ := json.Marshal(spec)
	if _, _, err := FromRestImage(Connector{Kind: RestImageConnectorKind, Spec: raw}); err == nil || !strings.Contains(err.Error(), "ComfyUI workflow") {
		t.Errorf("a pasted workflow: %v", err)
	}
}
