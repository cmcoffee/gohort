package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/migrate"
)

// keepImageProvider points the image setting hook at the test's store, and
// forgets the credentials a move adds: the credential store outlives a test.
func keepImageProvider(t *testing.T) {
	forgetCredentials(t, "gemini_images", "gemini_images_2", "openai_images")
	saved := ImageProviderFunc
	ImageProviderFunc = func() string {
		var p string
		RootDB.Get(ImageTable, "provider", &p)
		return p
	}
	t.Cleanup(func() { ImageProviderFunc = saved })
}

// forgetCredentials deletes the named credentials when the test ends.
func forgetCredentials(t *testing.T, names ...string) {
	t.Cleanup(func() {
		for _, n := range names {
			Secure().Delete(n)
		}
	})
}

// pointAt points the moved connector conn, and the credential it names, at
// base: a test server standing in for the provider.
func pointAt(t *testing.T, conn, base string) {
	t.Helper()
	c, _ := GetConnector(RootDB, conn)
	var spec map[string]any
	json.Unmarshal(c.Spec, &spec)
	p := spec["provider"].(map[string]any)
	p["base_url"] = base + "/"
	c.Spec, _ = json.Marshal(spec)
	if err := SaveConnector(RootDB, c); err != nil {
		t.Fatal(err)
	}
	ApproveConnector(RootDB, conn)
	auth, _ := p["auth"].(map[string]any)
	credName, _ := auth["credential"].(string)
	if credName == "" {
		return
	}
	cred, ok := Secure().Load(credName)
	if !ok {
		t.Fatalf("no credential %q", credName)
	}
	cred.BaseURL = base
	if err := Secure().Save(cred, ""); err != nil { // a blank secret keeps the stored one
		t.Fatal(err)
	}
}

// imageAPI is Gemini's or OpenAI's image API, keeping the key each request
// came with, and the moved connector and credential pointed at it: the key
// the migration stored is the one that arrives.
func imageAPI(t *testing.T, conn, header string) *[]string {
	t.Helper()
	img := pngBytes(t)
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get(header))
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "generateContent") {
			w.Write([]byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + b64(img) + `"}}]}}]}`))
			return
		}
		w.Write([]byte(`{"data":[{"b64_json":"` + b64(img) + `"}]}`))
	}))
	t.Cleanup(srv.Close)
	pointAt(t, conn, srv.URL)
	return &keys
}

// Image Generation set to the Gemini code with a key of its own becomes the
// Gemini template, installed and approved with that key in its credential;
// the setting names it, the key row is gone, and a picture is made through
// it. Run again, it changes nothing.
func TestImagesFromGeminiBecomeAConnector(t *testing.T) {
	world(t)
	keepImageProvider(t)
	RootDB.Set(ImageTable, "provider", "gemini")
	RootDB.Set(ImageTable, "api_key", "g-image-key")
	RootDB.Set(LLMTable, "provider", "gemini")
	RootDB.Set(LLMTable, "api_key", "g-llm-key")

	MigrateImages(RootDB)

	var provider, key string
	RootDB.Get(ImageTable, "provider", &provider)
	if provider != "gemini-images" || RootDB.Get(ImageTable, "api_key", &key) {
		t.Fatalf("the setting after: %q, key row kept %v", provider, key != "")
	}
	c, ok := GetConnector(RootDB, "gemini-images")
	if !ok || !c.Approved || c.Template != "gemini-images" || !Provides(c, "image.generate") || !strings.Contains(string(c.Spec), `"credential":"gemini_images"`) {
		t.Fatalf("the connector: %+v %v", c, ok)
	}
	cred, ok := Secure().Load("gemini_images")
	if !ok || cred.Disabled || cred.Type != SecureCredHeader || cred.ParamName != "x-goog-api-key" {
		t.Fatalf("the credential: %+v %v", cred, ok)
	}
	if !ImageGenerationAvailable() || !ImageBackendRegistered("gemini-images") {
		t.Fatal("the moved connector is not the image backend")
	}
	keys := imageAPI(t, "gemini-images", "x-goog-api-key")
	res, err := GenerateImage(context.Background(), "a lighthouse")
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(res.URL)
	if len(*keys) != 1 || (*keys)[0] != "g-image-key" {
		t.Errorf("the key sent: %v, want the image setting's own", *keys)
	}
	var m migrate.MigrationMarker
	if !RootDB.Get(migrate.MigrationsTable, "core:images_to_connector:v1:", &m) || !m.Done || m.Error != "" || m.Changed != 3 {
		t.Errorf("Admin > Migrations shows: %+v", m)
	}

	RootDB.Set(ImageTable, "provider", "gemini")
	RootDB.Set(ImageTable, "api_key", "g-image-key")
	MigrateImages(RootDB)
	if _, again := GetConnector(RootDB, "gemini-images-2"); again {
		t.Error("the migration ran twice")
	}
}

func mustSpec(t *testing.T, spec map[string]any) json.RawMessage {
	return json.RawMessage(mustJSON(t, spec))
}

// A blank setting meant Gemini, and a setting with no key of its own used
// the key of an LLM tier on the same provider, the worker's first, then the
// lead's. The names taken already are left alone: the move takes the next.
func TestImagesWithAnLLMsKeyMove(t *testing.T) {
	world(t)
	keepImageProvider(t)
	RootDB.Set(LLMTable, "provider", "anthropic")
	RootDB.Set(LLMTable, "api_key", "a-key")
	RootDB.Set(LeadLLMTable, "provider", "gemini")
	RootDB.Set(LeadLLMTable, "api_key", "g-lead-key")
	if err := SaveConnector(RootDB, Connector{Name: "gemini-images", Kind: Kind, Spec: mustSpec(t, templateSpec(t, "gemini-images", "https://other.example"))}); err != nil {
		t.Fatal(err)
	}
	if err := Secure().Save(SecureCredential{Name: "gemini_images", Type: SecureCredBearer, BaseURL: "https://other.example"}, "theirs"); err != nil {
		t.Fatal(err)
	}

	if changed, err := migrateImages(RootDB); err != nil || changed != 3 {
		t.Fatalf("migrate: %d %v", changed, err)
	}
	var provider string
	RootDB.Get(ImageTable, "provider", &provider)
	c, ok := GetConnector(RootDB, "gemini-images-2")
	if provider != "gemini-images-2" || !ok || !strings.Contains(string(c.Spec), `"credential":"gemini_images_2"`) {
		t.Fatalf("setting %q, connector %+v", provider, c)
	}
	keys := imageAPI(t, "gemini-images-2", "x-goog-api-key")
	if _, err := GenerateImageWithBackend(context.Background(), "", "a lighthouse", false); err != nil {
		t.Fatal(err)
	}
	if len(*keys) != 1 || (*keys)[0] != "g-lead-key" {
		t.Errorf("the key sent: %v, want the Gemini lead LLM's", *keys)
	}
}

// OpenAI moves to its template, with the worker LLM's key when it is OpenAI.
func TestImagesFromOpenAIBecomeAConnector(t *testing.T) {
	world(t)
	keepImageProvider(t)
	RootDB.Set(ImageTable, "provider", "openai")
	RootDB.Set(LLMTable, "provider", "openai")
	RootDB.Set(LLMTable, "api_key", "o-worker-key")
	RootDB.Set(LeadLLMTable, "provider", "openai")
	RootDB.Set(LeadLLMTable, "api_key", "o-lead-key")

	if changed, err := migrateImages(RootDB); err != nil || changed != 3 {
		t.Fatalf("migrate: %d %v", changed, err)
	}
	cred, _ := Secure().Load("openai_images")
	if cred.Type != SecureCredBearer || cred.Disabled {
		t.Errorf("the credential: %+v", cred)
	}
	keys := imageAPI(t, "openai-images", "Authorization")
	if _, err := GenerateImage(context.Background(), "a lighthouse"); err != nil {
		t.Fatal(err)
	}
	if len(*keys) != 1 || (*keys)[0] != "Bearer o-worker-key" {
		t.Errorf("the key sent: %v, want the worker LLM's", *keys)
	}
}

// A provider with no key anywhere made no pictures: named, it is turned off;
// blank, it is left alone. A setting that is off or names a connector is
// left alone, and none of these adds a connector.
func TestImagesWithNothingToMove(t *testing.T) {
	world(t)
	keepImageProvider(t)
	RootDB.Set(LLMTable, "provider", "gemini") // a key on another provider's LLM is not taken
	RootDB.Set(LeadLLMTable, "provider", "openai")
	RootDB.Set(LeadLLMTable, "api_key", "o-lead-key")
	if changed, err := migrateImages(RootDB); err != nil || changed != 0 {
		t.Errorf("blank with no Gemini key: %d %v", changed, err)
	}
	RootDB.Set(ImageTable, "provider", "gemini")
	if changed, err := migrateImages(RootDB); err != nil || changed != 1 {
		t.Errorf("gemini with no key: %d %v", changed, err)
	}
	var provider string
	if RootDB.Get(ImageTable, "provider", &provider); provider != "" || ImageGenerationAvailable() {
		t.Errorf("gemini with no key is left as %q", provider)
	}
	for _, set := range []string{"none", "comfy_lan", "peer-den-sdxl"} {
		RootDB.Set(ImageTable, "provider", set)
		RootDB.Set(ImageTable, "api_key", "stale")
		if changed, err := migrateImages(RootDB); err != nil || changed != 0 {
			t.Errorf("%s was moved: %d %v", set, changed, err)
		}
		if RootDB.Get(ImageTable, "provider", &provider); provider != set {
			t.Errorf("%s became %q", set, provider)
		}
	}
	if n := len(ListConnectors(RootDB)); n != 0 {
		t.Errorf("%d connectors added", n)
	}
}
