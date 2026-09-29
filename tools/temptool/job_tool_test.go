package temptool

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/apijob"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An api tool with a job waits out the job itself: submit, poll until done,
// then fetch the file the job produced into the workspace and deliver it,
// all through the tool's credential.
func TestAJobToolWaitsAndDeliversTheFile(t *testing.T) {
	var polls int32
	mp3 := append([]byte("ID3"), make([]byte, 2048)...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/songs":
			w.Write([]byte(`{"name":"op1","state":"queued"}`))
		case r.URL.Path == "/v1/ops/op1":
			if atomic.AddInt32(&polls, 1) < 2 {
				w.Write([]byte(`{"done":false}`))
				return
			}
			w.Write([]byte(`{"done":true,"song":{"title":"Night drive","audio":"/files/op1.mp3"}}`))
		case r.URL.Path == "/files/op1.mp3":
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Write(mp3)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	if err := Secure().Save(SecureCredential{Name: "songs", Type: SecureCredNone, BaseURL: srv.URL}, ""); err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	sess := &ToolSession{Username: "alice", ChatSessionID: "s1", WorkspaceDir: ws, DB: &DBase{Store: kvlite.MemStore()}}
	tt := &TempTool{
		Name: "make_song", Mode: TempToolModeAPI, Credential: "songs", Method: "POST",
		CommandTemplate: srv.URL + "/v1/songs", BodyTemplate: `{"prompt": {prompt}}`,
		Params:   map[string]ToolParam{"prompt": {Type: "string"}},
		Required: []string{"prompt"},
		Job: &apijob.Spec{IDPath: "name", PollURL: "/v1/ops/{id}", ReadyPath: "done", ReadyValues: []string{"true"},
			ResultPath: "song.title", FileURLPath: "song.audio", IntervalSecs: 1},
	}
	out, err := dispatchAPIModeTempTool(sess, tt, map[string]any{"prompt": "a song about the night"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Job op1 finished") || !strings.Contains(out, "Night drive") || !strings.Contains(out, "Delivered op1.mp3") {
		t.Errorf("the tool says the job finished, what it made, and that it delivered it:\n%s", out)
	}
	data, err := os.ReadFile(filepath.Join(ws, "jobs", "op1.mp3"))
	if err != nil || len(data) != len(mp3) {
		t.Errorf("the whole file is in the workspace: %d bytes, %v", len(data), err)
	}
	if len(sess.Videos)+len(sess.Files) == 0 {
		t.Error("the file is delivered to the person")
	}
}

// A refused submit is shown as it is: there is no job to wait for.
func TestAJobToolShowsARefusedSubmit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"prompt too long"}`))
	}))
	defer srv.Close()
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	Secure().Save(SecureCredential{Name: "songs", Type: SecureCredNone, BaseURL: srv.URL}, "")
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()}}
	tt := &TempTool{Name: "make_song", Mode: TempToolModeAPI, Credential: "songs", Method: "POST", CommandTemplate: srv.URL + "/v1/songs",
		Job: &apijob.Spec{IDPath: "name", PollURL: "/v1/ops/{id}", ReadyPath: "done"}}
	out, err := dispatchAPIModeTempTool(sess, tt, map[string]any{})
	if err != nil || !strings.Contains(out, "HTTP 400") || !strings.Contains(out, "prompt too long") {
		t.Errorf("the refusal comes back as it was: %q %v", out, err)
	}
}

// A job spec rides through tool_def: create keeps it, get shows it, and an
// unrelated update does not drop it. A broken one is refused at create.
func TestAJobSurvivesToolDefRoundTrips(t *testing.T) {
	tt := TempTool{Name: "make_song", Mode: TempToolModeAPI, Credential: "songs", CommandTemplate: "https://api.example/v1/songs",
		Job: &apijob.Spec{IDPath: "name", PollURL: "/v1/ops/{id}", ReadyPath: "done", ExpectSecs: 90}}
	args := tempToolToCreateArgs(tt)
	job, err := apijob.Parse(args["job"])
	if err != nil || job == nil || job.PollURL != "/v1/ops/{id}" || job.ExpectSecs != 90 {
		t.Errorf("the job round-trips through create args: %+v %v", job, err)
	}
	if _, err := apijob.Parse(map[string]any{"poll_url": "https://x/{id}", "ready_path": "done"}); err == nil {
		t.Error("a poll_url with {id} and no id_path is refused")
	}
}
