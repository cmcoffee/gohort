package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
	if r := got["both"]; !r.IsImage || !r.BothWays || !r.UsedBoth || r.UsedFor != "used for everything it does" {
		t.Errorf("a backend that makes and edits: %+v", r)
	}
	if r := got["painter"]; r.BothWays || r.UsedFor != "" {
		t.Errorf("a backend that only makes pictures has Used for: %+v", r)
	}
	if r := got["clips"]; r.IsImage || !r.IsUnused || r.Does != "video" {
		t.Errorf("a video connector: %+v", r)
	}

	// Used for, on the one that does both, and on no other.
	if w := call("POST", "/api/media-backends/uses?name=both&uses=edit"); w.Code >= 300 {
		t.Fatalf("Used for: %d %s", w.Code, w.Body.String())
	}
	if ImageBackendUse("both") != ImageUseEdit {
		t.Errorf("stored use = %q", ImageBackendUse("both"))
	}
	if r := rows()["both"]; !r.UsedEdit || r.UsedBoth || r.UsedFor != "used for editing and blending only" {
		t.Errorf("after editing only: %+v", r)
	}
	if w := call("POST", "/api/media-backends/uses?name=painter&uses=edit"); w.Code != http.StatusBadRequest {
		t.Errorf("Used for on a backend that only makes pictures: %d", w.Code)
	}
	if w := call("POST", "/api/media-backends/uses?name=both&uses=sometimes"); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown use: %d", w.Code)
	}
	call("POST", "/api/media-backends/uses?name=both&uses=both")
	if ImageBackendUse("both") != ImageUseBoth {
		t.Errorf("back to both, stored use = %q", ImageBackendUse("both"))
	}

	// One role endpoint for every kind: the open kinds and music go to their
	// own offered lists; an image backend to Image's default and list.
	call("POST", "/api/media-backends/role?name=clips&role=offered")
	call("POST", "/api/media-backends/role?name=tunes&role=offered")
	call("POST", "/api/media-backends/role?name=painter&role=default")
	var media, music []string
	RootDB.Get(service.MediaTable, "agents", &media)
	RootDB.Get(service.MusicTable, "agents", &music)
	if strings.Join(media, ",") != "clips" || strings.Join(music, ",") != "tunes" || a.storedString(ImageTable, "provider") != "painter" {
		t.Errorf("offered: media %v, music %v, image default %q", media, music, a.storedString(ImageTable, "provider"))
	}
	if r := rows()["clips"]; !r.IsOffered {
		t.Errorf("an offered video connector reads %+v", r)
	}
	if w := call("POST", "/api/media-backends/role?name=clips&role=default"); w.Code != http.StatusBadRequest {
		t.Errorf("a video connector made the default: %d", w.Code)
	}
	call("POST", "/api/media-backends/role?name=clips&role=off")
	RootDB.Get(service.MediaTable, "agents", &media)
	if len(media) != 0 {
		t.Errorf("after Stop offering: %v", media)
	}
}
