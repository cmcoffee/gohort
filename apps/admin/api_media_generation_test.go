package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/recipes"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Media Generation lists every connector that makes media, banded by what it
// makes: images first, then music, then each kind a media.generate connector
// declares. An image backend that both makes and edits pictures has Used
// for; music and the open kinds are offered to agents or not, from the same
// role buttons.
func TestMediaGenerationListsEveryKindInBands(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	a := &AdminApp{db: RootDB}
	sub := http.NewServeMux()
	a.registerMediaGenerationRoutes(sub)
	call := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		sub.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	// post sends body, as a row's Use dropdown does.
	post := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		sub.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return w
	}
	// usedFor is what a row's Used for dropdown sends: the row's key and
	// the choice.
	usedFor := func(name, uses string) int {
		w := httptest.NewRecorder()
		sub.ServeHTTP(w, httptest.NewRequest("POST", "/api/media-backends/uses", strings.NewReader(`{"name":"`+name+`","uses":"`+uses+`"}`)))
		return w.Code
	}
	save := func(name, spec string) {
		t.Helper()
		if err := SaveConnector(RootDB, Connector{Name: name, Kind: service.Kind, Spec: json.RawMessage(spec)}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	base := `"provider":{"transport":"http","base_url":"https://api.example.test","auth":{"none":true},"operations":{"make":{"method":"POST","path":"/make","body":{"prompt":"{prompt}"},"read":"file"},"edit":{"method":"POST","path":"/edit","body":{"prompt":"{prompt}","image":"{image.base64}"},"read":"file"}}}`
	bind := func(capability, out string, extra string) string {
		return `{"capability":"` + capability + `"` + extra + `,"steps":[{"call":"make"}],"output":{"` + out + `":{"from":"body"}}}`
	}
	save("painter", `{`+base+`,"provides":[`+bind("image.generate", "image", "")+`]}`)
	save("both", `{`+base+`,"provides":[`+bind("image.generate", "image", "")+`,{"capability":"image.edit","steps":[{"call":"edit"}],"output":{"image":{"from":"body"}}}]}`)
	save("tunes", `{`+base+`,"provides":[`+bind("music.generate", "audio", "")+`]}`)
	save("clips", `{`+base+`,"provides":[`+bind("media.generate", "file", `,"kind":"video"`)+`]}`)
	save("shapes", `{`+base+`,"provides":[`+bind("media.generate", "file", `,"kind":"3D model"`)+`]}`)

	rows := func() map[string]imageBackendRow {
		var list []imageBackendRow
		if w := call("GET", "/api/media-backends"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil {
			t.Fatalf("the list: %d %s", w.Code, w.Body.String())
		}
		var order []string
		out := map[string]imageBackendRow{}
		for _, r := range list {
			if len(order) == 0 || order[len(order)-1] != r.Group {
				order = append(order, r.Group)
			}
			out[r.Name] = r
		}
		// The server orders the bands: a band is drawn where its rows arrive.
		if got := strings.Join(order, ","); got != "Images,Music,3D model,Video" || len(out) != 5 {
			t.Errorf("bands = %s", got)
		}
		return out
	}
	got := rows()
	if r := got["both"]; !r.IsImage || !r.BothWays || r.Uses != "both" {
		t.Errorf("a backend that makes and edits: %+v", r)
	}
	if r := got["painter"]; r.BothWays || r.Uses != "" {
		t.Errorf("a backend that only makes pictures has Used for: %+v", r)
	}
	if r := got["clips"]; r.IsImage || !r.OfferOnly || r.Role != "unused" || r.Does != "video" {
		t.Errorf("a video connector: %+v", r)
	}

	// Used for, on the one that does both, and on no other.
	if code := usedFor("both", "edit"); code >= 300 {
		t.Fatalf("Used for: %d", code)
	}
	if ImageBackendUse("both") != ImageUseEdit {
		t.Errorf("stored use = %q", ImageBackendUse("both"))
	}
	if r := rows()["both"]; r.Uses != "edit" {
		t.Errorf("after editing only: %+v", r)
	}
	if code := usedFor("painter", "edit"); code != http.StatusBadRequest {
		t.Errorf("Used for on a backend that only makes pictures: %d", code)
	}
	if code := usedFor("both", "sometimes"); code != http.StatusBadRequest {
		t.Errorf("an unknown use: %d", code)
	}
	usedFor("both", "both")
	if ImageBackendUse("both") != ImageUseBoth {
		t.Errorf("back to both, stored use = %q", ImageBackendUse("both"))
	}

	// One role endpoint for every kind: the open kinds and music go to their
	// own offered lists; an image backend to Image's default and list.
	post("/api/media-backends/role", `{"name":"clips","role":"offered"}`)
	post("/api/media-backends/role", `{"name":"tunes","role":"offered"}`)
	post("/api/media-backends/role", `{"name":"painter","role":"default"}`)
	var media, music []string
	RootDB.Get(service.MediaTable, "agents", &media)
	RootDB.Get(service.MusicTable, "agents", &music)
	if strings.Join(media, ",") != "clips" || strings.Join(music, ",") != "tunes" || a.storedString(ImageTable, "provider") != "painter" {
		t.Errorf("offered: media %v, music %v, image default %q", media, music, a.storedString(ImageTable, "provider"))
	}
	if r := rows()["clips"]; r.Role != "offered" {
		t.Errorf("an offered video connector reads %+v", r)
	}
	if w := post("/api/media-backends/role", `{"name":"clips","role":"default"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a video connector made the default: %d", w.Code)
	}
	post("/api/media-backends/role", `{"name":"clips","role":"unused"}`)
	RootDB.Get(service.MediaTable, "agents", &media)
	if len(media) != 0 {
		t.Errorf("after Stop offering: %v", media)
	}
}

// A choice Media Generation holds that no longer reads is a row of its own,
// first, under Needs setting again, saying why: the page answers the
// dashboard's card that sent the reader there. Remove takes the name off
// every choice that holds it and touches nothing else.
func TestMediaGenerationShowsWhatNeedsSettingAgain(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	a := &AdminApp{db: RootDB}
	sub := http.NewServeMux()
	a.registerMediaGenerationRoutes(sub)
	call := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		sub.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	tunes := `{"provider":{"transport":"http","base_url":"https://api.example.test","auth":{"none":true},"operations":{"make":{"method":"POST","path":"/make","body":{"prompt":"{prompt}"},"read":"file"}}},"provides":[{"capability":"music.generate","steps":[{"call":"make"}],"output":{"audio":{"from":"body"}}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "tunes", Kind: service.Kind, Spec: json.RawMessage(tunes)}); err != nil {
		t.Fatal(err)
	}
	RootDB.Set(ImageTable, "provider", "gone")
	RootDB.Set(service.MusicTable, "agents", []string{"gone", "tunes", "lost"})
	list := func() []imageBackendRow {
		var rows []imageBackendRow
		if w := call("GET", "/api/media-backends"); json.Unmarshal(w.Body.Bytes(), &rows) != nil {
			t.Fatalf("the list: %s", w.Body.String())
		}
		return rows
	}
	rows := list()
	if len(rows) != 3 || rows[0].Group != staleBand || rows[1].Group != staleBand || rows[2].Name != "tunes" {
		t.Fatalf("rows = %+v, want the two that no longer read first, then tunes", rows)
	}
	if r := rows[0]; r.Name != "gone" || !r.IsStale || r.Role != "default" ||
		!strings.Contains(r.Stale, "The default image backend: it names gone, which is not a connection here.") ||
		!strings.Contains(r.Stale, "Offered to agents for music: it names gone") {
		t.Errorf("the row for gone says: %+v", r)
	}
	if r := rows[1]; r.Name != "lost" || r.Role != "offered" || !strings.HasPrefix(r.Stale, "Offered to agents for music: it names lost") {
		t.Errorf("the row for lost says: %+v", r)
	}

	if w := call("POST", "/api/media-backends/remove?name=gone"); w.Code >= 300 {
		t.Fatalf("Remove: %d %s", w.Code, w.Body.String())
	}
	var music []string
	RootDB.Get(service.MusicTable, "agents", &music)
	if a.storedString(ImageTable, "provider") != "" || strings.Join(music, ",") != "tunes,lost" {
		t.Errorf("after Remove: default %q, music %v", a.storedString(ImageTable, "provider"), music)
	}
	if rows := list(); len(rows) != 2 || rows[0].Name != "lost" {
		t.Errorf("after Remove, rows = %+v", rows)
	}
	call("POST", "/api/media-backends/remove?name=lost")
	if rows := list(); len(rows) != 1 || rows[0].IsStale || len(service.StaleSettings(RootDB)) != 0 {
		t.Errorf("with nothing stale, rows = %+v, stale settings %v", rows, service.StaleSettings(RootDB))
	}
}

// Add AI generator lists the templates for every kind Media Generation holds,
// in the page's order (images, then music, then the other kinds), so its
// picker reads under a heading per kind; another page's list stays its own.
func TestAddAIGeneratorListsTemplatesByKind(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	a := &AdminApp{db: RootDB}
	sub := http.NewServeMux()
	a.registerRecipeRoutes(sub)
	// Imported templates of the other kinds: a template's category is the
	// kind its picker heading names.
	rec, _, ok := recipes.Get(RootDB, "resend-mail")
	if !ok {
		t.Fatal("no resend-mail template to copy")
	}
	for id, category := range map[string]string{"zz-video": "Media", "aa-tunes": "Music"} {
		r := rec
		r.ID, r.Title, r.Category = id, id, category
		raw, _ := json.Marshal(r)
		if _, err := recipes.Import(RootDB, raw, "root"); err != nil {
			t.Fatalf("import %s: %v", id, err)
		}
	}
	list := func(need string) []templateRow {
		w := httptest.NewRecorder()
		sub.ServeHTTP(w, httptest.NewRequest("GET", "/api/templates/for?need="+need, nil))
		var rows []templateRow
		if json.Unmarshal(w.Body.Bytes(), &rows) != nil {
			t.Fatalf("%s: %s", need, w.Body.String())
		}
		return rows
	}
	var kinds []string
	images := map[string]bool{}
	for _, r := range list("media") {
		if len(kinds) == 0 || kinds[len(kinds)-1] != r.Category {
			kinds = append(kinds, r.Category)
		}
		if r.Category == "Images" {
			images[r.ID] = true
		}
	}
	if got := strings.Join(kinds, ","); got != "Images,Music,Media" {
		t.Errorf("kinds in order = %s", got)
	}
	// What Add image backend opened is among them: a local ComfyUI or A1111.
	if !images["comfyui"] || !images["a1111"] {
		t.Errorf("image templates = %v, want ComfyUI and A1111 among them", images)
	}
	for _, r := range list("mail.send") {
		if r.Category != "Mail" {
			t.Errorf("the mail list holds %s (%s)", r.ID, r.Category)
		}
	}
}
