package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// The consuming side does not render. It writes a credential and one rest_image
// connector per advertised renderer, and everything that already drives a
// rest_image backend takes over — which is why the serving endpoint speaks
// A1111 instead of a shape of its own.
//
// This pins the connector it writes: if any of these drift, edits silently stop
// working while generation appears fine.
func TestProvisionedPeerBackendDrivesTheExistingClient(t *testing.T) {
	peerImageDB(t)
	p := RemotePeer{
		Name: "gpu-box", BaseURL: "https://gpu.example",
		Key: "secret-key", Caps: []string{PeerCapImages},
	}
	made, err := provisionPeerImages(p, []PeerImageBackend{
		{Name: "comfy", Edits: true, MaxImages: 3, Guidance: "keep prompts literal"},
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if len(made) != 1 {
		t.Fatalf("made %d connectors, want 1", len(made))
	}

	c, ok := GetConnector(RootDB, made[0])
	if !ok {
		t.Fatalf("connector %q was not stored", made[0])
	}
	if !c.Approved {
		t.Error("the connector is not approved, so it never materialized and the picker stays empty")
	}
	var spec RestImageSpec
	if err := json.Unmarshal(c.Spec, &spec); err != nil {
		t.Fatalf("spec: %v", err)
	}

	// {images} in the body is what makes this an EDITING backend. Without it the
	// peer offers generation only, and a face swap silently has nowhere to go.
	if !spec.SupportsImageInput() {
		t.Errorf("the spec does not accept source images — edits will refuse: body=%q", spec.SubmitBody)
	}
	if spec.MaxImages() != 3 {
		t.Errorf("max images = %d, want the remote's 3 — the cascade sizes itself from this",
			spec.MaxImages())
	}
	if !strings.HasSuffix(spec.SubmitURL, "/api/peer/v1/images/render") {
		t.Errorf("submit url = %q", spec.SubmitURL)
	}
	if spec.ImageB64Path != "images.0" {
		t.Errorf("b64 path = %q — the render comes back inline and nothing would find it", spec.ImageB64Path)
	}
	// The body must name WHICH remote renderer; one peer commonly offers several.
	if !strings.Contains(spec.SubmitBody, `"backend":"comfy"`) {
		t.Errorf("the body does not name the remote backend: %q", spec.SubmitBody)
	}
	if spec.PromptGuidance != "keep prompts literal" {
		t.Errorf("guidance was dropped: %q", spec.PromptGuidance)
	}
}

// The key must NOT be in the connector spec. Connectors are drafted by agents
// and read by admins; the file defining them says plainly that no secret lives
// there, and auth is a credential referenced by name.
func TestPeerKeyNeverLandsInTheConnectorSpec(t *testing.T) {
	peerImageDB(t)
	p := RemotePeer{Name: "gpu-box", BaseURL: "https://gpu.example", Key: "super-secret-key"}
	made, err := provisionPeerImages(p, []PeerImageBackend{{Name: "comfy"}})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	c, _ := GetConnector(RootDB, made[0])
	if strings.Contains(string(c.Spec), "super-secret-key") {
		t.Fatal("the peer key is stored in the connector spec, where every agent that reads connectors can see it")
	}
	var spec RestImageSpec
	json.Unmarshal(c.Spec, &spec)
	if spec.Credential == "" {
		t.Fatal("no credential named — the request would go out unauthenticated and 401")
	}
	if exists, _, hasSecret := Secure().CredentialStatus(spec.Credential); !exists || !hasSecret {
		t.Errorf("credential %q: exists=%v hasSecret=%v — the key was not stored where auth reads it",
			spec.Credential, exists, hasSecret)
	}
}

// Forgetting a peer takes its backends with it. Left behind they would sit in
// the picker pointed at a machine that is no longer configured, and the
// credential would outlive the reason it existed.
func TestForgettingAPeerRemovesItsBackends(t *testing.T) {
	peerImageDB(t)
	p := RemotePeer{Name: "gpu-box", BaseURL: "https://gpu.example", Key: "k", Caps: []string{PeerCapImages}}
	made, _ := provisionPeerImages(p, []PeerImageBackend{{Name: "comfy"}, {Name: "flux"}})
	p.ImageConnectors = made
	RootDB.Set(remotePeersTable, p.Name, p)
	if len(made) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(made))
	}

	if !DeleteRemotePeer("gpu-box") {
		t.Fatal("delete failed")
	}
	for _, name := range made {
		if _, ok := GetConnector(RootDB, name); ok {
			t.Errorf("connector %q survived the peer being forgotten", name)
		}
	}
	if exists, _, _ := Secure().CredentialStatus(peerCredentialName("gpu-box")); exists {
		t.Error("the peer's credential survived it")
	}
}

// Teardown is driven by the STORED name list, never by pattern-matching. An
// operator's own connector that happens to look like a peer's must survive.
func TestTeardownOnlyRemovesWhatItCreated(t *testing.T) {
	peerImageDB(t)
	p := RemotePeer{Name: "gpu-box", BaseURL: "https://gpu.example", Key: "k", Caps: []string{PeerCapImages}}
	made, _ := provisionPeerImages(p, []PeerImageBackend{{Name: "comfy"}})
	p.ImageConnectors = made

	// A hand-made connector whose name follows the same convention.
	mine := peerConnectorName("gpu-box", "handmade")
	spec, _ := json.Marshal(RestImageSpec{
		SubmitURL: "https://elsewhere.example/render", SubmitMethod: "POST",
		SubmitBody: `{"prompt":"{prompt}"}`, ImageB64Path: "images.0", Credential: "no_auth",
	})
	if err := SaveConnector(RootDB, Connector{Name: mine, Kind: RestImageConnectorKind, Spec: spec}); err != nil {
		t.Fatalf("saving the operator's own connector: %v", err)
	}

	teardownPeerImages(p)
	if _, ok := GetConnector(RootDB, mine); !ok {
		t.Error("a connector the operator created was swept up by a peer teardown")
	}
	if _, ok := GetConnector(RootDB, made[0]); ok {
		t.Error("the peer's own connector survived teardown")
	}
}

// A renderer withdrawn on the far side stops being offered here at the next
// check, rather than leaving a backend in the picker that answers 403.
func TestSyncRemovesBackendsThePeerNoLongerOffers(t *testing.T) {
	peerImageDB(t)
	p := RemotePeer{Name: "gpu-box", BaseURL: "https://gpu.example", Key: "k", Caps: []string{PeerCapImages}}

	syncPeerImages(&p, []PeerImageBackend{{Name: "comfy"}, {Name: "flux"}})
	if len(p.ImageConnectors) != 2 {
		t.Fatalf("provisioned %d, want 2", len(p.ImageConnectors))
	}
	first := append([]string(nil), p.ImageConnectors...)

	// The far side now offers only one.
	syncPeerImages(&p, []PeerImageBackend{{Name: "comfy"}})
	if len(p.ImageConnectors) != 1 {
		t.Errorf("after the peer dropped a renderer, %d local backends remain", len(p.ImageConnectors))
	}
	var live int
	for _, name := range first {
		if _, ok := GetConnector(RootDB, name); ok {
			live++
		}
	}
	if live != 1 {
		t.Errorf("%d of the original connectors still exist, want 1", live)
	}

	// And the images grant going away removes the rest.
	p.Caps = nil
	syncPeerImages(&p, nil)
	if len(p.ImageConnectors) != 0 {
		t.Errorf("the grant was revoked but %d backends remain", len(p.ImageConnectors))
	}
}

// Connector names may not contain dots, so the ring-id form is unavailable and
// the name has to be sanitized. It must stay valid whatever the peer and remote
// backend are called, or the save fails with a regex the operator never saw.
func TestPeerConnectorNamesAreAlwaysValid(t *testing.T) {
	for _, tc := range [][2]string{
		{"gpu-box", "comfy"},
		{"GPU_Box", "Flux.1 Dev"},
		{"mac", "sd xl / turbo"},
		{"a", strings.Repeat("x", 200)},
	} {
		name := peerConnectorName(tc[0], tc[1])
		if !connectorNameRE.MatchString(name) {
			t.Errorf("peerConnectorName(%q,%q) = %q, which SaveConnector will reject", tc[0], tc[1], name)
		}
	}
}
