package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/costledger"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/snugforge/kvlite"
)

// world wires what a service call reaches: the store, the governed dispatch,
// where images are written, and the cost ledger.
func world(t *testing.T) {
	t.Helper()
	savedRoot, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	SetImageDir(t.TempDir())
	ledger := &DBase{Store: kvlite.MemStore()}
	costledger.SetCostLedgerDB(ledger)
	t.Cleanup(func() {
		RootDB, AuthDB = savedRoot, savedAuth
		costledger.SetCostLedgerDB(nil)
	})
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// fakeAPI is a fake image API: POST /v1/jobs queues a job, GET
// /v1/jobs/{id} says running once and then done (or failed), and the image
// is at /files/{id}.png. bad serves something that is not an image.
type fakeAPI struct {
	mu     sync.Mutex
	polls  int
	fail   bool
	bad    bool
	down   bool
	bodies []string
	img    []byte
	srv    *httptest.Server
}

func newProvider(t *testing.T) *fakeAPI {
	p := &fakeAPI{img: pngBytes(t)}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case p.down:
			http.Error(w, `{"error":"service unavailable"}`, http.StatusServiceUnavailable)
		case r.Method == "POST" && r.URL.Path == "/v1/jobs":
			b := new(bytes.Buffer)
			b.ReadFrom(r.Body)
			p.bodies = append(p.bodies, b.String())
			w.Write([]byte(`{"job":{"id":"j-1"}}`))
		case r.URL.Path == "/v1/jobs/j-1":
			p.polls++
			switch {
			case p.polls == 1:
				w.Write([]byte(`{"state":"running"}`))
			case p.fail:
				w.Write([]byte(`{"state":"failed","error":{"message":"content policy"}}`))
			default:
				w.Write([]byte(`{"state":"done","outputs":[{"type":"text","url":"x"},{"type":"image","url":"/files/j-1.png"}],"usage":{"cost_usd":0.031}}`))
			}
		case r.URL.Path == "/files/j-1.png":
			w.Header().Set("Content-Type", "image/png")
			if p.bad {
				w.Write([]byte(`{"oops":true}`))
				return
			}
			w.Write(p.img)
		case r.URL.Path == "/v1/models":
			w.Write([]byte(`{"data":[{"id":"m1"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func install(t *testing.T, name string, spec map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(spec)
	if err := SaveConnector(RootDB, Connector{Name: name, Kind: Kind, Spec: raw}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := ApproveConnector(RootDB, name); err != nil {
		t.Fatalf("approve: %v", err)
	}
}

// An approved image service is an image backend like a built-in one: a
// render goes submit, wait, fetch, the image passes the contract's check,
// and the call is recorded with the cost the provider reported.
func TestAnImageServiceRendersThroughTheBackendSeam(t *testing.T) {
	world(t)
	p := newProvider(t)
	install(t, "acme-images", imageSpec(p.srv.URL))

	res, err := GenerateImageWithBackend(context.Background(), "acme-images", "a red circle", false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(res.URL)
	if err != nil || !bytes.Equal(got, p.img) {
		t.Fatalf("the image at %s is not what the provider made: %v", res.URL, err)
	}
	if len(p.bodies) != 1 || !strings.Contains(p.bodies[0], `"prompt":"a red circle"`) || !strings.Contains(p.bodies[0], `"w":64`) {
		t.Errorf("submit body: %v", p.bodies)
	}
	rec := RecordOf("acme-images").Capabilities["image.generate"]
	if calls, failed := rec.Recent(); calls != 1 || failed != 0 || rec.Spend != 0.031 {
		t.Errorf("record: %d calls, %d failed, spend %v", calls, failed, rec.Spend)
	}
	if rows := costledger.CostBySource(1); len(rows) != 1 || rows[0].SourceID != "service:acme-images/image.generate" || rows[0].Cost != 0.031 {
		t.Errorf("ledger: %+v", rows)
	}

	// Torn down, it is no longer a backend.
	if err := UnapproveConnector(RootDB, "acme-images"); err != nil {
		t.Fatal(err)
	}
	if ImageBackendRegistered("acme-images") {
		t.Error("an unapproved service is still an image backend")
	}
}

// A job the provider fails, and a reply that is not an image, are refused
// with the provider's reason and recorded as failures. The failed job costs
// nothing; the finished one that failed the check was still billed by the
// provider, and is counted.
func TestAFailedOrWrongResultIsRefusedWithWhy(t *testing.T) {
	world(t)
	p := newProvider(t)
	install(t, "acme-images", imageSpec(p.srv.URL))

	p.fail = true
	_, err := Call(context.Background(), nil, "acme-images", "image.generate", map[string]any{"prompt": "x"})
	if err == nil || !strings.Contains(err.Error(), "content policy") {
		t.Errorf("a failed job: %v", err)
	}
	p.fail, p.polls, p.bad = false, 0, true
	_, err = Call(context.Background(), nil, "acme-images", "image.generate", map[string]any{"prompt": "x"})
	if err == nil || !strings.Contains(err.Error(), "not an image") || !strings.Contains(err.Error(), "JSON") {
		t.Errorf("a reply that is not an image: %v", err)
	}
	rec := RecordOf("acme-images").Capabilities["image.generate"]
	if calls, failed := rec.Recent(); calls != 2 || failed != 2 || rec.Spend != 0.031 || rec.LastError == "" {
		t.Errorf("record: %d calls, %d failed, spend %v, last error %q", calls, failed, rec.Spend, rec.LastError)
	}
	if rows := costledger.CostBySource(1); len(rows) != 1 || rows[0].Calls != 1 || rows[0].Cost != 0.031 {
		t.Errorf("only the job the provider finished is charged: %+v", rows)
	}
}

// The free check asks the health operation and nothing else: it proves the
// service is up without a paid call, and says so when it is not.
func TestTheFreeHealthCheck(t *testing.T) {
	world(t)
	p := newProvider(t)
	install(t, "acme-images", imageSpec(p.srv.URL))
	h, err := CheckHealth(context.Background(), "acme-images")
	if err != nil || !h.OK {
		t.Fatalf("healthy: %+v %v", h, err)
	}
	if len(p.bodies) != 0 || p.polls != 0 {
		t.Error("the health check made a paid call")
	}
	p.down = true
	if h, _ := CheckHealth(context.Background(), "acme-images"); h.OK || !strings.Contains(h.Message, "503") {
		t.Errorf("down: %+v", h)
	}
	if RecordOf("acme-images").Health.OK {
		t.Error("the record still says healthy")
	}
	// Without a health operation, it is unknown, never healthy.
	m := imageSpec(p.srv.URL)
	delete(provider(m), "health")
	install(t, "quiet", m)
	if _, err := CheckHealth(context.Background(), "quiet"); err == nil || !strings.Contains(err.Error(), "known only from") {
		t.Errorf("no health operation: %v", err)
	}
}

// A contract test is a real call through the contract's check, counted
// apart from real use, and refused past the day's budget with its price.
func TestContractTestsDrawOnADailyBudget(t *testing.T) {
	world(t)
	p := newProvider(t)
	m := imageSpec(p.srv.URL)
	m["test_budget"] = 2
	install(t, "acme-images", m)
	for i := 0; i < 2; i++ {
		p.polls = 0
		reps, err := Test(context.Background(), "acme-images")
		if err != nil || len(reps) != 1 || !reps[0].OK || reps[0].Cost != 0.031 {
			t.Fatalf("test %d: %+v %v", i+1, reps, err)
		}
	}
	if !strings.Contains(p.bodies[0], `"w":256`) {
		t.Errorf("a test does not send the contract's small test inputs: %s", p.bodies[0])
	}
	_, err := Test(context.Background(), "acme-images")
	if err == nil || !strings.Contains(err.Error(), "used its 2 contract tests") || !strings.Contains(err.Error(), "$0.0400") {
		t.Errorf("past the budget: %v", err)
	}
	if len(p.bodies) != 2 {
		t.Errorf("a refused test reached the provider: %d calls", len(p.bodies))
	}
	rows := costledger.CostBySource(1)
	if len(rows) != 1 || !strings.HasSuffix(rows[0].SourceID, ":test") {
		t.Errorf("test spend is not kept apart: %+v", rows)
	}
	if calls, _ := RecordOf("acme-images").Capabilities["image.generate"].Recent(); calls != 0 {
		t.Error("tests counted as real use")
	}
}

// An approved service image backend is among an agent's image backends by
// name, unless the agent's session was denied the credential it spends.
func TestAgentsSeeAServiceBackendUnlessDeniedItsKey(t *testing.T) {
	world(t)
	m := imageSpec("https://api.example.test")
	provider(m)["auth"] = map[string]any{"credential": "acme_key"}
	install(t, "acme-images", m)
	has := func(sess *ToolSession) bool {
		for _, b := range ReachableImageBackends(sess) {
			if b.Name == "acme-images" {
				return true
			}
		}
		return false
	}
	if !has(&ToolSession{}) {
		t.Error("an approved service image backend is not offered to agents")
	}
	if has(&ToolSession{DeniedCredentials: map[string]bool{"acme_key": true}}) {
		t.Error("a session denied the service's key is still offered it")
	}
}

// editServer is a fake editing API: POST /v1/edits takes multipart (an
// image part and a prompt field) and answers base64; POST /v1/raw takes the
// picture as the whole body and answers the edited picture as the body.
func editServer(t *testing.T, out []byte) (*httptest.Server, *[]string) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/edits":
			if err := r.ParseMultipartForm(8 << 20); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			f, hdr, err := r.FormFile("image")
			if err != nil {
				http.Error(w, "no image part", 400)
				return
			}
			b := new(bytes.Buffer)
			b.ReadFrom(f)
			seen = append(seen, "multipart "+hdr.Filename+" png="+fmtBool(bytes.HasPrefix(b.Bytes(), []byte("\x89PNG")))+" prompt="+r.FormValue("prompt"))
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"b64_json": b64(out)}}})
		case "/v1/raw":
			b := new(bytes.Buffer)
			b.ReadFrom(r.Body)
			seen = append(seen, "raw "+r.Header.Get("Content-Type")+" png="+fmtBool(bytes.HasPrefix(b.Bytes(), []byte("\x89PNG"))))
			w.Header().Set("Content-Type", "image/png")
			w.Write(out)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func fmtBool(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// image.edit by multipart and by the picture as the whole body: the
// contract test sends its own small sample picture, and the edited picture
// comes back through the image check.
func TestAnEditServiceSendsThePictureEitherWay(t *testing.T) {
	world(t)
	edited := pngBytes(t)
	srv, seen := editServer(t, edited)
	spec := func(op map[string]any, output map[string]any) map[string]any {
		return map[string]any{
			"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
				"operations": map[string]any{"edit": op}},
			"provides": []any{map[string]any{"capability": "image.edit", "steps": []any{map[string]any{"call": "edit"}}, "output": output}},
		}
	}
	install(t, "multi-edit", spec(
		map[string]any{"method": "POST", "path": "/v1/edits", "send": "multipart", "body": map[string]any{"image": "{image}", "prompt": "{prompt}"}},
		map[string]any{"image": map[string]any{"path": "data.0.b64_json", "as": "base64"}}))
	install(t, "raw-edit", spec(
		map[string]any{"method": "POST", "path": "/v1/raw", "send": "file", "body": "{image}", "read": "file"},
		map[string]any{"image": map[string]any{"from": "body"}}))

	for _, name := range []string{"multi-edit", "raw-edit"} {
		reps, err := Test(context.Background(), name)
		if err != nil || len(reps) != 1 || !reps[0].OK {
			t.Fatalf("%s test: %+v %v", name, reps, err)
		}
		got, _ := os.ReadFile(reps[0].File)
		if !bytes.Equal(got, edited) {
			t.Errorf("%s: the edited picture is not what came back", name)
		}
	}
	want := []string{"multipart", "png=yes prompt=Make the circle blue.", "raw image/png png=yes"}
	all := strings.Join(*seen, " | ")
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("the provider saw %q, missing %q", all, w)
		}
	}
	// A caller's own file, by path.
	in := t.TempDir() + "/photo.png"
	os.WriteFile(in, pngBytes(t), 0o644)
	if _, err := Call(context.Background(), nil, "raw-edit", "image.edit", map[string]any{"prompt": "x", "image": in}); err != nil {
		t.Errorf("an edit of a file by its path: %v", err)
	}
}

// An edit service is where an agent's picture edit goes: offered as able to
// edit one picture, and EditImageWithBackend sends the agent's own picture
// through the contract.
func TestAnAgentEditsAPictureThroughAService(t *testing.T) {
	world(t)
	edited := pngBytes(t)
	srv, seen := editServer(t, edited)
	install(t, "raw-edit", map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
			"operations": map[string]any{"edit": map[string]any{"method": "POST", "path": "/v1/raw", "send": "file", "body": "{image}", "read": "file"}}},
		"provides": []any{map[string]any{"capability": "image.edit", "steps": []any{map[string]any{"call": "edit"}},
			"output": map[string]any{"image": map[string]any{"from": "body"}}}},
	})
	ws := t.TempDir()
	os.WriteFile(ws+"/photo.png", pngBytes(t), 0o644)
	sess := &ToolSession{WorkspaceDir: ws}
	var choice *ImageBackendChoice
	for _, b := range ReachableImageBackends(sess) {
		if b.Name == "raw-edit" {
			b := b
			choice = &b
		}
	}
	if choice == nil || !choice.Edits || choice.MaxImages != 1 {
		t.Fatalf("the edit service as agents see it: %+v", choice)
	}
	res, err := EditImageWithBackend(sess, EditImageRequest{Backend: "raw-edit", Prompt: "bluer", Images: []string{"photo.png"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(res.URL); !bytes.Equal(got, edited) || len(*seen) != 1 {
		t.Errorf("the edit did not go through the service: %v", *seen)
	}
	if _, err := EditImageWithBackend(sess, EditImageRequest{Backend: "raw-edit", Prompt: "x", Images: []string{"photo.png", "photo.png"}}); err == nil || !strings.Contains(err.Error(), "one picture at a time") {
		t.Errorf("two pictures: %v", err)
	}
	// Unapproved, it is gone from the edit path too.
	UnapproveConnector(RootDB, "raw-edit")
	if _, err := EditImageWithBackend(sess, EditImageRequest{Backend: "raw-edit", Prompt: "x", Images: []string{"photo.png"}}); err == nil {
		t.Error("an unapproved edit service still edits")
	}
}

// A service failing again and again tells each admin once, in their
// notifications, where to fix it; the repeats fold into a count.
func TestAFailingServiceTellsTheAdmins(t *testing.T) {
	world(t)
	AuthDB().Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	AuthDB().Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	p := newProvider(t)
	install(t, "acme-images", imageSpec(p.srv.URL))
	p.down = true
	for i := 0; i < 4; i++ {
		Call(context.Background(), nil, "acme-images", "image.generate", map[string]any{"prompt": "x"})
		if i == 1 && len(notices.List(RootDB, "root")) != 0 {
			t.Fatal("told after two failures")
		}
	}
	list := notices.List(RootDB, "root")
	if len(list) != 1 || list[0].Count != 2 || !strings.Contains(list[0].Title, "acme-images is failing") || !strings.Contains(list[0].Body, "use Fix") {
		t.Fatalf("the admin's notices: %+v", list)
	}
	if len(notices.List(RootDB, "bob")) != 0 {
		t.Error("a non-admin was told")
	}
}

// A picture goes up only to the service's own host, never to another one an
// operation's url names, and a multipart carries one file part.
func TestAPictureGoesOnlyToTheServicesHost(t *testing.T) {
	world(t)
	srv, seen := editServer(t, pngBytes(t))
	edit := func(op map[string]any) map[string]any {
		return map[string]any{
			"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
				"operations": map[string]any{"edit": op}},
			"provides": []any{map[string]any{"capability": "image.edit", "steps": []any{map[string]any{"call": "edit"}},
				"output": map[string]any{"image": map[string]any{"from": "body"}}}},
		}
	}
	install(t, "elsewhere", edit(map[string]any{"method": "POST", "url": "http://other.example.test/v1/raw", "send": "file", "body": "{image}", "read": "file"}))
	_, err := Call(context.Background(), nil, "elsewhere", "image.edit", map[string]any{"prompt": "x", "image": mustSample(t)})
	if err == nil || !strings.Contains(err.Error(), "only to the service's own host") {
		t.Errorf("a picture sent to another host: %v", err)
	}
	if len(*seen) != 0 {
		t.Error("something reached a server")
	}
	two := edit(map[string]any{"method": "POST", "path": "/v1/edits", "send": "multipart",
		"body": map[string]any{"image": "{image}", "again": "{image}"}})
	binding(two)["output"] = map[string]any{"image": map[string]any{"path": "data.0.b64_json", "as": "base64"}}
	if _, err := specFrom(t, two); err == nil || !strings.Contains(err.Error(), "one per request") {
		t.Errorf("two file parts: %v", err)
	}
}

func mustSample(t *testing.T) *File {
	t.Helper()
	f, err := samplePicture()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A service reads as the wrapper it is, inside to outside: what gohort asks
// of each capability (in the admin's words), the mapping onto endpoints, and
// what it reaches with which key. A music service's tool is named, since that
// is where an admin finds it.
func TestAServiceReadsAsItsWrapper(t *testing.T) {
	m := imageSpec("https://api.example.test/v1")
	provider(m)["auth"] = map[string]any{"credential": "img"}
	s, err := specFrom(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.Describe("pics"), "Image Generation ⇐ submit, then wait on status ⇐ api.example.test (key img)"; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	s.Provides = append(s.Provides, Binding{Capability: "music.generate", Steps: []Step{{Call: "a"}, {Wait: "b"}}})
	s.Provider.Auth = Auth{None: true}
	if got := s.Describe("My Songs"); got != "Image Generation, agent tool generate_music_my_songs ⇐ api.example.test (no key), 3 endpoints" {
		t.Errorf("two capabilities stay one line, naming them and counting endpoints: %q", got)
	}
}

// Each endpoint reads as what it is sent and from where, what is read from
// its reply, and what uses it: the capabilities' in step order, then the
// health check, then any nothing uses.
func TestAServiceListsItsEndpoints(t *testing.T) {
	m := imageSpec("https://api.example.test")
	ops := provider(m)["operations"].(map[string]any)
	ops["speak"] = map[string]any{"method": "POST", "path": "/v1/speech", "body": map[string]any{"text": "{text}", "voice": "{voice}"}, "read": "file"}
	ops["cleanup"] = map[string]any{"method": "DELETE", "path": "/v1/files/{id}"}
	m["provides"] = append(m["provides"].([]any), map[string]any{"capability": "speech.synthesize",
		"steps": []any{map[string]any{"call": "speak"}}, "defaults": map[string]any{"voice": "alloy"},
		"output": map[string]any{"audio": map[string]any{"from": "body"}}})
	s, err := specFrom(t, m)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range s.Endpoints() {
		got = append(got, strings.Join([]string{e.Name, e.Address, e.UsedBy, e.Sends, e.Reads}, " | "))
	}
	want := []string{
		`submit | POST /v1/jobs | Image Generation, step 1 | json: prompt ← {prompt}; size ← "{width}x{height}"; w ← {width} (default 64) | job.id → id`,
		`status | GET /v1/jobs/{id} | Image Generation, step 2 |  | until state == done, failing on state == failed; outputs[type=image].url → image (a file, fetched)`,
		`speak | POST /v1/speech | Speech | json: text ← {text}; voice ← {voice} (default alloy) | the reply → audio (a file)`,
		`models | GET /v1/models | health check |  | expects data`,
		`cleanup | DELETE /v1/files/{id} | not used |  | `,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
