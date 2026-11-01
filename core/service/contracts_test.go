package service

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/media"
)

// A search's results are settled the same way for every engine before its
// check: tags and entities out, a relative link or a bare DOI made whole, a
// result with no title titled by its link, and one with no link left out
// when others have one; when none has one, the check still refuses them.
func TestSearchResultsAreTidied(t *testing.T) {
	r := Result{"results": Value{Text: `[
		{"title":"<b>Drones</b> &amp; the law","url":"/opinion/12/","snippet":"Court <mark>ruled</mark> on drones."},
		{"title":"A paper","url":"10.1000/xyz123","snippet":""},
		{"title":"","url":"//c.example/x","snippet":"untitled"},
		{"title":"No link","url":"","snippet":"dropped"},
		{"title":"","url":"","snippet":"nothing"}]`}}
	got, _ := SearchResults(tidyResults(r, "https://api.example.org/v3/search"))
	want := []SearchResult{
		{Title: "Drones & the law", URL: "https://api.example.org/opinion/12/", Snippet: "Court ruled on drones."},
		{Title: "A paper", URL: "https://doi.org/10.1000/xyz123"},
		{Title: "https://c.example/x", URL: "https://c.example/x", Snippet: "untitled"},
	}
	if len(got) != len(want) {
		t.Fatalf("results: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %+v, want %+v", i, got[i], want[i])
		}
	}
	none := tidyResults(Result{"results": Value{Text: `[{"title":"A","url":"","snippet":"x"}]`}}, "https://api.example.org")
	if err := checkResults("results")(none); err == nil {
		t.Error("results read from the wrong field passed")
	}
}

// A plain search takes a query's site: filters out before it is sent; any
// other leaves the query as asked. Only a search may be plain.
func TestAPlainSearchDropsSiteFilters(t *testing.T) {
	world(t)
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Query().Get("q"))
		w.Write([]byte(`{"items":[{"t":"A","u":"https://a.example/1"}]}`))
	}))
	defer srv.Close()
	for _, plain := range []bool{true, false} {
		install(t, "finder", map[string]any{
			"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
				"operations": map[string]any{"search": map[string]any{"path": "/s?q={query}"}}},
			"provides": []any{map[string]any{"capability": "web.search", "plain": plain, "steps": []any{map[string]any{"call": "search"}},
				"output": map[string]any{"results": map[string]any{"path": "items", "fields": map[string]any{"title": "t", "url": "u"}}}}},
		})
		if _, err := Search(context.Background(), "finder", "drone law site:courts.example.gov rulings"); err != nil {
			t.Fatal(err)
		}
	}
	if len(asked) != 2 || asked[0] != "drone law rulings" || asked[1] != "drone law site:courts.example.gov rulings" {
		t.Errorf("asked: %q", asked)
	}
	bad := templateSpec(t, "gemini-images", srv.URL)
	bad["provides"].([]any)[0].(map[string]any)["plain"] = true
	if err := validateSpec(t, bad); err == nil || !strings.Contains(err.Error(), "plain") {
		t.Errorf("a plain image maker: %v", err)
	}
}

// The voice list contract reads a list of names or objects, and its check
// refuses entries that name no voice.
func TestTheVoiceListIsChecked(t *testing.T) {
	ct, _ := Lookup("speech.voices")
	for list, ok := range map[string]bool{
		`["af_heart",{"voice_id":"21m00","name":"Rachel"}]`: true,
		`[]`:                       true,
		`[{"category":"premade"}]`: false,
		`{"voices":[]}`:            false,
	} {
		if err := ct.Check(Result{"voices": Value{Text: list}}); (err == nil) != ok {
			t.Errorf("%s: %v", list, err)
		}
	}
	vs, _ := Voices(Result{"voices": Value{Text: `["af_heart",{"voice_id":"21m00","name":"Rachel"}]`}})
	if len(vs) != 2 || vs[1] != (media.Voice{ID: "21m00", Name: "Rachel"}) {
		t.Errorf("voices: %+v", vs)
	}
}

// The image search contract reads a list of pictures, and its check
// refuses one with no picture address: the spec reads the wrong field.
func TestImageSearchIsChecked(t *testing.T) {
	ct, _ := Lookup("image.search")
	for list, ok := range map[string]bool{
		`[{"image_url":"https://img.example/1.jpg","title":"A"}]`: true,
		`[]`:                             true,
		`[{"image_url":"","title":"A"}]`: false,
		`[{"image_url":"/local.jpg","title":"A"}]`: false,
		`{"images":[]}`: false,
	} {
		if err := ct.Check(Result{"results": Value{Text: list}}); (err == nil) != ok {
			t.Errorf("%s: %v", list, err)
		}
	}
}

// The Test's tone is a WAV a strict reader takes: a 16-byte PCM format chunk
// before the data. It went out without one, and whisper.cpp refused it with
// a 400 while a real recording transcribed.
func TestTheSampleToneIsAWholeWAV(t *testing.T) {
	w := SampleTone()
	if len(w) < 44 || string(w[0:4]) != "RIFF" || string(w[8:16]) != "WAVEfmt " || string(w[36:40]) != "data" {
		t.Fatalf("not a canonical WAV header: % x", w[:min(len(w), 44)])
	}
	le := binary.LittleEndian
	if size, format, chans, rate, bits := le.Uint32(w[16:]), le.Uint16(w[20:]), le.Uint16(w[22:]), le.Uint32(w[24:]), le.Uint16(w[34:]); size != 16 || format != 1 || chans != 1 || rate != 16000 || bits != 16 {
		t.Errorf("format chunk: size=%d format=%d channels=%d rate=%d bits=%d", size, format, chans, rate, bits)
	}
	if riff, data := le.Uint32(w[4:]), le.Uint32(w[40:]); int(riff) != len(w)-8 || int(data) != len(w)-44 {
		t.Errorf("sizes disagree with the file: riff=%d data=%d len=%d", riff, data, len(w))
	}
}
