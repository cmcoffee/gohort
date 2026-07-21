package orchestrate

import "testing"

// The reject paths need no network — fetchAttachmentURL returns before any GET
// for non-URLs and SSRF-guarded hosts.
func TestFetchAttachmentURLRejects(t *testing.T) {
	cases := []string{
		"find-abc.jpg",              // a workspace filename, not a URL
		"media#1",                   // an inbound media ref
		"ftp://example.com/x.jpg",   // non-http scheme
		"http://localhost/x.jpg",    // SSRF: localhost
		"http://127.0.0.1/x.jpg",    // SSRF: loopback
		"http://192.168.1.5/x.jpg",  // SSRF: private
		"https://box.local/x.jpg",   // SSRF: .local
	}
	for _, ref := range cases {
		if _, _, ok := fetchAttachmentURL(ref); ok {
			t.Errorf("%q should be rejected without fetching", ref)
		}
	}
}
