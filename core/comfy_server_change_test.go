// Changing a ComfyUI connector's server was impossible without restarting.
//
// ApplyRestImagePreset derives upload_url from the CURRENT base_url, and then
// the submitted upload_url overrode it — but the Configure panel prefills that
// field from the STORED spec, so a re-save wrote the old host straight back
// over the freshly-derived one. sameImageHost then refused the save, from a
// field marked Advanced that the admin never sees:
//
//	upload_url host "alpaca.snuglab.locl:8188" must match
//	submit_url host "alpaca.snuglab.local:8188"
//
// Two nearly identical strings, no way forward, and the real cause hidden
// behind a disclosure toggle.
package core

import (
	"strings"
	"testing"
)

func comfyVals(baseURL, uploadURL string) map[string]any {
	v := map[string]any{
		"base_url":      baseURL,
		"workflow_type": ComfyTypeGenerate,
		"credential":    "no_auth",
	}
	if uploadURL != "" {
		v["upload_url"] = uploadURL
	}
	return v
}

// The reported case: the panel round-trips a stale upload_url while the admin
// corrects the server. The new host must win.
func TestChangingTheServerCarriesTheUploadEndpointWithIt(t *testing.T) {
	stale := "http://alpaca.snuglab.locl:8188/upload/image"
	s := buildComfy(t, comfyVals("http://alpaca.snuglab.local:8188", stale))

	if strings.Contains(s.UploadURL, "locl") {
		t.Fatalf("the old host survived the edit: %q", s.UploadURL)
	}
	if !strings.Contains(s.UploadURL, "alpaca.snuglab.local:8188") {
		t.Errorf("upload_url must follow base_url, got %q", s.UploadURL)
	}
	// And the pair the validator compares must now agree.
	if err := sameImageHost(s.SubmitURL, s.UploadURL); err != nil {
		t.Errorf("the save would still be refused: %v", err)
	}
}

// A genuinely custom endpoint is a choice, not residue, and must survive.
func TestACustomUploadPathIsPreserved(t *testing.T) {
	custom := "http://alpaca.snuglab.local:8188/api/v2/put-image"
	s := buildComfy(t, comfyVals("http://alpaca.snuglab.local:8188", custom))
	if s.UploadURL != custom {
		t.Errorf("custom upload endpoint = %q, want %q", s.UploadURL, custom)
	}
}

func TestIsDerivedUploadURL(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"", true},
		{"{base_url}/upload/image", true},
		{"http://box:8188/upload/image", true},
		{"http://other-box:8188/upload/image", true}, // stale host, still the default shape
		{"http://box:8188/upload/image/", true},
		{"http://box:8188/api/v2/put-image", false},
		{"{base_url}/custom/upload", false},
	} {
		if got := isDerivedUploadURL(tc.in); got != tc.want {
			t.Errorf("isDerivedUploadURL(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// Moving the server also has to move poll/view URLs, or generation keeps
// talking to the old box even once the save goes through.
func TestTheWholeSpecFollowsTheNewServer(t *testing.T) {
	s := buildComfy(t, comfyVals("http://newbox:9000", "http://oldbox:8188/upload/image"))
	for name, got := range map[string]string{
		"submit_url": s.SubmitURL,
		"upload_url": s.UploadURL,
		"poll_url":   s.PollURL,
	} {
		if strings.Contains(got, "oldbox") {
			t.Errorf("%s still points at the old server: %q", name, got)
		}
	}
}
