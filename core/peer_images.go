// Image sharing, serving half: rendering on this instance's GPU for a peer.
//
// Wire shape is deliberately the A1111 img2img one — prompt/negative/steps/seed
// in, init_images as base64 in, {"images":[base64]} out — because RestImageSpec
// already drives exactly that. The consuming side does not need a client: it
// registers an ordinary rest_image connector pointed here, and every existing
// path (the backend picker, edits, the cascade over multi-image composites, the
// face-refine pass) works with no knowledge that the GPU is on another machine.
// Inventing a gohort-shaped protocol would have meant writing all of that again
// on the far side, and keeping the two in step forever.
package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// peerImageBudget bounds a single render. Generous — a multi-step edit on a
// busy GPU is minutes, not seconds — but finite, so a wedged backend cannot pin
// a peer's connection open indefinitely.
const peerImageBudget = 10 * time.Minute

// maxPeerImageBytes caps one inbound source photo after decoding. Renders are
// the one peer capability where the REQUEST carries real weight, and an
// unbounded one is a way to spend this instance's memory from off-machine.
const maxPeerImageBytes = 24 << 20

// peerImageRequest is the A1111-shaped body.
type peerImageRequest struct {
	Prompt     string   `json:"prompt"`
	Negative   string   `json:"negative_prompt"`
	InitImages []string `json:"init_images"` // base64, source photos for an edit
	Backend    string   `json:"backend"`     // which local backend to render on
	Steps      int      `json:"steps"`
	Seed       int      `json:"seed"`
	Width      int      `json:"width"`
	Height     int      `json:"height"`
}

// PeerImageBackend is one renderer this instance offers, as advertised in the
// manifest. The shape mirrors ImageBackendChoice because the consuming side
// turns each of these back into a local backend entry.
type PeerImageBackend struct {
	Name       string `json:"name"`
	Guidance   string `json:"guidance,omitempty"`
	Edits      bool   `json:"edits"`
	MaxImages  int    `json:"max_images,omitempty"`
	CascadeMax int    `json:"cascade_max,omitempty"`
	Default    bool   `json:"default,omitempty"`
}

// peerBackendEdits reports whether a locally-offered backend takes source
// photos. Reads the same list the manifest advertises, so what a peer is told
// and what it is held to cannot disagree.
func peerBackendEdits(name string) bool {
	for _, b := range ReachableImageBackends(nil) {
		if b.Name == name {
			return b.Edits
		}
	}
	return false
}

// peerImageBackends lists what this instance can render with.
//
// Session-free: ReachableImageBackends filters on a session's denied
// credentials, and a peer has no session and no credentials of its own. What it
// may use is decided by its key's capability grant, not by a user's scoping.
func peerImageBackends() []PeerImageBackend {
	var out []PeerImageBackend
	for _, c := range ReachableImageBackends(nil) {
		out = append(out, PeerImageBackend{
			Name: c.Name, Guidance: c.Guidance, Edits: c.Edits,
			MaxImages: c.MaxImages, CascadeMax: c.CascadeMax, Default: c.Default,
		})
	}
	return out
}

// HandlePeerImageRender serves POST /api/peer/v1/images/render.
func HandlePeerImageRender(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		peerDeny(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	k, ok := peerAuthorize(w, r, PeerCapImages)
	if !ok {
		return
	}
	var req peerImageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<20)).Decode(&req); err != nil {
		peerDeny(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" && len(req.InitImages) == 0 {
		peerDeny(w, http.StatusBadRequest, "a prompt or at least one source image is required")
		return
	}

	// The backend must be one this instance actually offers. Without this a peer
	// could name any connector by string and render through a backend the
	// operator never meant to share.
	backend := strings.TrimSpace(req.Backend)
	if backend != "" && !ImageBackendReachable(nil, backend) {
		peerDeny(w, http.StatusBadRequest, "no image backend named "+backend+" is available here")
		return
	}

	// Source photos aimed at a text-only renderer mean the CALLER is working
	// from a stale picture of what this instance offers: its connector was
	// provisioned when that backend still edited, or by a build that assumed
	// every peer backend did. Refusing here, as a bad request that names the
	// repair, beats letting it fall through to peerRenderEdit — that returns a
	// 502, which reads as "the machine I borrowed broke" and sends the operator
	// looking at the wrong end of the link.
	if len(req.InitImages) > 0 && backend != "" && !peerBackendEdits(backend) {
		// Hostname for the same reason the manifest carries one: the caller may
		// have several peers configured and has to know which one is stale.
		host, _ := os.Hostname()
		peerDeny(w, http.StatusBadRequest, "backend "+backend+" on "+host+" generates from text only and has no image "+
			"input, so it cannot edit a photo — this peer's record of what "+host+" offers is out of date; refresh the peer on that side")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), peerImageBudget)
	defer cancel()

	var (
		result *ImageGenResult
		err    error
	)
	if len(req.InitImages) > 0 {
		result, err = peerRenderEdit(req, backend)
	} else {
		result, err = GenerateImageWithBackend(ctx, backend, req.Prompt, req.Width >= req.Height)
	}
	if err != nil {
		peerDeny(w, http.StatusBadGateway, "render failed: "+err.Error())
		return
	}
	if result == nil || result.URL == "" {
		peerDeny(w, http.StatusBadGateway, "the backend returned nothing")
		return
	}

	// Read the bytes and answer inline. A URL is not forwarded: it would name a
	// host the peer may not reach, and asking it to fetch one is the SSRF shape
	// this codebase already refuses elsewhere.
	if strings.HasPrefix(result.URL, "http://") || strings.HasPrefix(result.URL, "https://") {
		peerDeny(w, http.StatusBadGateway,
			"this backend returns an image URL rather than pixels, which cannot be shared with a peer")
		return
	}
	data, readErr := os.ReadFile(result.URL)
	os.Remove(result.URL)
	if readErr != nil {
		peerDeny(w, http.StatusInternalServerError, "could not read the render: "+readErr.Error())
		return
	}
	Debug("[peer] %q rendered %d bytes on %q", k.Label, len(data), backend)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"images": []string{base64.StdEncoding.EncodeToString(data)},
	})
	touchPeerKey(k)
}

// peerRenderEdit runs an edit with the caller's source photos.
//
// A synthetic session carries them: EditImageWithBackend takes a *ToolSession
// because a local edit resolves refs out of one, and the images here arrive as
// bytes instead. RefineFaces stays off — the second pass is an agent-path
// choice, and a peer that wants it can ask for it on its own side, where the
// decision and the cost belong together.
//
// The session needs a workspace ROOT, not just the files. resolveInputImages
// resolves every ref against sess.WorkspaceDir and rejects absolute paths
// outright, so a bare &ToolSession{} plus full temp paths fell through to the
// last branch and failed with "no workspace available to read
// /opt/gohort/data/images/….png from" — an error quoting a serving-side path,
// returned to a peer, about a directory that exists and a file that had just
// been written to it. Rooting the session where writeImageTemp actually puts
// them and passing basenames puts the refs back inside the containment check
// they were always meant to pass.
func peerRenderEdit(req peerImageRequest, backend string) (*ImageGenResult, error) {
	sess := &ToolSession{WorkspaceDir: ImageDir()}
	refs := make([]string, 0, len(req.InitImages))
	for i, b64 := range req.InitImages {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil {
			return nil, fmt.Errorf("source image %d is not valid base64: %w", i+1, err)
		}
		if len(data) > maxPeerImageBytes {
			return nil, fmt.Errorf("source image %d is %d bytes, over the %d limit", i+1, len(data), maxPeerImageBytes)
		}
		path, err := writeImageTemp(data)
		if err != nil {
			return nil, err
		}
		defer os.Remove(path)
		refs = append(refs, filepath.Base(path))
	}
	return EditImageWithBackend(sess, EditImageRequest{
		Backend: backend,
		Prompt:  req.Prompt,
		Images:  refs,
		Steps:   req.Steps,
		Seed:    req.Seed,
	})
}
