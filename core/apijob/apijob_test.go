package apijob

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fakeAPI answers polls from a script of bodies, the last repeating.
type fakeAPI struct {
	bodies []string
	urls   []string
}

func (f *fakeAPI) call(_ context.Context, method, rawURL string) (Response, error) {
	f.urls = append(f.urls, method+" "+rawURL)
	i := len(f.urls) - 1
	if i >= len(f.bodies) {
		i = len(f.bodies) - 1
	}
	return Response{Status: 200, Body: []byte(f.bodies[i])}, nil
}

func fast(s Spec) Spec { s.IntervalSecs = MinInterval; return s }

// A job is polled at its URL (the id filled in, relative to the submit URL)
// until its ready field says done, and its result is the part asked for.
func TestAJobIsPolledUntilDone(t *testing.T) {
	api := &fakeAPI{bodies: []string{
		`{"status":"running"}`,
		`{"status":"succeeded","output":{"text":"the transcript"}}`,
	}}
	s := fast(Spec{IDPath: "id", PollURL: "/v1/jobs/{id}", ReadyPath: "status", ReadyValues: []string{"succeeded"}, ResultPath: "output.text"})
	polls := 0
	res, err := Run(context.Background(), s, "https://api.example/v1/submit", []byte(`{"id":"j 1"}`), api.call,
		func(time.Duration, int) { polls++ })
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "j 1" || res.Output != "the transcript" || res.Polls != 2 || polls != 2 {
		t.Errorf("done on the second poll with the result: %+v", res)
	}
	if api.urls[0] != "GET https://api.example/v1/jobs/j%201" {
		t.Errorf("the poll URL resolves against the submit URL with the id escaped: %s", api.urls[0])
	}
}

// A failed job says so with its reason, without waiting out the clock.
func TestAFailedJobSaysWhy(t *testing.T) {
	api := &fakeAPI{bodies: []string{`{"state":"FAILED","error":{"message":"prompt refused"}}`}}
	s := fast(Spec{IDPath: "id", PollURL: "https://x/{id}", ReadyPath: "result", ErrorPath: "state", ErrorValues: []string{"failed"}, ErrorDetailPath: "error.message"})
	_, err := Run(context.Background(), s, "https://x/s", []byte(`{"id":"a"}`), api.call, nil)
	if err == nil || !strings.Contains(err.Error(), "prompt refused") {
		t.Errorf("the failure and its reason: %v", err)
	}
}

// A job that produced a file says where it is: at a path, or built from
// fields the way ComfyUI's /view is, keyed by the job id.
func TestAJobFindsItsFile(t *testing.T) {
	comfy := &fakeAPI{bodies: []string{
		`{}`,
		`{"p1":{"outputs":{"9":{"images":[{"filename":"gohort 01.png","subfolder":"","type":"output"}]}}}}`,
	}}
	s := fast(Spec{IDPath: "prompt_id", PollURL: "/history/{id}", ReadyPath: "{id}.outputs.9.images.0.filename",
		FileURLTemplate: "/view?filename={filename}&type={type}",
		FileFields:      map[string]string{"filename": "{id}.outputs.9.images.0.filename", "type": "{id}.outputs.9.images.0.type"}})
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), s, "http://comfy.lan:8188/prompt", []byte(`{"prompt_id":"p1"}`), comfy.call, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.FileURL != "http://comfy.lan:8188/view?filename=gohort+01.png&type=output" || res.FileName != "gohort_01.png" {
		t.Errorf("the file URL is built from the fields: %q named %q", res.FileURL, res.FileName)
	}

	song := &fakeAPI{bodies: []string{`{"done":true,"audio":{"url":"https://cdn.example/out/abc.mp3?sig=x"}}`}}
	res, err = Run(context.Background(), fast(Spec{IDPath: "name", PollURL: "/ops/{id}", ReadyPath: "done", ReadyValues: []string{"true"}, FileURLPath: "audio.url"}),
		"https://api.example/v1/songs", []byte(`{"name":"ops/9"}`), song.call, nil)
	if err != nil || res.FileURL != "https://cdn.example/out/abc.mp3?sig=x" || res.FileName != "abc.mp3" {
		t.Errorf("a file at a path, named from the URL: %+v %v", res, err)
	}
}

// A job that never finishes gives up at its deadline and says it may still
// finish; a stop ends the wait at once.
func TestAJobGivesUpAtItsDeadline(t *testing.T) {
	api := &fakeAPI{bodies: []string{`{"status":"running"}`}}
	s := Spec{IDPath: "id", PollURL: "/j/{id}", ReadyPath: "status", ReadyValues: []string{"done"}, IntervalSecs: 1, MaxSecs: 1}
	_, err := Run(context.Background(), s, "https://x/s", []byte(`{"id":"a"}`), api.call, nil)
	if err == nil || !strings.Contains(err.Error(), "may still finish") {
		t.Errorf("out of time, it says the job may still finish: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, fast(Spec{PollURL: "https://x/j", ReadyPath: "status"}), "https://x/s", []byte(`{}`), api.call, nil); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Errorf("a stop ends the wait: %v", err)
	}
}

// A spec that cannot run is refused where it is written.
func TestABrokenJobSpecIsRefused(t *testing.T) {
	for name, s := range map[string]Spec{
		"no poll url":     {ReadyPath: "x"},
		"no ready path":   {PollURL: "https://x"},
		"id with no path": {PollURL: "https://x/{id}", ReadyPath: "x"},
		"two file ways":   {PollURL: "https://x", ReadyPath: "x", FileURLPath: "a", FileURLTemplate: "b"},
		"unused field":    {PollURL: "https://x", ReadyPath: "x", FileURLTemplate: "/v?f={f}", FileFields: map[string]string{"g": "a"}},
		"too long":        {PollURL: "https://x", ReadyPath: "x", MaxSecs: MaxMax + 1},
	} {
		if s.Validate() == nil {
			t.Errorf("%s should be refused", name)
		}
	}
	if s, err := Parse(`{"poll_url":"https://x/{id}","id_path":"id","ready_path":"done"}`); err != nil || s == nil || s.Deadline() != DefaultMax*time.Second {
		t.Errorf("parsed from JSON text with defaults: %+v %v", s, err)
	}
}

// A selector picks the element of a list by one of its fields, where a fixed
// index cannot: a reply whose image part is sometimes first and sometimes
// second. A value may hold a dot.
func TestASelectorPicksTheElementByItsField(t *testing.T) {
	var node any
	json.Unmarshal([]byte(`{"parts":[{"type":"text","text":"here"},{"type":"image","data":"AAA"}],
		"files":[{"name":"take.mp3","url":"u1"}]}`), &node)
	for path, want := range map[string]string{
		"parts[type=image].data":   "AAA",
		"parts.1.data":             "AAA",
		"files[name=take.mp3].url": "u1",
		"parts[type=video].data":   "",
		"parts[type=image]":        `{"data":"AAA","type":"image"}`,
		"parts[data].data":         "AAA",
		"parts[missing].data":      "",
	} {
		if got := Lookup(node, path, ""); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	for _, bad := range []string{"a..b", "parts[]", "parts[=image]", "parts[type=image", "a]b"} {
		if ValidPath(bad) == nil {
			t.Errorf("%q passed as a path", bad)
		}
	}
	for _, good := range []string{"", "a.b.0", "parts[type=image].data", "parts[data].data", "files[name=a.b].url"} {
		if err := ValidPath(good); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
}

// Check decides each poll in place of the ready and error paths.
func TestACheckDecidesEachPoll(t *testing.T) {
	polls := 0
	call := func(ctx context.Context, method, rawURL string) (Response, error) {
		polls++
		state := "running"
		if polls == 3 {
			state = "done"
		}
		return Response{Status: 200, Body: []byte(`{"state":"` + state + `"}`)}, nil
	}
	s := Spec{PollURL: "https://x.test/jobs/1", IntervalSecs: 1, MaxSecs: 30,
		Check: func(node any, id string) (bool, error) { return Lookup(node, "state", id) == "done", nil }}
	res, err := Run(context.Background(), s, "https://x.test/jobs", []byte(`{}`), call, nil)
	if err != nil || res.Polls != 3 {
		t.Fatalf("run: %v after %d polls", err, res.Polls)
	}
}
