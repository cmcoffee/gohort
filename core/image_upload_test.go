package core

// The upload leg. ComfyUI's graph references an input by SERVER-SIDE filename,
// so the bytes have to land on the backend before the graph runs — and that
// request is the first multipart body to go through the governed dispatch,
// which had no content-type channel on the local/no_auth path at all.

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// secureAPITestStore points the process-wide SecureAPI at an in-memory store so
// the governed dispatch runs for real (allow-list, audit, no auth) rather than
// failing with "store not initialized".
func secureAPITestStore(t *testing.T) {
	t.Helper()
	secureAPIInstanceMu.Lock()
	saved := secureAPIInstance
	secureAPIInstance = &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	secureAPIInstanceMu.Unlock()
	t.Cleanup(func() {
		secureAPIInstanceMu.Lock()
		secureAPIInstance = saved
		secureAPIInstanceMu.Unlock()
	})
}

// smallPNG is a real, decodable image.
func smallPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{B: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// captured is what the fake backend saw.
type captured struct {
	contentType string
	field       string
	filename    string
	body        []byte
	subfolder   string
}

func uploadServer(t *testing.T, got *captured, reply string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.contentType = r.Header.Get("Content-Type")
		mediaType, params, err := mime.ParseMediaType(got.contentType)
		if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
			http.Error(w, "not multipart", http.StatusUnsupportedMediaType)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			buf := make([]byte, 4096)
			n, _ := part.Read(buf)
			switch part.FormName() {
			case "subfolder":
				got.subfolder = string(buf[:n])
			default:
				if part.FileName() != "" {
					got.field, got.filename, got.body = part.FormName(), part.FileName(), buf[:n]
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
}

func TestUploadSendsMultipartThroughTheGovernedDispatch(t *testing.T) {
	secureAPITestStore(t)
	var got captured
	srv := uploadServer(t, &got, `{"name":"stored.png","subfolder":"gohort","type":"input"}`)
	defer srv.Close()

	s := RestImageSpec{
		Credential:          "no_auth",
		SubmitURL:           srv.URL + "/prompt",
		UploadURL:           srv.URL + "/upload/image",
		UploadFileField:     "image",
		UploadNamePath:      "name",
		UploadSubfolderPath: "subfolder",
	}
	png := smallPNG(t)
	up, err := s.uploadImage(&ToolSession{}, inputImage{name: "photo.png", data: png, mime: "image/png"})
	if err != nil {
		t.Fatalf("uploadImage: %v", err)
	}
	if up.Name != "stored.png" || up.Subfolder != "gohort" {
		t.Errorf("upload result = %+v, want the server's name + subfolder", up)
	}
	// The graph references "subfolder/name"; dropping either half sends the
	// backend looking in the wrong place.
	if up.Ref() != "gohort/stored.png" {
		t.Errorf("Ref() = %q, want gohort/stored.png", up.Ref())
	}
	// Content-Type is the thing the local dispatch path used to have no way to
	// set — a multipart body sent as application/json is rejected outright.
	if !strings.HasPrefix(got.contentType, "multipart/form-data") {
		t.Errorf("content-type = %q, want multipart/form-data", got.contentType)
	}
	if got.field != "image" {
		t.Errorf("file field = %q, want the spec's upload_file_field", got.field)
	}
	if string(got.body) != string(png) {
		t.Error("the uploaded bytes are not the image bytes")
	}
}

func TestUploadNameIsUniquePerCall(t *testing.T) {
	// ComfyUI keys its input store by filename. Two turns uploading "photo.png"
	// would share a key, and one could replace the other between its upload and
	// its graph run — rendering the wrong photo, silently.
	secureAPITestStore(t)
	var a, b captured
	srvA := uploadServer(t, &a, `{"name":"x.png"}`)
	defer srvA.Close()
	srvB := uploadServer(t, &b, `{"name":"x.png"}`)
	defer srvB.Close()

	png := smallPNG(t)
	specFor := func(url string) RestImageSpec {
		return RestImageSpec{Credential: "no_auth", SubmitURL: url + "/prompt", UploadURL: url + "/upload/image", UploadNamePath: "name"}
	}
	if _, err := specFor(srvA.URL).uploadImage(&ToolSession{}, inputImage{name: "photo.png", data: png}); err != nil {
		t.Fatalf("upload a: %v", err)
	}
	if _, err := specFor(srvB.URL).uploadImage(&ToolSession{}, inputImage{name: "photo.png", data: png}); err != nil {
		t.Fatalf("upload b: %v", err)
	}
	if a.filename == b.filename {
		t.Errorf("both uploads used %q — a shared key lets one call overwrite another", a.filename)
	}
	if !strings.HasSuffix(a.filename, ".png") {
		t.Errorf("filename %q lost its extension; backends sniff format from it", a.filename)
	}
}

func TestUploadFailureIsReportedNotSwallowed(t *testing.T) {
	secureAPITestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"disk full"}`, http.StatusInsufficientStorage)
	}))
	defer srv.Close()

	s := RestImageSpec{Credential: "no_auth", SubmitURL: srv.URL + "/prompt", UploadURL: srv.URL + "/upload/image", UploadNamePath: "name"}
	_, err := s.uploadImage(&ToolSession{}, inputImage{name: "photo.png", data: smallPNG(t)})
	if err == nil {
		t.Fatal("a failed upload must not be swallowed — the graph would run on a stale input")
	}
	if !strings.Contains(err.Error(), "507") {
		t.Errorf("error should carry the status: %v", err)
	}
}

func TestUploadWithoutANameInTheResponseFails(t *testing.T) {
	// No filename means nothing to write into the LoadImage node. Proceeding
	// would render the workflow's placeholder image and look like success.
	secureAPITestStore(t)
	var got captured
	srv := uploadServer(t, &got, `{"ok":true}`)
	defer srv.Close()

	s := RestImageSpec{Credential: "no_auth", SubmitURL: srv.URL + "/prompt", UploadURL: srv.URL + "/upload/image", UploadNamePath: "name"}
	if _, err := s.uploadImage(&ToolSession{}, inputImage{name: "photo.png", data: smallPNG(t)}); err == nil {
		t.Fatal("an upload response with no filename must fail")
	}
}

func TestCrossHostUploadURLIsRejectedAtValidate(t *testing.T) {
	// The no_auth dispatch is scoped to the submit URL's host, so a cross-host
	// upload_url is refused at dispatch with an opaque allow-list error. Catch
	// it at save time, where it can say what's wrong.
	spec, _, err := NewComfyImageSpec("http://localhost:8188", "no_auth", ComfyEditDefaultGraph(), "")
	if err != nil {
		t.Fatalf("NewComfyImageSpec: %v", err)
	}
	spec.UploadURL = "http://elsewhere:8188/upload/image"
	if err := spec.validateImageInput(); err == nil {
		t.Fatal("a cross-host upload_url must be rejected")
	} else if !strings.Contains(err.Error(), "host") {
		t.Errorf("error should name the host mismatch: %v", err)
	}
}

func TestImageInputNodesRequireAnUploadEndpoint(t *testing.T) {
	spec, _, err := NewComfyImageSpec("http://localhost:8188", "no_auth", ComfyEditDefaultGraph(), "")
	if err != nil {
		t.Fatalf("NewComfyImageSpec: %v", err)
	}
	spec.UploadURL = ""
	if err := spec.validateImageInput(); err == nil {
		t.Fatal("image nodes with no upload_url must be rejected")
	}
	if spec.SupportsImageInput() {
		t.Error("without an upload endpoint the backend cannot take images")
	}
}
