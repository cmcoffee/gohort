package orchestrate

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// The reject paths need no network — fetchAttachmentURL returns before any GET
// for non-URLs and SSRF-guarded hosts.
func TestFetchAttachmentURLRejects(t *testing.T) {
	cases := []string{
		"find-abc.jpg",             // a workspace filename, not a URL
		"media#1",                  // an inbound media ref
		"ftp://example.com/x.jpg",  // non-http scheme
		"http://localhost/x.jpg",   // SSRF: localhost
		"http://127.0.0.1/x.jpg",   // SSRF: loopback
		"http://192.168.1.5/x.jpg", // SSRF: private
		"https://box.local/x.jpg",  // SSRF: .local
	}
	for _, ref := range cases {
		if _, _, ok := fetchAttachmentURL(ref); ok {
			t.Errorf("%q should be rejected without fetching", ref)
		}
	}
}

// fakeFetch answers every attachment fetch with one canned response.
type fakeFetch struct {
	ct   string
	body []byte
}

func (f fakeFetch) RoundTrip(r *http.Request) (*http.Response, error) {
	h := http.Header{}
	if f.ct != "" {
		h.Set("Content-Type", f.ct)
	}
	return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(bytes.NewReader(f.body)), Request: r}, nil
}

// A song given as a URL is attached, as audio, whether it is served as
// audio or as a plain download; other downloads still are not.
func TestASongURLIsAttachedAsAudio(t *testing.T) {
	prev := attachmentHTTP
	t.Cleanup(func() { attachmentHTTP = prev })
	untagged := []byte{0xff, 0xfb, 0x90, 0x64, 0x00, 0x0f, 0xf0, 0x00, 0x00, 0x69}
	for _, c := range []struct {
		name, url, ct string
		body          []byte
		want          bool
	}{
		{"served as audio", "https://songs.example.com/a.mp3", "audio/mpeg", untagged, true},
		{"a plain download, by its name", "https://songs.example.com/b.mp3", "application/octet-stream", untagged, true},
		{"an Ogg download", "https://songs.example.com/c.opus", "application/ogg", []byte("OggS\x00\x02OpusHead"), true},
		{"a plain download that is not a song", "https://files.example.com/d.zip", "application/octet-stream", []byte("PK\x03\x04junk"), false},
		{"a page", "https://www.example.com/e.mp3", "text/html", []byte("<html></html>"), false},
	} {
		attachmentHTTP = &http.Client{Transport: fakeFetch{ct: c.ct, body: c.body}}
		_, kind, ok := fetchAttachmentURL(c.url)
		if ok != c.want || (ok && kind != "video") {
			t.Errorf("%s: ok=%v kind=%q, want ok=%v on the audio/video list", c.name, ok, kind, c.want)
		}
	}
}

// A finished background task whose result is only a song still has something
// to send to the conversation.
func TestAResultThatIsOnlyASongIsSent(t *testing.T) {
	if !wakeHasSomethingToSend("", nil, []string{"c29uZw=="}) {
		t.Error("a song with no text and no picture was not sent")
	}
	if wakeHasSomethingToSend("", nil, nil) {
		t.Error("nothing at all should not be sent")
	}
	// And the channel delivery asks it, with the audio/video list.
	src, err := os.ReadFile("task_runner.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "if !wakeHasSomethingToSend(text, imgs, vids) {") {
		t.Error("deliverWakeToChannel does not decide by wakeHasSomethingToSend")
	}
}
