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

// The send templates post what their APIs take and read the message's id
// back: Slack to its channel (a refusal Slack answers with 200 is an error,
// with Slack's reason), Discord to its channel's path, ntfy as JSON to its
// topic, and ntfy with a file as the request's whole body.
func TestTheSendTemplatesSend(t *testing.T) {
	world(t)
	var got []string
	refuse := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if len(b) > 4 && string(b[1:4]) == "PNG" {
			b = []byte("<png>")
		}
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+string(b))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case refuse:
			w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
		case strings.HasPrefix(r.URL.Path, "/api/chat"):
			w.Write([]byte(`{"ok":true,"ts":"1700.01"}`))
		default:
			w.Write([]byte(`{"id":"m-7"}`))
		}
	}))
	defer srv.Close()
	for _, id := range []string{"slack-send", "discord-send", "ntfy-send", "ntfy-send-file"} {
		install(t, id, templateSpec(t, id, srv.URL))
	}
	pic, _ := samplePicture()
	for _, c := range []struct {
		id, want, out string
		in            map[string]any
	}{
		{"slack-send", `POST /api/chat.postMessage {"channel":"C42","text":"Rates rose"}`, "1700.01", map[string]any{"text": "Rates rose", "to": "C42"}},
		{"discord-send", `POST /api/v10/channels/9001/messages {"content":"Rates rose"}`, "m-7", map[string]any{"text": "Rates rose", "to": "9001"}},
		{"ntfy-send", `POST / {"message":"Rates rose","topic":"news-x7"}`, "m-7", map[string]any{"text": "Rates rose", "to": "news-x7"}},
		{"ntfy-send-file", `PUT /news-x7?message=Rates+rose+%26+fell <png>`, "m-7", map[string]any{"text": "Rates rose & fell", "to": "news-x7", "file": pic}},
	} {
		got = nil
		res, err := Call(context.Background(), nil, c.id, "message.send", c.in)
		if err != nil || len(got) != 1 || got[0] != c.want || res["id"].Text != c.out {
			t.Errorf("%s: %v %q %v\nwant %q", c.id, err, got, res, c.want)
		}
	}
	refuse = true
	if _, err := Call(context.Background(), nil, "slack-send", "message.send", map[string]any{"text": "x", "to": "C0"}); err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("a Slack refusal: %v", err)
	}
}
