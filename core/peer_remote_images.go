// Image sharing, consuming half: turning a peer's advertised renderers into
// local image backends.
//
// Nothing here renders anything. It writes two kinds of ordinary record — a
// SecureAPI credential holding the peer key, and one rest_image connector per
// advertised renderer — and everything that already knows how to drive a
// rest_image backend takes it from there: the backend picker, edits, the
// cascade over multi-image composites, masks, the face-refine pass. That is the
// entire reason the serving endpoint speaks A1111 rather than something of its
// own.
//
// The key never lands in the connector spec. Connectors are drafted by agents
// and read by admins, and the file that defines them says plainly that no
// secret lives there — auth is a credential referenced BY NAME. A header
// credential carries it instead, scoped to the peer's host.
package core

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// peerConnectorSanitize strips anything a connector name may not hold.
var peerConnectorSanitize = regexp.MustCompile(`[^a-z0-9_-]+`)

// peerConnectorName builds the local name for a peer's renderer.
//
// connectorNameRE forbids dots, so the "<peer>.<remote>" form used for ring ids
// is unavailable and there is no separator that cannot also appear in either
// half. Rather than inventing an ambiguous one and parsing it back, the names
// this produces are STORED on the peer record — teardown reads the list instead
// of trying to recognize its own handiwork.
func peerConnectorName(peer, backend string) string {
	clean := func(s string) string {
		return strings.Trim(peerConnectorSanitize.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
	}
	name := "peer-" + clean(peer) + "-" + clean(backend)
	if len(name) > 64 {
		name = name[:64]
	}
	return strings.Trim(name, "-")
}

// peerCredentialSanitize strips anything a CREDENTIAL name may not hold.
// Deliberately stricter than the connector one: credential names allow letters,
// digits and underscores but NOT hyphens, and a peer nicknamed "gpu-box" is the
// obvious case that would otherwise be rejected on save with a regex the
// operator never typed.
var peerCredentialSanitize = regexp.MustCompile(`[^a-z0-9_]+`)

// peerCredentialName is the credential holding one peer's key.
func peerCredentialName(peer string) string {
	return "peer_" + strings.Trim(peerCredentialSanitize.ReplaceAllString(strings.ToLower(strings.TrimSpace(peer)), "_"), "_") + "_key"
}

// peerImageSubmitBody is the request template.
//
// {images} expands to a JSON array of base64 source photos, which is what makes
// SupportsImageInput true and the whole edit path light up. {prompt} and
// {negative} are JSON-escaped by the driver; the numeric tokens are inserted
// raw. backend names WHICH renderer on the far side, since one peer commonly
// offers several.
const peerImageSubmitBody = `{"prompt":"{prompt}","negative_prompt":"{negative}",` +
	`"init_images":{images},"backend":%s,"steps":{steps},"seed":{seed},` +
	`"width":{width},"height":{height}}`

// provisionPeerImages installs a credential and one connector per renderer the
// peer advertises, and returns the connector names it created.
//
// Approved on creation. The gate exists so an agent cannot quietly add outward
// reach; here the operator has already made that decision explicitly, by typing
// this peer's address and key into the admin UI. Leaving them pending would
// mean adding a peer, being told it offers rendering, and then finding nothing
// in the picker until a second approval nobody mentioned.
func provisionPeerImages(p RemotePeer, backends []PeerImageBackend) ([]string, error) {
	if RootDB == nil {
		return nil, fmt.Errorf("no database available")
	}
	if len(backends) == 0 {
		return nil, nil
	}
	credName := peerCredentialName(p.Name)
	if err := Secure().Save(SecureCredential{
		Name:              credName,
		Type:              SecureCredHeader,
		ParamName:         peerKeyHeader,
		BaseURL:           p.BaseURL,
		AllowedURLPattern: imageHostPattern(p.BaseURL),
		Description:       "Resource-sharing key for peer " + p.Name + ". Created with the peer; removed when it is forgotten.",
	}, p.Key); err != nil {
		return nil, fmt.Errorf("storing the peer key as a credential: %w", err)
	}

	var made []string
	for _, b := range backends {
		if strings.TrimSpace(b.Name) == "" {
			continue
		}
		remote, _ := json.Marshal(b.Name) // JSON-quoted, so an odd remote name cannot break the body
		spec := RestImageSpec{
			Credential:     credName,
			SubmitURL:      strings.TrimRight(p.BaseURL, "/") + "/api/peer/v1/images/render",
			SubmitMethod:   "POST",
			SubmitBody:     fmt.Sprintf(peerImageSubmitBody, string(remote)),
			ImageB64Path:   "images.0",
			MaxInputImages: b.MaxImages,
			PromptGuidance: b.Guidance,
		}
		raw, err := json.Marshal(spec)
		if err != nil {
			return made, err
		}
		name := peerConnectorName(p.Name, b.Name)
		c := Connector{
			Name: name,
			Kind: RestImageConnectorKind,
			Desc: "Renders on peer " + p.Name + " (" + b.Name + ")",
			Spec: raw,
		}
		if err := SaveConnector(RootDB, c); err != nil {
			return made, fmt.Errorf("creating backend %q: %w", name, err)
		}
		if err := ApproveConnector(RootDB, name); err != nil {
			return made, fmt.Errorf("enabling backend %q: %w", name, err)
		}
		made = append(made, name)
	}
	Log("[peer] %q contributed %d image backend(s): %s", p.Name, len(made), strings.Join(made, ", "))
	return made, nil
}

// teardownPeerImages removes what provisionPeerImages created.
//
// Driven by the stored name list rather than by pattern-matching connector
// names, so a connector the operator renamed or created themselves can never be
// swept up by a peer being forgotten.
func teardownPeerImages(p RemotePeer) {
	for _, name := range p.ImageConnectors {
		if err := DeleteConnector(RootDB, name); err != nil {
			Debug("[peer] removing backend %q: %v", name, err)
		}
	}
	if cred := peerCredentialName(p.Name); cred != "" {
		if exists, _, _ := Secure().CredentialStatus(cred); exists {
			if err := Secure().Delete(cred); err != nil {
				Debug("[peer] removing credential %q: %v", cred, err)
			}
		}
	}
}

// syncPeerImages brings a peer's local backends in line with what it currently
// advertises: provision when images are on offer, tear down when they are not.
//
// Called on every save and refresh, so a grant revoked on the far side stops
// offering a renderer here at the next check rather than leaving a backend in
// the picker that answers 403.
func syncPeerImages(p *RemotePeer, backends []PeerImageBackend) {
	if p.Offers(PeerCapImages) && len(backends) > 0 {
		// Drop the previous set first: a renderer removed on the far side must
		// not linger locally, and re-approving the survivors is cheap.
		teardownPeerImages(*p)
		made, err := provisionPeerImages(*p, backends)
		p.ImageConnectors = made
		if err != nil {
			p.LastError = err.Error()
			Log("[peer] %q image setup incomplete: %v", p.Name, err)
		}
		return
	}
	if len(p.ImageConnectors) > 0 {
		Log("[peer] %q no longer offers rendering — removing %d local backend(s)", p.Name, len(p.ImageConnectors))
		teardownPeerImages(*p)
		p.ImageConnectors = nil
	}
}
