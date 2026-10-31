package service

import (
	"context"
	"encoding/json"
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
	"github.com/cmcoffee/gohort/core/sourcehooks"
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
		spec := templateSpec(t, id, srv.URL)
		binding(spec)["defaults"] = map[string]any{"from": "gohort@mail.example", "to": "admin@mail.example"}
		if id == "mailgun-mail" {
			provider(spec)["operations"].(map[string]any)["send"].(map[string]any)["path"] = "/v3/mg.mail.example/messages"
		}
		install(t, id, spec)
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

// A search connector is offered beyond Web Search as a Source Hook is: to
// research on the topics it is for (research asks it as it asks a hook),
// and to agents as a tool of its own, which leaves every catalog when the
// connector stops offering it. Its results are marked like any search's.
func TestASearchConnectorIsOfferedToResearchAndAgents(t *testing.T) {
	world(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hits":[{"name":"Smith v. Jones","href":"https://law.example/smith"},{"name":"Later","href":"https://www.google.com/goto?id=9"}]}`))
	}))
	defer srv.Close()
	spec := map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
			"operations": map[string]any{"find": map[string]any{"url": "/find?q={query}"}}},
		"provides": []any{map[string]any{"capability": "web.search", "steps": []any{map[string]any{"call": "find"}},
			"output":   map[string]any{"results": map[string]any{"path": "hits", "fields": map[string]any{"title": "name", "url": "href"}}},
			"research": map[string]any{"topics": []any{"legal"}},
			"tool":     map[string]any{"name": "caselaw_search", "description": "Search court opinions."}}},
	}
	install(t, "caselaw", spec)

	if got := sourcehooks.HooksForTopic([]string{"medical"}); len(got) != 0 {
		t.Errorf("asked on a topic it is not for: %+v", got)
	}
	hooks := sourcehooks.HooksForTopic([]string{"legal"})
	if len(hooks) != 1 || hooks[0].Connector != "caselaw" {
		t.Fatalf("research on its topic: %+v", hooks)
	}
	out, err := sourcehooks.QuerySourceHook(hooks[0], "negligence")
	if err != nil || !strings.HasPrefix(out, "1. Smith v. Jones\n   https://law.example/smith") || !strings.Contains(out, "2. Later\n   (no usable link") {
		t.Errorf("research's query: %q %v", out, err)
	}

	tool, ok := LookupChatTool("caselaw_search")
	if !ok {
		t.Fatal("the connector is not offered as a tool")
	}
	if desc, params := tool.(DynamicChatTool).SchemaWithSession(nil); desc != "Search court opinions." || params == nil {
		t.Errorf("the tool's schema: %q %v", desc, params)
	}
	if out, err := tool.(SessionChatTool).RunWithSession(map[string]any{"query": "negligence"}, nil); err != nil || !strings.Contains(out, "Smith v. Jones") {
		t.Errorf("the tool's search: %q %v", out, err)
	}
	UnapproveConnector(RootDB, "caselaw")
	if _, params := tool.(DynamicChatTool).SchemaWithSession(nil); params != nil {
		t.Error("a turned-off connector's tool is still offered")
	}
	if len(sourcehooks.HooksForTopic([]string{"legal"})) != 0 {
		t.Error("a turned-off connector is still asked in research")
	}

	for want, b := range map[string]map[string]any{
		"web.search only":   {"capability": "speech.synthesize", "steps": []any{map[string]any{"call": "find"}}, "output": map[string]any{"audio": map[string]any{"from": "body"}}, "tool": map[string]any{"name": "say_it"}},
		"needs topics":      {"capability": "web.search", "steps": []any{map[string]any{"call": "find"}}, "output": map[string]any{"results": map[string]any{"path": "hits", "fields": map[string]any{"url": "href"}}}, "research": map[string]any{}},
		"lowercase letters": {"capability": "web.search", "steps": []any{map[string]any{"call": "find"}}, "output": map[string]any{"results": map[string]any{"path": "hits", "fields": map[string]any{"url": "href"}}}, "tool": map[string]any{"name": "Case Law"}},
	} {
		_, err := specFrom(t, map[string]any{"provider": spec["provider"], "provides": []any{b}})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}
