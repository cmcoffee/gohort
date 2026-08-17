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
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
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

// peerImageGenerateBody is the same request WITHOUT the {images} token.
//
// The distinction is load-bearing, and getting it wrong cost a working peer its
// only usable action. Locally, generators and editors are a strict PARTITION:
// a backend whose body contains {images} is classified as an editor and is
// removed from the generator list entirely, because an img2img graph has no
// text-only mode. Putting {images} in every peer connector therefore made even
// a remote TEXT-TO-IMAGE backend an editor here, left the generator list empty,
// and made the image tool refuse "generate" at preflight with "no
// image-generation provider is configured" — before the backend it was
// complaining about was ever consulted.
//
// So the token goes in only when the remote says it edits. The remote's own
// Edits flag is the authority: it reflects that graph's real shape.
const peerImageGenerateBody = `{"prompt":"{prompt}","negative_prompt":"{negative}",` +
	`"backend":%s,"steps":{steps},"seed":{seed},` +
	`"width":{width},"height":{height}}`

// peerImageConnector builds the local connector record one advertised renderer
// translates into.
//
// Pure on purpose — it writes nothing — so a re-sync can compare what it WOULD
// create against what is already stored, and leave a matching one untouched.
func peerImageConnector(p RemotePeer, credName string, b PeerImageBackend) (Connector, error) {
	remote, _ := json.Marshal(b.Name) // JSON-quoted, so an odd remote name cannot break the body
	body := peerImageGenerateBody
	if b.Edits {
		body = peerImageSubmitBody
	}
	spec := RestImageSpec{
		Credential:     credName,
		SubmitURL:      strings.TrimRight(p.BaseURL, "/") + "/api/peer/v1/images/render",
		SubmitMethod:   "POST",
		SubmitBody:     fmt.Sprintf(body, string(remote)),
		ImageB64Path:   "images.0",
		MaxInputImages: b.MaxImages,
		PromptGuidance: b.Guidance,
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return Connector{}, err
	}
	return Connector{
		Name: peerConnectorName(p.Name, b.Name),
		Kind: RestImageConnectorKind,
		Desc: "Renders on peer " + p.Name + " (" + b.Name + ")",
		Spec: raw,
	}, nil
}

// provisionPeerImages installs a credential and one connector per renderer the
// peer advertises, and returns the connector names this peer now owns.
//
// Approved on creation. The gate exists so an agent cannot quietly add outward
// reach; here the operator has already made that decision explicitly, by typing
// this peer's address and key into the admin UI. Leaving them pending would
// mean adding a peer, being told it offers rendering, and then finding nothing
// in the picker until a second approval nobody mentioned.
//
// Idempotent: a connector that already says exactly what this peer currently
// advertises is left alone. Re-provisioning runs on a timer, and rewriting an
// unchanged record every pass would re-materialize a live backend and re-approve
// one an operator had deliberately disabled.
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
		// SECURED: reachable only through a tool that declares it, which
		// is what the image connectors below do. Without this the
		// credential also produced an auto-generated call_<name> tool in
		// every agent's catalog and auto-routed for the peer's host —
		// handing the default pool an open request path to the peer,
		// carrying the sharing key, for a credential nobody chose to
		// grant. The connectors are unaffected: they name it.
		Secured: true,
		// And it is not an operator's credential to maintain. Peering
		// writes it on save and deletes it on forget, so offering it in
		// a picker invites a binding that the next peer save overwrites
		// or that breaks when the peer is dropped.
		Managed: "peer",
	}, p.Key); err != nil {
		return nil, fmt.Errorf("storing the peer key as a credential: %w", err)
	}

	var made, wrote []string
	for _, b := range backends {
		if strings.TrimSpace(b.Name) == "" {
			continue
		}
		c, err := peerImageConnector(p, credName, b)
		if err != nil {
			return made, err
		}
		if cur, ok := GetConnector(RootDB, c.Name); ok &&
			cur.Kind == c.Kind && cur.Desc == c.Desc && bytes.Equal(cur.Spec, c.Spec) {
			made = append(made, c.Name)
			continue
		}
		if err := SaveConnector(RootDB, c); err != nil {
			return made, fmt.Errorf("creating backend %q: %w", c.Name, err)
		}
		if err := ApproveConnector(RootDB, c.Name); err != nil {
			return made, fmt.Errorf("enabling backend %q: %w", c.Name, err)
		}
		made, wrote = append(made, c.Name), append(wrote, c.Name)
	}
	if len(wrote) > 0 {
		Log("[peer] %q contributed %d image backend(s): %s", p.Name, len(wrote), strings.Join(wrote, ", "))
	}
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
//
// It also repairs DRIFT, which is the failure this shape exists for: a peer's
// backends are a snapshot taken at provisioning time, and both ends move. A
// renderer whose graph was swapped from img2img to text-only on the far side —
// or one provisioned by an older build that marked every peer backend an editor
// — leaves a connector here claiming a capability the remote no longer has. The
// symptom is remote and unhelpful: source photos get shipped to a text-only
// backend, which refuses them, and the operator sees a 502 from a machine they
// were not looking at. Re-syncing rewrites the spec from what the peer says
// TODAY.
func syncPeerImages(p *RemotePeer, backends []PeerImageBackend) {
	if p.Offers(PeerCapImages) && len(backends) > 0 {
		prev := p.ImageConnectors
		made, err := provisionPeerImages(*p, backends)
		// Remove only what this peer used to contribute and no longer does — a
		// renderer withdrawn on the far side, or one whose name changed. The
		// previous shape dropped every backend first and rebuilt it, which was
		// fine when this ran only on an operator's click and is not when it
		// runs on a timer: it would delete and recreate a working, in-use
		// connector on every pass.
		for _, old := range prev {
			if !slices.Contains(made, old) {
				if err := DeleteConnector(RootDB, old); err != nil {
					Debug("[peer] removing withdrawn backend %q: %v", old, err)
				}
			}
		}
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
