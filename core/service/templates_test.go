package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/recipes"
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

// OpenAI's wide picture is 1536x1024, a square one 1024x1024, from a GPT
// image model, which refuses response_format (it always answers in base64).
func TestTheOpenAITemplateReadsItsReply(t *testing.T) {
	world(t)
	img := pngBytes(t)
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			w.Write([]byte(`{"data":[{"id":"gpt-image-2.5-sunburst"}]}`))
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
	if len(bodies) != 2 || !strings.Contains(bodies[0], `"size":"1536x1024"`) || !strings.Contains(bodies[1], `"size":"1024x1024"`) || !strings.Contains(bodies[0], `"model":"gpt-image-2.5-sunburst"`) {
		t.Errorf("requests: %v", bodies)
	}
	if strings.Contains(bodies[0], "response_format") || strings.Contains(bodies[0], "quality") {
		t.Errorf("a GPT image model refuses these: %s", bodies[0])
	}
	if h, err := CheckHealth(context.Background(), "openai-images"); err != nil || !h.OK {
		t.Errorf("health: %+v %v", h, err)
	}
}

// OpenAI's template edits and blends too: the pictures given go as data URLs
// in a JSON edit, only those given, up to its Pictures per blend; doing both
// jobs, it is offered for both until its row's Used for says otherwise.
func TestTheOpenAITemplateEditsAndBlends(t *testing.T) {
	world(t)
	img := pngBytes(t)
	var path string
	var body struct {
		Model  string              `json:"model"`
		Prompt string              `json:"prompt"`
		Images []map[string]string `json:"images"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path = r.URL.Path
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"data":[{"b64_json":"` + b64(img) + `"}]}`))
	}))
	defer srv.Close()
	install(t, "openai-images", templateSpec(t, "openai-images", srv.URL))
	if n := editLimit("openai-images"); n != 10 {
		t.Errorf("it blends %d pictures, want 10", n)
	}
	RootDB.Set(ImageTable, "agents", []string{"openai-images"})
	offered := false
	for _, b := range ReachableImageBackends(&ToolSession{}) {
		if b.Name == "openai-images" {
			offered = b.Edits && b.Generates
		}
	}
	if !offered {
		t.Errorf("not offered for both jobs: %+v", ReachableImageBackends(&ToolSession{}))
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.png"), img, 0o600)
	os.WriteFile(filepath.Join(dir, "b.png"), img, 0o600)
	res, err := EditImageWithBackend(&ToolSession{WorkspaceDir: dir}, EditImageRequest{Backend: "openai-images", Prompt: "put them side by side", Images: []string{"a.png", "b.png"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(res.URL); string(got) != string(img) {
		t.Error("the picture is not the one in the reply")
	}
	os.Remove(res.URL)
	if path != "/v1/images/edits" || body.Model != "gpt-image-2.5-sunburst" || body.Prompt != "put them side by side" {
		t.Errorf("request: %s %+v", path, body)
	}
	want := "data:image/png;base64," + b64(img)
	if len(body.Images) != 2 || body.Images[0]["image_url"] != want || body.Images[1]["image_url"] != want {
		t.Errorf("sent %d pictures, want the two given as data URLs: %.200v", len(body.Images), body.Images)
	}
}

// ElevenLabs' music template composes a song from the prompt with the
// model its row sets, and reads the reply as the song; its health check
// asks who the key belongs to.
func TestTheElevenLabsMusicTemplateComposes(t *testing.T) {
	world(t)
	var path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/v1/user" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"user_id":"u","subscription":{"tier":"creator"}}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		path, body = r.URL.Path, string(b)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write(ttsClip)
	}))
	defer srv.Close()
	install(t, "elevenlabs-music", templateSpec(t, "elevenlabs-music", srv.URL))
	res, err := Call(context.Background(), &ToolSession{WorkspaceDir: t.TempDir()}, "elevenlabs-music", "music.generate", map[string]any{"prompt": "a calm piano piece, about a minute"})
	if err != nil {
		t.Fatal(err)
	}
	if v := res["audio"]; v.File == nil || v.File.Size == 0 {
		t.Fatalf("no song: %+v", res)
	}
	if path != "/v1/music" || !strings.Contains(body, `"prompt":"a calm piano piece, about a minute"`) || !strings.Contains(body, `"model_id":"music_v2_5"`) {
		t.Errorf("request: %s %s", path, body)
	}
	if h, err := CheckHealth(context.Background(), "elevenlabs-music"); err != nil || !h.OK {
		t.Errorf("health: %+v %v", h, err)
	}
}

// Gemini Omni Flash answers the request when the video is made, with a link
// to it (asked for by link: inline replies stop at 4MB), and the connector
// downloads it. The link is read where the REST docs put it (steps) or where
// their example reads it (output_video), whole or under the API's address;
// a download that fails is refused rather than kept as a video.
func TestTheGeminiVideoTemplateDownloadsItsVideo(t *testing.T) {
	world(t)
	clip := []byte("\x00\x00\x00\x18ftypmp42 a short clip")
	var created string
	reply, ready := "", true
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1beta/interactions":
			b, _ := io.ReadAll(r.Body)
			created = string(b)
			w.Write([]byte(reply))
		case r.URL.Path == "/v1beta/files/abc123:download" && r.URL.Query().Get("alt") == "media" && ready:
			w.Header().Set("Content-Type", "video/mp4")
			w.Write(clip)
		default:
			http.Error(w, `{"error":{"message":"no such file"}}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	spec := templateSpec(t, "gemini-video", srv.URL)
	spec["provider"].(map[string]any)["base_url"] = srv.URL + "/v1beta/"
	install(t, "gemini-video", spec)
	run := func() (Result, error) {
		return Call(context.Background(), &ToolSession{WorkspaceDir: t.TempDir()}, "gemini-video", "media.generate", map[string]any{"prompt": "a marble rolling down a track"})
	}
	for _, at := range []string{
		`{"steps":[{"type":"user_input","content":[{"type":"text","text":"x"}]},{"type":"model_output","content":[{"type":"video","mime_type":"video/mp4","uri":"` + srv.URL + `/v1beta/files/abc123:download?alt=media"}]}],"status":"completed"}`,
		`{"output_video":{"uri":"files/abc123:download?alt=media"},"status":"completed"}`,
	} {
		reply = at
		res, err := run()
		if err != nil {
			t.Fatalf("%s: %v", at, err)
		}
		if got, _ := os.ReadFile(res["file"].File.Path); string(got) != string(clip) {
			t.Errorf("%s: the video is not the one downloaded", at)
		}
	}
	for _, want := range []string{`"model":"gemini-omni-1.2-flash"`, `"delivery":"uri"`, `"duration":"10s"`, `"aspect_ratio":"16:9"`, `"resolution":"720p"`} {
		if !strings.Contains(created, want) {
			t.Errorf("the request has no %s: %s", want, created)
		}
	}
	type part struct{ Type, Text, Data, MimeType string }
	parts := func() []part {
		var body struct {
			Input []map[string]string `json:"input"`
		}
		if err := json.Unmarshal([]byte(created), &body); err != nil {
			t.Fatalf("the request: %v %s", err, created)
		}
		var out []part
		for _, p := range body.Input {
			out = append(out, part{p["type"], p["text"], p["data"], p["mime_type"]})
		}
		return out
	}
	// With no pictures, the prompt alone: no empty picture parts.
	if got := parts(); len(got) != 1 || got[0].Type != "text" || got[0].Text != "a marble rolling down a track" {
		t.Errorf("a prompt alone sent %+v", got)
	}
	// Reference pictures go first, in order, each with its type.
	png := pngBytes(t)
	dir := t.TempDir()
	var files []*File
	for i, data := range [][]byte{png, []byte("\xff\xd8\xff\xe0 a jpeg")} {
		path := filepath.Join(dir, fmt.Sprintf("ref%d", i))
		os.WriteFile(path, data, 0o600)
		files = append(files, &File{Path: path, Type: []string{"image/png", "image/jpeg"}[i], Size: int64(len(data))})
	}
	if _, err := Call(context.Background(), &ToolSession{WorkspaceDir: t.TempDir()}, "gemini-video", "media.generate",
		map[string]any{"prompt": "the cat chases the yarn", "file": files[0], "file2": files[1]}); err != nil {
		t.Fatal(err)
	}
	got := parts()
	if len(got) != 3 || got[0].Type != "image" || got[0].MimeType != "image/png" || got[0].Data != b64(png) ||
		got[1].Type != "image" || got[1].MimeType != "image/jpeg" || got[2].Type != "text" || got[2].Text != "the cat chases the yarn" {
		t.Errorf("two references and a prompt sent %+v", got)
	}
	ready = false
	if _, err := run(); err == nil {
		t.Error("a download that failed came back as a video")
	}
}

// The search templates ask what their APIs take and read each one's results
// into the contract's title, url and snippet; Search gives them in the
// numbered shape the web_search tool reads. A reply with no results is no
// results, but a contract test must find some, and a result read with no
// web address fails the check: either is a spec reading the wrong place.
func TestTheSearchTemplatesSearch(t *testing.T) {
	world(t)
	var got []string
	empty, noURL := false, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+string(b))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case empty:
			w.Write([]byte(`{"type":"search"}`))
		case noURL:
			w.Write([]byte(`{"organic":[{"title":"A","snippet":"no link"}]}`))
		case strings.HasPrefix(r.URL.Path, "/res/"):
			w.Write([]byte(`{"web":{"results":[{"title":"Gohort","url":"https://a.example/1","description":"First."},{"title":"Two","url":"https://a.example/2","description":"Second."}]}}`))
		case r.URL.Path == "/search" && strings.Contains(string(b), `"q"`):
			w.Write([]byte(`{"organic":[{"title":"Gohort","link":"https://a.example/1","snippet":"First."}]}`))
		default:
			w.Write([]byte(`{"results":[{"title":"Gohort","url":"https://a.example/1","content":"First."}]}`))
		}
	}))
	defer srv.Close()
	for _, id := range []string{"brave-search", "serper-search", "tavily-search"} {
		install(t, id, templateSpec(t, id, srv.URL))
	}
	for id, want := range map[string]string{
		"brave-search":  `GET /res/v1/web/search?q=rust+%26+go&count=10 `,
		"serper-search": `POST /search {"num":10,"q":"rust \u0026 go"}`,
		"tavily-search": `POST /search {"max_results":5,"query":"rust \u0026 go"}`,
	} {
		got = nil
		out, err := Search(context.Background(), id, "rust & go")
		if err != nil || len(got) != 1 || got[0] != want || !strings.HasPrefix(out, "1. Gohort\n   https://a.example/1\n   First.") {
			t.Errorf("%s: %v %q\n%s\nwant request %q", id, err, got, out, want)
		}
	}
	if out, _ := Search(context.Background(), "brave-search", "x"); !strings.HasSuffix(out, "\n\n2. Two\n   https://a.example/2\n   Second.") {
		t.Errorf("two results: %q", out)
	}
	empty = true
	if out, err := Search(context.Background(), "brave-search", "x"); err != nil || out != "No results found." {
		t.Errorf("no results: %q %v", out, err)
	}
	if reps, err := Test(context.Background(), "brave-search"); err != nil || reps[0].OK || !strings.Contains(reps[0].Detail, "found nothing") {
		t.Errorf("a test that finds nothing: %+v %v", reps, err)
	}
	empty, noURL = false, true
	if _, err := Search(context.Background(), "serper-search", "x"); err == nil || !strings.Contains(err.Error(), "no web address") {
		t.Errorf("a result with no address: %v", err)
	}
	noURL = false
	if reps, err := Test(context.Background(), "serper-search"); err != nil || !reps[0].OK {
		t.Errorf("serper's contract test: %+v %v", reps, err)
	}
}

// The page templates ask their readers for a page and read its text and
// title back; a page goes through the same seam the browser does, so only
// a public web address is sent, and the text is cut to what the caller
// asked for. A reply with no text fails the check.
func TestThePageTemplatesRender(t *testing.T) {
	world(t)
	var got []string
	blank := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get("Accept")+" "+string(b))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case blank:
			w.Write([]byte(`{"data":{"title":"Empty"}}`))
		case r.URL.Path == "/":
			w.Write([]byte(`{"code":200,"data":{"title":"Rates","url":"https://news.example/a","content":"Rates rose today."}}`))
		default:
			w.Write([]byte(`{"success":true,"data":{"markdown":"# Rates\n\nRates rose today.","metadata":{"title":"Rates"}}}`))
		}
	}))
	defer srv.Close()
	for _, id := range []string{"jina-reader", "firecrawl"} {
		install(t, id, templateSpec(t, id, srv.URL))
	}
	for id, c := range map[string]struct{ req, text string }{
		"jina-reader": {`POST / application/json {"url":"https://news.example/a"}`, "Rates\n\nRates rose today."},
		"firecrawl":   {`POST /v1/scrape  {"formats":["markdown"],"url":"https://news.example/a"}`, "# Rates\n\nRates rose today."},
	} {
		got = nil
		text, err := media.PageService(context.Background(), id, "https://news.example/a", 0)
		if err != nil || len(got) != 1 || got[0] != c.req || text != c.text {
			t.Errorf("%s: %q %v %q\nwant %q", id, text, err, got, c.req)
		}
	}
	if text, _ := media.PageService(context.Background(), "jina-reader", "https://news.example/a", 5); text != "Rates" {
		t.Errorf("cut to five characters: %q", text)
	}
	got = nil
	if _, err := media.PageService(context.Background(), "jina-reader", "http://192.168.1.1/admin", 0); err == nil || len(got) != 0 {
		t.Errorf("a private address was sent: %v %q", err, got)
	}
	blank = true
	if _, err := media.PageService(context.Background(), "jina-reader", "https://news.example/a", 0); err == nil || !strings.Contains(err.Error(), "no text came back") {
		t.Errorf("a page with no text: %v", err)
	}
}

// The mail templates send what their APIs take: Resend and Postmark as JSON,
// Mailgun as a form to its domain's path. A From given by the mail settings
// replaces the template's own; the test sends to the template's recipient.
func TestTheMailTemplatesSend(t *testing.T) {
	world(t)
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+string(b))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"m-1","MessageID":"pm-1","message":"Queued."}`))
	}))
	defer srv.Close()
	for _, id := range []string{"resend-mail", "postmark-mail", "mailgun-mail"} {
		install(t, id, templateSpec(t, id, srv.URL))
		c, _ := GetConnector(RootDB, id)
		settings := map[string]string{"from": "gohort@mail.example", "to": "admin@mail.example"}
		if id == "mailgun-mail" {
			settings["domain"] = "mg.mail.example"
		}
		if err := SaveSettings(c, "", settings, nil); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	for id, want := range map[string]string{
		"resend-mail":   `POST /emails {"from":"alerts@mail.example","subject":"Rates","text":"Rates rose.","to":"pat@mail.example"}`,
		"postmark-mail": `POST /email {"From":"alerts@mail.example","Subject":"Rates","TextBody":"Rates rose.","To":"pat@mail.example"}`,
		"mailgun-mail":  `POST /v3/mg.mail.example/messages from=alerts%40mail.example&subject=Rates&text=Rates+rose.&to=pat%40mail.example`,
	} {
		got = nil
		if err := media.MailService(context.Background(), id, "alerts@mail.example", "pat@mail.example", "Rates", "Rates rose."); err != nil || len(got) != 1 || got[0] != want {
			t.Errorf("%s: %v %q\nwant %q", id, err, got, want)
		}
	}
	got = nil
	if err := media.MailService(context.Background(), "resend-mail", "", "pat@mail.example", "Rates", "x"); err != nil || len(got) != 1 || !strings.Contains(got[0], `"from":"gohort@mail.example"`) {
		t.Errorf("with no From of its own, the template's: %v %q", err, got)
	}
	got = nil
	if reps, err := Test(context.Background(), "postmark-mail"); err != nil || !reps[0].OK || len(got) != 1 || !strings.Contains(got[0], `"To":"admin@mail.example"`) {
		t.Errorf("the test goes to the test recipient: %+v %v %q", reps, err, got)
	}
}

// The ConvertAPI template posts the file as a form part to its formats'
// path and reads the converted file from the reply; a System Hooks job
// reaches it through flows.ConvertService, and its contract test converts
// a sample picture.
func TestTheConvertTemplateConverts(t *testing.T) {
	world(t)
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f, h, err := r.FormFile("File")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b, _ := io.ReadAll(f)
		got = append(got, r.URL.Path+" "+filepath.Ext(h.Filename)+" "+string(b[:4]))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ConversionCost":1,"Files":[{"FileName":"out.txt","FileData":"` + b64([]byte("The text of the file.")) + `"}]}`))
	}))
	defer srv.Close()
	install(t, "convertapi", templateSpec(t, "convertapi", srv.URL))
	out, err := flows.ConvertService(context.Background(), "convertapi", []byte("%PDF-1.4 x"), "pdf", "txt")
	if err != nil || string(out) != "The text of the file." || len(got) != 1 || got[0] != "/convert/pdf/to/txt .pdf %PDF" {
		t.Errorf("%q %v %q", out, err, got)
	}
	if reps, err := Test(context.Background(), "convertapi"); err != nil || !reps[0].OK || got[1] != "/convert/png/to/jpg .png \x89PNG" {
		t.Errorf("contract test: %+v %v %q", reps, err, got)
	}
}

// The embedding templates post the texts where their servers take them and
// read the vectors from each server's own reply: Ollama's /api/embed with the
// model it pulled, and an OpenAI-compatible server's /embeddings.
func TestTheEmbeddingTemplatesEmbed(t *testing.T) {
	world(t)
	var paths, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"nomic-embed-text"}]}`))
			return
		case "/v1/models":
			w.Write([]byte(`{"data":[{"id":"embedding"}]}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		paths, bodies = append(paths, r.URL.Path), append(bodies, string(b))
		if r.URL.Path == "/api/embed" {
			w.Write([]byte(`{"embeddings":[[0.1,0.2],[0.3,0.4]]}`))
			return
		}
		w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2]},{"index":1,"embedding":[0.3,0.4]}]}`))
	}))
	defer srv.Close()
	install(t, "ollama-embeddings", templateSpec(t, "ollama-embeddings", srv.URL))
	install(t, "local-embeddings", templateSpec(t, "openai-compatible-embeddings", srv.URL+"/v1"))
	for _, name := range []string{"ollama-embeddings", "local-embeddings"} {
		vecs, err := embedVia(context.Background(), name, "", []string{"a", "b"})
		if err != nil || len(vecs) != 2 || vecs[1][1] != 0.4 {
			t.Errorf("%s: %v %v", name, vecs, err)
		}
		if h, err := CheckHealth(context.Background(), name); err != nil || !h.OK {
			t.Errorf("%s health: %+v %v", name, h, err)
		}
	}
	if len(paths) != 2 || paths[0] != "/api/embed" || paths[1] != "/v1/embeddings" ||
		!strings.Contains(bodies[0], `"model":"nomic-embed-text"`) || !strings.Contains(bodies[1], `"model":"embedding"`) || !strings.Contains(bodies[1], `"input":["a","b"]`) {
		t.Errorf("requests: %v %v", paths, bodies)
	}
}

// The local template transcribes at its server's /audio/transcriptions with
// the model it was given, and reads the text back.
func TestTheLocalTranscriptionTemplateTranscribes(t *testing.T) {
	world(t)
	srv, got := sttServer(t, "hello there")
	install(t, "local-transcription", templateSpec(t, "openai-compatible-transcription", srv.URL+"/v1"))
	text, err := transcribeVia(context.Background(), "local-transcription", []byte("audio"), "clip.m4a")
	if err != nil || text != "hello there" {
		t.Fatalf("transcribe: %q %v", text, err)
	}
	if r := (*got)[0]; r.fields["model"] != "whisper-1" || !strings.HasSuffix(r.file, ".m4a") || r.key != "" {
		t.Errorf("the request: %+v", r)
	}
}

// The local template speaks in its default voice and lists the server's
// voices; ElevenLabs lists its account's voices by name.
func TestTheSpeechTemplatesListVoices(t *testing.T) {
	world(t)
	srv, _, bodies := ttsServer(t, false)
	install(t, "local-speech", templateSpec(t, "openai-compatible-speech", srv.URL+"/v1"))
	if audio, _, err := speakVia(context.Background(), "local-speech", "Hi.", ""); err != nil || len(audio) == 0 {
		t.Fatalf("speak: %v", err)
	}
	if b := (*bodies)[0]; b["voice"] != "af_heart" || b["model"] != "kokoro" {
		t.Errorf("the request: %v", b)
	}
	if v, err := voicesVia(context.Background(), "local-speech"); err != nil || len(v) != 2 {
		t.Errorf("voices: %+v %v", v, err)
	}
	if h, err := CheckHealth(context.Background(), "local-speech"); err != nil || !h.OK {
		t.Errorf("health: %+v %v", h, err)
	}

	el := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"voices":[{"voice_id":"21m00","name":"Rachel","category":"premade"}]}`))
	}))
	defer el.Close()
	eleven := templateSpec(t, "elevenlabs-speech", el.URL)
	if !strings.Contains(mustJSON(t, eleven), `"speech.voices"`) {
		t.Fatal("the ElevenLabs template lists no voices")
	}
	install(t, "eleven", eleven)
	if v, err := voicesVia(context.Background(), "eleven"); err != nil || len(v) != 1 || v[0] != (media.Voice{ID: "21m00", Name: "Rachel"}) {
		t.Errorf("ElevenLabs voices: %+v %v", v, err)
	}
	// A connector that declares no list has none, and is not called for one.
	install(t, "plain", speakSpec("http://127.0.0.1:1/v1"))
	if v, err := voicesVia(context.Background(), "plain"); err != nil || v != nil {
		t.Errorf("a connector with no list: %+v %v", v, err)
	}
}

// Brave gives a page's age as page_age, or only as age: a field reads the
// first of its paths that has something.
func TestBraveReadsEitherAge(t *testing.T) {
	world(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"web":{"results":[{"title":"A","url":"https://a.example/1","description":"One.","page_age":"2026-09-20T10:00:00","age":"September 18, 2026"},{"title":"B","url":"https://a.example/2","description":"Two.","age":"3 days ago"},{"title":"C","url":"https://a.example/3","description":"Three."}]}}`))
	}))
	defer srv.Close()
	install(t, "brave-search", templateSpec(t, "brave-search", srv.URL))
	out, err := Search(context.Background(), "brave-search", "x")
	if err != nil || !strings.Contains(out, "[Published 2026-09-20] One.") || !strings.Contains(out, "\n   [Published ") || !strings.Contains(out, "\n   Three.") {
		t.Errorf("results:\n%s\n%v", out, err)
	}
	if strings.Count(out, "[Published") != 2 {
		t.Errorf("want two dated results:\n%s", out)
	}
	bad := templateSpec(t, "brave-search", srv.URL)
	bad["provides"].([]any)[0].(map[string]any)["output"].(map[string]any)["results"].(map[string]any)["fields"].(map[string]any)["published"] = "page_age|"
	if err := validateSpec(t, bad); err == nil || !strings.Contains(err.Error(), "field published") {
		t.Errorf("an empty alternative: %v", err)
	}
}

// sttServer is a transcriber at /v1/audio/transcriptions answering text,
// keeping each request's key and form.
type sttRequest struct {
	key, file string
	fields    map[string]string
}

func sttServer(t *testing.T, text string) (*httptest.Server, *[]sttRequest) {
	t.Helper()
	var got []sttRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req := sttRequest{key: r.Header.Get("Authorization"), fields: map[string]string{}}
		for k, v := range r.MultipartForm.Value {
			req.fields[k] = v[0]
		}
		if fh := r.MultipartForm.File["file"]; len(fh) > 0 {
			req.file = fh[0].Filename
		}
		got = append(got, req)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"` + text + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

// ttsServer is a Kokoro-FastAPI at /v1: it speaks, keeping each request's
// key and body, and lists its voices unless noVoices.
func ttsServer(t *testing.T, noVoices bool) (*httptest.Server, *[]string, *[]map[string]any) {
	t.Helper()
	var keys []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/audio/voices" && !noVoices:
			w.Write([]byte(`{"voices":["af_heart","bm_george"]}`))
		case r.URL.Path == "/v1/audio/speech":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			keys, bodies = append(keys, r.Header.Get("Authorization")), append(bodies, body)
			w.Write(ttsClip)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &keys, &bodies
}

// ttsClip is enough of an mp3 for the audio check.
var ttsClip = append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 32)...)

// forgetCredentials removes the named credentials a test saved.
func forgetCredentials(t *testing.T, names ...string) {
	t.Cleanup(func() {
		for _, n := range names {
			Secure().Delete(n)
		}
	})
}

// The templates that make a connection install it alone, unapproved, with a
// spec that saves: no credential beside it, since its key is a secret
// setting it keeps. Two installs are two connections, each keeping its own
// address and key, so no install clashes with another.
func TestATemplateInstallsAConnection(t *testing.T) {
	for _, id := range []string{"gemini-images", "openai-images", "elevenlabs-speech", "openai-transcription",
		"openai-embeddings", "brave-search", "mailgun-mail"} {
		world(t)
		res, err := recipes.Install(RootDB, id, "admin", map[string]string{"domain": "mg.example"})
		if err != nil || res.Imported != 1 || len(res.Outcomes) != 1 || res.Outcomes[0].Type != "connector" {
			t.Fatalf("%s: %+v %v", id, res, err)
		}
		c, ok := GetConnector(RootDB, res.Outcomes[0].Name)
		if !ok || c.Kind != Kind || c.Approved || c.ID == "" {
			t.Fatalf("%s: the connection: %+v", id, c)
		}
		s, err := Parse(c.Spec)
		if err == nil {
			err = s.Validate()
		}
		if _, keyed := s.secretField(); err != nil || !keyed {
			t.Errorf("%s: its spec: %v, declares its key: %v", id, err, keyed)
		}
	}
	world(t)
	const title = "llama.cpp / vLLM / OpenAI-compatible (embeddings)"
	for _, name := range []string{"embed-a", "embed-b"} {
		if _, err := recipes.Install(RootDB, "openai-compatible-embeddings", "admin", map[string]string{"name": name}); err != nil {
			t.Fatal(err)
		}
		c, _ := GetConnector(RootDB, name)
		if err := SaveSettings(c, "embed.text", map[string]string{"base_url": "http://" + name + ".example:8080/v1"}, nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Each is shown as its template is, the second numbered; two cannot be
	// shown alike. Each keeps its own address.
	a, _ := GetConnector(RootDB, "embed-a")
	b, _ := GetConnector(RootDB, "embed-b")
	if a.Title() != title || b.Title() != title+" 2" {
		t.Errorf("shown as %q and %q", a.Title(), b.Title())
	}
	if err := SetConnectorDisplayName(RootDB, "embed-b", strings.ToLower(title)); err == nil {
		t.Error("two connectors shown alike")
	}
	if err := SetConnectorDisplayName(RootDB, "embed-b", "Office embedder"); err != nil {
		t.Error(err)
	}
	for _, name := range []string{"embed-a", "embed-b"} {
		c, _ := GetConnector(RootDB, name)
		if got := ConnectionConfig(RootDB, c)["base_url"]; got != "http://"+name+".example:8080/v1" {
			t.Errorf("%s keeps %q", name, got)
		}
	}
}

// A tier's or a setting's provider reads by its proper name: a built-in one
// as people know it, a connection by the name it is shown as, a peer as
// Peer <name>, anything else as it is.
func TestAProviderIsShownByItsName(t *testing.T) {
	world(t)
	if _, err := recipes.Install(RootDB, "openai-compatible-speech", "", map[string]string{"name": "local-speech"}); err != nil {
		t.Fatal(err)
	}
	SetConnectorDisplayName(RootDB, "local-speech", "Local speech server")
	for in, want := range map[string]string{
		"local-speech": "Local speech server", "anthropic": "Anthropic", "openai": "OpenAI", "llama.cpp": "llama.cpp",
		"vllm": "vLLM", "bedrock": "AWS Bedrock", "peer:den": "Peer den", "something-else": "something-else",
	} {
		if got := ProviderTitle(in); got != want {
			t.Errorf("ProviderTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// A template's connector is named by gohort, not asked: added with no name,
// it takes the question's default, and added again it takes that numbered,
// shown numbered too, never with its name; a name answered is kept, and
// what it is shown as can be answered instead of the template's title.
func TestATemplateNamesItsConnector(t *testing.T) {
	world(t)
	db := RootDB
	r, _, _ := recipes.Get(db, "serper-search")
	if q := recipes.ConnectorNameQuestion(r); q != "name" {
		t.Fatalf("the naming question: %q", q)
	}
	connectorOf := func(res ArtifactImportResult) Connector {
		for _, o := range res.Outcomes {
			if o.Type == "connector" && o.Status == "imported" {
				c, _ := GetConnector(db, o.Name)
				return c
			}
		}
		t.Fatalf("no connector landed: %+v", res)
		return Connector{}
	}
	var got []string
	for _, answers := range []map[string]string{
		{},
		{"name": " "},
		{"name": "mine", recipes.ShownAs: "Team search"},
		{recipes.ShownAs: "Team search"},
	} {
		res, err := recipes.Install(db, "serper-search", "admin", answers)
		if err != nil {
			t.Fatal(err)
		}
		c := connectorOf(res)
		got = append(got, c.Name+"="+c.Title())
	}
	want := "serper-search=Serper (web search) serper-search-2=Serper (web search) 2 mine=Team search serper-search-3=Team search 2"
	if strings.Join(got, " ") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, " "), want)
	}
	if recipes.ConnectorNameQuestion(recipes.Recipe{}) != "" {
		t.Error("a template with no connector has no naming question")
	}
	two := recipes.Recipe{Bundle: ArtifactBundle{Artifacts: []PortableArtifact{{Type: "connector", Name: "{{site}}-{{name}}"}}}}
	if q := recipes.ConnectorNameQuestion(two); q != "" {
		t.Errorf("a name made of two answers is no one question's: %q", q)
	}
	// The question's default names it, not the template's id.
	var names []string
	for i := 0; i < 2; i++ {
		res, err := recipes.Install(db, "openai-compatible-embeddings", "admin", nil)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, connectorOf(res).Name)
	}
	if strings.Join(names, " ") != "local-embeddings local-embeddings-2" {
		t.Errorf("named %v", names)
	}
}

// The message templates fetch a channel and send to it: Slack from where
// the last fetch left off (oldest), its messages saying no chat of their
// own, so they are in the one asked for, and the newest's ts the cursor;
// Discord its latest, each saying its channel, with no cursor. A Slack
// refusal, which comes back as ok false with no messages, fails visibly
// rather than reading as nothing new.
func TestTheMessageTemplatesFetchAndSend(t *testing.T) {
	world(t)
	var got []string
	refuse := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+string(b))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case refuse:
			w.Write([]byte(`{"ok":false,"error":"not_in_channel"}`))
		case strings.HasPrefix(r.URL.Path, "/api/conversations.history"):
			w.Write([]byte(`{"ok":true,"messages":[{"ts":"1700000200.000200","user":"U2","text":"second","user_profile":{"real_name":"Ann"}},{"ts":"1700000100.000100","bot_id":"B1","username":"build","text":"first"}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/chat"):
			w.Write([]byte(`{"ok":true,"ts":"1700000300.0003"}`))
		case r.Method == "GET":
			w.Write([]byte(`[{"id":"112","channel_id":"9001","author":{"id":"77","username":"ann","global_name":"Ann"},"content":"hi there","timestamp":"2026-11-01T10:00:00+00:00"}]`))
		default:
			w.Write([]byte(`{"id":"113"}`))
		}
	}))
	defer srv.Close()
	for _, id := range []string{"slack-messages", "discord-messages"} {
		install(t, id, templateSpec(t, id, srv.URL))
	}
	c, _ := GetConnector(RootDB, "slack-messages")
	SetConnectionConfig(RootDB, c, map[string]string{"channel": "C42"})

	got = nil
	res, err := Call(context.Background(), nil, "slack-messages", "message.fetch", map[string]any{"chat": "C7", "since": "1700000000.0"})
	ms, _ := FetchedMessages(res, "C7")
	if err != nil || len(got) != 1 || got[0] != "GET /api/conversations.history?channel=C7&oldest=1700000000.0&limit=50 " ||
		len(ms) != 2 || ms[0] != (FetchedMessage{ID: "1700000200.000200", Chat: "C7", From: "U2", FromName: "Ann", Text: "second", At: "1700000200.000200"}) ||
		ms[1].From != "B1" || ms[1].FromName != "build" || res["cursor"].Text != "1700000200.000200" {
		t.Errorf("slack fetch: %v %q %+v %q", err, got, ms, res["cursor"].Text)
	}
	got = nil
	if _, err := Call(context.Background(), nil, "slack-messages", "message.fetch", map[string]any{}); err != nil || got[0] != "GET /api/conversations.history?channel=C42&oldest=0&limit=50 " {
		t.Errorf("slack fetch with nothing given: %v %q", err, got)
	}
	got = nil
	res, err = Call(context.Background(), nil, "discord-messages", "message.fetch", map[string]any{"chat": "9001"})
	ms, _ = FetchedMessages(res, "")
	if err != nil || got[0] != "GET /api/v10/channels/9001/messages?limit=50 " || len(ms) != 1 ||
		ms[0] != (FetchedMessage{ID: "112", Chat: "9001", From: "77", FromName: "Ann", Text: "hi there", At: "2026-11-01T10:00:00+00:00"}) || res["cursor"].Text != "" {
		t.Errorf("discord fetch: %v %q %+v", err, got, ms)
	}
	for _, s := range []struct{ id, want string }{
		{"slack-messages", `POST /api/chat.postMessage {"channel":"C7","text":"ok"}`},
		{"discord-messages", `POST /api/v10/channels/9001/messages {"content":"ok"}`},
	} {
		got = nil
		to := "C7"
		if s.id == "discord-messages" {
			to = "9001"
		}
		if _, err := Call(context.Background(), nil, s.id, "message.send", map[string]any{"text": "ok", "to": to}); err != nil || got[0] != s.want {
			t.Errorf("%s send: %v %q", s.id, err, got)
		}
	}
	// Slack answers a refusal with 200 and ok false: an error, with Slack's
	// reason, on a fetch and on a send alike.
	refuse = true
	if _, err := Call(context.Background(), nil, "slack-messages", "message.fetch", map[string]any{"chat": "C7"}); err == nil || !strings.Contains(err.Error(), "not_in_channel") {
		t.Errorf("a Slack refusal: %v", err)
	}
	if _, err := Call(context.Background(), nil, "slack-messages", "message.send", map[string]any{"text": "x", "to": "C7"}); err == nil || !strings.Contains(err.Error(), "not_in_channel") {
		t.Errorf("a Slack refusal of a send: %v", err)
	}
}

// What a fetch gives is checked: a message with no id fails it.
func TestAFetchedMessageNeedsAnID(t *testing.T) {
	c, _ := Lookup("message.fetch")
	if err := c.Check(Result{"messages": {Text: `[{"id":"1","text":"a"}]`}}); err != nil {
		t.Error(err)
	}
	if err := c.Check(Result{"messages": {Text: `[{"text":"a"}]`}}); err == nil {
		t.Error("a message with no id passed")
	}
	if err := c.Check(Result{"messages": {Text: `"nope"`}}); err == nil {
		t.Error("messages that are not a list passed")
	}
}

// The vLLM System One template asks the server's /systemone with the
// options as an object in the order given, thinking off and the model set,
// reads each option's share and the label mass back, and passes its own
// known-answer test.
func TestTheSystemOneTemplateDecides(t *testing.T) {
	world(t)
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			w.Write([]byte(`{"data":[{"id":"judge"}]}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Write([]byte(`{"answers":{"d":{"type":"choice","choice":"o00","probabilities":{"o00":0.9,"o01":0.1}}},"diagnostics":{"d":{"label_mass":0.95}}}`))
	}))
	defer srv.Close()
	install(t, "local-system-one", templateSpec(t, "vllm-system-one", srv.URL+"/v1"))
	res, err := Call(context.Background(), nil, "local-system-one", DecideChoice, map[string]any{
		"instructions": "You filter.", "state": "a message", "ask": "Take it up?",
		"criteria": map[string]any{"o00": "Take it up", "o01": "Leave it"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Probabilities(res)
	if err != nil || p["o00"] != 0.9 || p["o01"] != 0.1 || strings.TrimSpace(res["label_mass"].Text) != "0.95" {
		t.Errorf("the reply read as %v, label mass %q (%v)", p, res["label_mass"].Text, err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &sent); err != nil {
		t.Fatalf("the request: %s", bodies[0])
	}
	q, _ := sent["questions"].(map[string]any)["d"].(map[string]any)
	crit, ok := q["criteria"].(map[string]any)
	if !ok || crit["o01"] != "Leave it" || q["instructions"] != "Take it up?" || sent["model"] != "judge" || sent["state"] != "a message" ||
		!strings.Contains(bodies[0], `"enable_thinking":false`) || strings.Index(bodies[0], `"o00"`) > strings.Index(bodies[0], `"o01"`) {
		t.Errorf("the request: %s", bodies[0])
	}
	if c := contracts[DecideChoice]; c.testCheck == nil || c.Check(res) != nil || c.testCheck(res) != nil {
		t.Error("a right answer to the known-answer test did not pass it")
	}
	wrong := Result{"probabilities": {Text: `{"o00":0.2,"o01":0.8}`}}
	if contracts[DecideChoice].testCheck(wrong) == nil {
		t.Error("a wrong answer to the known-answer test passed it")
	}
}

// The Gemini templates' Model setting lists the models the key can use, from
// the provider's own list: only the ones the template can drive (image models
// for pictures, omni models for video), named as the setting takes them.
func TestTheGeminiTemplatesListTheirModels(t *testing.T) {
	world(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[{"name":"models/gemini-2.5-flash"},{"name":"models/gemini-2.5-flash-image"},` +
			`{"name":"models/gemini-3-pro-image-preview"},{"name":"models/gemini-omni-1.2-flash"},{"name":"models/text-embedding-004"}]}`))
	}))
	defer srv.Close()
	install(t, "gemini-images", templateSpec(t, "gemini-images", srv.URL))
	install(t, "gemini-video", templateSpec(t, "gemini-video", srv.URL+"/v1beta/"))
	for name, want := range map[string]string{
		"gemini-images": "gemini-2.5-flash-image,gemini-3-pro-image-preview",
		"gemini-video":  "gemini-omni-1.2-flash",
	} {
		got, err := ListOptions(context.Background(), name, "model")
		if err != nil || strings.Join(got, ",") != want {
			t.Errorf("%s lists %v (%v), want %s", name, got, err, want)
		}
	}
	if got := matchOptions([]string{"a", "b"}, ""); strings.Join(got, ",") != "a,b" {
		t.Errorf("no match keeps every value: %v", got)
	}
}

// A Qwen3-TTS server lists its voices at /voices and has no /audio/voices.
// Its template reads them there: the voice List, the Speech page's voices
// (speech.voices), the health check, and a speak in its default voice,
// which is one of them. Its models come from /models.
func TestTheQwenSpeechTemplateReadsItsOwnVoiceList(t *testing.T) {
	world(t)
	var spoke []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Write([]byte(`{"object":"list","data":[{"id":"Qwen/Qwen3-TTS-12Hz-1.7B-CustomVoice","object":"model","available_voices":["serena","ryan","aiden"]}]}`))
		case "/v1/voices":
			w.Write([]byte(`{"voices":["serena","ryan","aiden"],"languages":["auto","english"]}`))
		case "/v1/audio/speech":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			spoke = append(spoke, body)
			w.Write(ttsClip)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	install(t, "qwen", templateSpec(t, "qwen3-tts-speech", srv.URL+"/v1"))
	if v, err := ListOptions(context.Background(), "qwen", "voice"); err != nil || strings.Join(v, ",") != "serena,ryan,aiden" {
		t.Errorf("the voice List: %v %v", v, err)
	}
	if v, err := ListOptions(context.Background(), "qwen", "model"); err != nil || len(v) != 1 || v[0] != "Qwen/Qwen3-TTS-12Hz-1.7B-CustomVoice" {
		t.Errorf("the model List: %v %v", v, err)
	}
	if v, err := voicesVia(context.Background(), "qwen"); err != nil || len(v) != 3 || v[1].ID != "ryan" {
		t.Errorf("the speech voices: %+v %v", v, err)
	}
	if h, err := CheckHealth(context.Background(), "qwen"); err != nil || !h.OK {
		t.Errorf("health: %+v %v", h, err)
	}
	if _, _, err := speakVia(context.Background(), "qwen", "Hi.", ""); err != nil || len(spoke) != 1 || spoke[0]["voice"] != "ryan" {
		t.Fatalf("speak in its default voice: %v %v", err, spoke)
	}
}

// options_path reads a list from inside a reply, for a server that keeps its
// list in another (each model's available_voices inside /models).
func TestOptionsPathReadsAListInsideAReply(t *testing.T) {
	world(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m","available_voices":["serena","ryan"]}]}`))
	}))
	defer srv.Close()
	spec := templateSpec(t, "qwen3-tts-speech", srv.URL+"/v1")
	for _, f := range spec["config"].([]any) {
		if m := f.(map[string]any); m["name"] == "voice" {
			m["options_from"], m["options_path"] = "models", "data.0.available_voices"
		}
	}
	install(t, "nested", spec)
	if v, err := ListOptions(context.Background(), "nested", "voice"); err != nil || strings.Join(v, ",") != "serena,ryan" {
		t.Errorf("the list at options_path: %v %v", v, err)
	}
}

// options_path is a spec path, checked when the spec is: it needs the
// options_from it reads, and a path in the grammar.
func TestOptionsPathIsChecked(t *testing.T) {
	for _, f := range []ConfigField{
		{Name: "voice", OptionsPath: "data.0.available_voices"},
		{Name: "voice", OptionsFrom: "models", OptionsPath: "data..voices"},
	} {
		if err := checkField("config", f, true); err == nil {
			t.Errorf("accepted %+v", f)
		}
	}
	if err := checkField("config", ConfigField{Name: "voice", OptionsFrom: "models", OptionsPath: "data.0.available_voices"}, true); err != nil {
		t.Errorf("refused a good one: %v", err)
	}
}
