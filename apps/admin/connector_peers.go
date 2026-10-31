package admin

// Peers are connectors too (docs/design/connectors-and-flows.md, stage 6):
// inside, what another gohort lends this one (its models, embedder, speech,
// renderer, search); outside, that instance and the key it issued. The rows
// are read from the peers as they are registered and from the settings that
// use them, so nothing stored moves: a peer is added, re-keyed and forgotten
// under Admin > Capabilities > Peers.
//
// A peer lends resources, never the instance (CLAUDE.md): a row describes
// what is borrowed and what here uses it, nothing of the far side's own.

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/service"
)

// peerRowPrefix names a peer's row: a colon no stored connector's name can
// hold, as for the built-in providers and the programs.
const peerRowPrefix = "peer:"

// peerConnector is one peer's row.
type peerConnector struct {
	name    string // peer:<name>
	peer    RemotePeer
	wrapper string
	status  string
	// uses are what here uses each capability the peer lends.
	uses map[string][]string
}

// peerEndpoint is where a capability is served on a peer, and what goes
// there and comes back, in the contract's words.
var peerEndpoint = map[string]struct{ path, sends, reads string }{
	PeerCapModels:     {"/api/peer/v1/chat/completions", "a conversation, for the model the tier names", "the model's reply, streamed"},
	PeerCapEmbeddings: {"/api/peer/v1/embeddings", "texts", "one vector per text"},
	PeerCapImages:     {"/api/peer/v1/images/render", "a prompt, and pictures to edit", "a picture"},
	PeerCapTranscribe: {"/api/peer/v1/audio/transcriptions", "audio", "its text"},
	PeerCapSpeech:     {"/api/peer/v1/audio/speech", "text and a voice", "audio"},
	PeerCapSearch:     {"/api/peer/v1/search", "a query", "results"},
	PeerCapBrowse:     {"/api/peer/v1/browse", "a public page's address", "the page as a browser renders it"},
	PeerCapExec:       {"/api/peer/v1/exec", "a command, for a system the key reaches", "its output"},
	PeerCapRelay:      {"/api/peer/v1/relay", "a standing connection from here", "messages for this instance, relayed"},
}

// peerCapOrder is the order a peer's capabilities are named in.
var peerCapOrder = []string{PeerCapModels, PeerCapEmbeddings, PeerCapImages, PeerCapTranscribe, PeerCapSpeech, PeerCapSearch, PeerCapBrowse, PeerCapExec, PeerCapRelay}

// peerConnectors are the registered peers, as connectors.
func (a *AdminApp) peerConnectors() []peerConnector {
	var out []peerConnector
	for _, p := range ListRemotePeers() {
		pc := peerConnector{name: peerRowPrefix + p.Name, peer: p, uses: a.peerUses(p)}
		pc.wrapper = peerWrapper(p)
		pc.status = peerStatus(pc)
		out = append(out, pc)
	}
	return out
}

// peerCaps are p's capabilities in the order they are named.
func peerCaps(p RemotePeer) []string {
	var out []string
	for _, c := range peerCapOrder {
		if p.Offers(c) {
			out = append(out, c)
		}
	}
	for _, c := range p.Caps {
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// peerUses reads which settings here name p, for each capability.
func (a *AdminApp) peerUses(p RemotePeer) map[string][]string {
	mine := PeerProviderValue(p.Name)
	is := func(v string) bool { return strings.EqualFold(strings.TrimSpace(v), mine) }
	uses := map[string][]string{}
	if auth := authDB(); auth != nil {
		for _, t := range []struct{ table, what string }{{LLMTable, "the worker LLM"}, {LeadLLMTable, "the lead LLM"}} {
			var provider string
			auth.Get(t.table, "provider", &provider)
			if is(provider) {
				uses[PeerCapModels] = append(uses[PeerCapModels], t.what)
			}
		}
	}
	if is(a.storedEmbeddingConfig().Provider) {
		uses[PeerCapEmbeddings] = append(uses[PeerCapEmbeddings], "Embeddings")
	}
	if is(a.storedTranscribeConfig().Provider) {
		uses[PeerCapTranscribe] = append(uses[PeerCapTranscribe], "Audio Transcription")
	}
	if is(a.storedSpeechConfig().Provider) {
		uses[PeerCapSpeech] = append(uses[PeerCapSpeech], "Speech")
	}
	if is(a.storedString(SearchTable, "source")) {
		uses[PeerCapSearch] = append(uses[PeerCapSearch], "Web Search")
	}
	if is(a.storedString(BrowseTable, "source")) {
		uses[PeerCapBrowse] = append(uses[PeerCapBrowse], "page rendering")
	}
	if chosen := a.storedString(ImageTable, "provider"); chosen != "" && slices.Contains(p.ImageConnectors, chosen) {
		uses[PeerCapImages] = append(uses[PeerCapImages], "Image Generation, through "+chosen)
	}
	return uses
}

// authDB is the auth store, where the LLM tiers are kept, or nil.
func authDB() Database {
	if AuthDB == nil {
		return nil
	}
	return AuthDB()
}

// peerWrapper is a peer's line, read inside to outside: what it lends, then
// the instance and its key.
func peerWrapper(p RemotePeer) string {
	var gives []string
	for _, c := range peerCaps(p) {
		gives = append(gives, peerCapShortLabel(c))
	}
	inside := strings.Join(gives, ", ")
	if inside == "" {
		inside = "nothing yet"
	}
	host := p.BaseURL
	if u, err := url.Parse(p.BaseURL); err == nil && u.Host != "" {
		host = u.Host
	}
	if p.Instance != "" && p.Instance != host {
		host = p.Instance + " at " + host
	}
	return inside + " ⇐ another gohort, " + host + " (its peer key)"
}

// peerStatus says what here uses the peer, what it lends that nothing uses,
// and how its last check went.
func peerStatus(pc peerConnector) string {
	var used, unused []string
	for _, c := range peerCaps(pc.peer) {
		if u := pc.uses[c]; len(u) > 0 {
			used = append(used, strings.Join(u, " and "))
		} else {
			unused = append(unused, peerCapShortLabel(c))
		}
	}
	var parts []string
	if len(used) > 0 {
		parts = append(parts, "In use for "+strings.Join(used, "; ")+".")
	}
	if len(unused) > 0 {
		parts = append(parts, "Lent, not used here: "+strings.Join(unused, ", ")+".")
	}
	if e := strings.TrimSpace(pc.peer.LastError); e != "" {
		parts = append(parts, "Last check failed: "+e)
	} else if t, err := time.Parse(time.RFC3339, pc.peer.LastChecked); err == nil {
		parts = append(parts, "Checked "+ago(t)+".")
	}
	parts = append(parts, "Set up under Admin > Capabilities > Peers.")
	return strings.Join(parts, " ")
}

// peerEndpoints are where each capability the peer lends is served, with
// what goes there, what comes back, and what here uses it.
func peerEndpoints(pc peerConnector) []service.Endpoint {
	var out []service.Endpoint
	for _, c := range peerCaps(pc.peer) {
		e := service.Endpoint{Name: c, UsedBy: "lent, not used here"}
		if ep, ok := peerEndpoint[c]; ok {
			e.Address, e.Sends, e.Reads = ep.path, ep.sends, ep.reads
		}
		if u := pc.uses[c]; len(u) > 0 {
			e.UsedBy = strings.Join(u, "; ")
		}
		switch c {
		case PeerCapModels:
			var tiers []string
			for _, m := range pc.peer.Models {
				tiers = append(tiers, m.Tier+": "+m.Model+" on "+m.Provider)
			}
			if len(tiers) > 0 {
				e.Sends += " (" + strings.Join(tiers, "; ") + ")"
			}
		case PeerCapEmbeddings:
			if pc.peer.EmbedModel != "" {
				e.Reads += ", from " + pc.peer.EmbedModel
			}
		case PeerCapTranscribe:
			if pc.peer.TranscribeModel != "" {
				e.Reads += ", from " + pc.peer.TranscribeModel
			}
		case PeerCapSpeech:
			if pc.peer.SpeechModel != "" {
				e.Reads += ", from " + pc.peer.SpeechModel
			}
		case PeerCapSearch:
			if pc.peer.SearchProvider != "" {
				e.Reads += ", from " + pc.peer.SearchProvider
			}
		case PeerCapImages:
			if n := len(pc.peer.ImageConnectors); n > 0 {
				e.Reads += ", through " + strings.Join(pc.peer.ImageConnectors, ", ")
			}
		}
		out = append(out, e)
	}
	return out
}

// peerByName is the row named name (peer:<name>).
func (a *AdminApp) peerByName(name string) (peerConnector, bool) {
	for _, pc := range a.peerConnectors() {
		if pc.name == name {
			return pc, true
		}
	}
	return peerConnector{}, false
}

// peerTest is a peer row's Test: asks the peer again what it lends, which
// costs nothing, as Re-check under Peers does.
func peerTest(ctx context.Context, name string) map[string]any {
	p, err := RefreshRemotePeer(ctx, strings.TrimPrefix(name, peerRowPrefix))
	if err != nil {
		return map[string]any{"message": err.Error(), "warnings": []string{err.Error()}}
	}
	return map[string]any{"message": "Checked: " + p.Instance + " lends " + strings.Join(peerCapShortLabels(peerCaps(p)), ", ") + "."}
}
