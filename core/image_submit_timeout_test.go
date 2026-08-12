package core

// A SYNCHRONOUS image backend answers the submit with the finished picture, so
// the submit IS the render. It was governed by tune_secure_api_request_timeout
// — 30 seconds, a cap sized for ordinary API calls — while the image deadline
// the operator actually set (900s for an edit) governed only the poll loop a
// synchronous backend never reaches.
//
// A peer render is exactly this shape: the far side runs the whole job under
// its own ten-minute budget and replies with pixels. Every peer edit that took
// longer than half a minute came back as "den.snuglab.com did not respond
// within 30s", which reads as a network fault rather than a deadline nobody
// could see.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

func TestSynchronousImageSubmitGetsTheRenderDeadline(t *testing.T) {
	sync := RestImageSpec{
		SubmitURL: "http://gpu.example/render", SubmitMethod: "POST",
		SubmitBody: `{"prompt":"{prompt}","init_images":{images}}`, ImageB64Path: "images.0",
	}
	// An editing spec, so it takes the (longer) edit deadline.
	if !sync.SupportsImageInput() {
		t.Fatal("the fixture is meant to be an editing backend")
	}
	got := sync.submitTimeoutSecs()
	want := int(sync.pollDeadline() / time.Second)
	if got != want {
		t.Errorf("submit timeout = %ds, want the render deadline %ds", got, want)
	}
	if got <= int(secureAPIRequestTimeout()/time.Second) {
		t.Errorf("submit timeout %ds is no better than the general API cap — the bug is unfixed", got)
	}

	// An explicit per-connector deadline still wins.
	sync.PollMaxSecs = 1234
	if got := sync.submitTimeoutSecs(); got != 1234 {
		t.Errorf("an explicit PollMaxSecs must govern the submit too, got %ds", got)
	}
}

func TestAsyncImageSubmitKeepsTheGeneralCap(t *testing.T) {
	// With a poll URL the submit should return an id immediately and the poll
	// loop does the waiting. Letting the submit hang for fifteen minutes there
	// would hide a genuinely wedged backend.
	async := RestImageSpec{
		SubmitURL: "http://gpu.example/prompt", SubmitMethod: "POST",
		PollURL: "http://gpu.example/history/{id}", ImageB64Path: "images.0",
	}
	if got := async.submitTimeoutSecs(); got != 0 {
		t.Errorf("submit timeout = %ds, want 0 (leave the general cap alone) for a polling backend", got)
	}
}

func TestDispatchReadsTheTimeoutOverrideWhateverItsNumericType(t *testing.T) {
	// The args map is hand-built in one place and JSON-decoded in another, so
	// the same value arrives as int here and float64 there. An override that is
	// silently ignored is the exact failure this change removes.
	for _, v := range []any{900, int64(900), float64(900)} {
		if got := secureTimeoutSeconds(map[string]any{secureTimeoutArg: v}); got != 900 {
			t.Errorf("%T override read as %d, want 900", v, got)
		}
	}
	if got := secureTimeoutSeconds(map[string]any{}); got != 0 {
		t.Errorf("absent override read as %d, want 0", got)
	}
}

// End to end through the real dispatch: a backend slower than the general API
// cap must still be waited for. Uses a tiny override rather than a real 30s
// wait — the point is that the SPEC's number governs, not the general one.
func TestASlowSynchronousRenderIsNotCutOffByTheAPICap(t *testing.T) {
	peerImageDB(t)
	prev := ImageDir()
	SetImageDir(t.TempDir())
	t.Cleanup(func() { SetImageDir(prev) })

	// General API cap: 1s. Render deadline: 30s. The backend takes ~1.4s, so it
	// finishes only if the render deadline is the one being enforced.
	tdb := &DBase{Store: kvlite.MemStore()}
	tdb.Set(WebTable, "tune_secure_api_request_timeout", float64(1))
	SetTunablesDB(tdb)
	t.Cleanup(func() { SetTunablesDB(nil) })

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1400 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"images":[%q]}`, tinyPNG)
	}))
	t.Cleanup(slow.Close)

	spec := RestImageSpec{
		SubmitURL: slow.URL + "/sdapi/v1/img2img", SubmitMethod: "POST",
		SubmitBody:   `{"prompt":"{prompt}","init_images":{images}}`,
		ImageB64Path: "images.0", Credential: "no_auth",
		MaxInputImages: 1, PollMaxSecs: 30,
	}
	raw, _ := json.Marshal(spec)
	if err := SaveConnector(RootDB, Connector{Name: "slowedit", Kind: RestImageConnectorKind, Spec: raw}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := ApproveConnector(RootDB, "slowedit"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	src := filepath.Join(ImageDir(), "src.png")
	png, _ := base64.StdEncoding.DecodeString(tinyPNG)
	if err := os.WriteFile(src, png, 0644); err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{WorkspaceDir: ImageDir()}
	res, err := EditImageWithBackend(sess, EditImageRequest{
		Backend: "slowedit", Prompt: "one legible number on the left pillar",
		Images: []string{"src.png"},
	})
	if err != nil {
		t.Fatalf("a render slower than the general API cap was cut off: %v", err)
	}
	if res == nil || res.URL == "" {
		t.Fatal("no image came back")
	}
}
