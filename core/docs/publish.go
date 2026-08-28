// Publish destinations — the OUTBOUND third of the document registries.
//
// ReferenceSource pulls knowledge from another service INTO a document.
// DocumentTarget pushes a section from one gohort app into another's document.
// PublishDestination pushes a FINISHED document OUT to an external system — a
// Confluence space, a wiki, a webhook — and hands back where it landed.
//
// Same discipline as its two siblings: a destination binds its own credential +
// configuration at registration, resolves everything per user in its own
// subsystem, and the producing app (guides today; techwriter later) only ever
// talks to this registry. A writer app never learns what Confluence is.
//
// The registry is also the SAFETY boundary for agent-driven publishing. A
// publisher agent may choose a destination and a target, but only from IDs this
// registry handed it — Publish rejects a target that isn't in the destination's
// own Targets list, so an outbound write can't land somewhere the model
// composed from memory.

package docs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// PublishTarget is one place inside a destination a document can land — a
// Confluence space, a parent page, a folder. Group is an optional heading a
// picker can bucket by; Desc is a one-line hint.
type PublishTarget struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Group string `json:"group,omitempty"`
	Desc  string `json:"desc,omitempty"`
}

// PublishDoc is the document being published, rendered every way a destination
// might want it. A destination picks the representation it can carry: Confluence
// converts Markdown to storage format, a webhook may post Markdown or HTML.
type PublishDoc struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Markdown string `json:"markdown"`
	HTML     string `json:"html,omitempty"` // self-contained rendering, when the producer has one
	// SourceKind/SourceID identify the gohort document this came from ("guide",
	// the guide id) — carried so a destination can record provenance and a
	// producer can match a PublishRecord back to what it published.
	SourceKind string `json:"source_kind,omitempty"`
	SourceID   string `json:"source_id,omitempty"`
}

// PublishRequest is one publish call. An empty ExternalID creates; a non-empty
// one UPDATES that existing remote document (Version carries the last version
// the producer saw, which Confluence needs to bump).
type PublishRequest struct {
	Target     string     `json:"target"`
	Title      string     `json:"title"` // may differ from Doc.Title (the remote page's name)
	Doc        PublishDoc `json:"-"`
	ExternalID string     `json:"external_id,omitempty"`
	Version    int        `json:"version,omitempty"`
}

// PublishResult is where a document landed. ExternalID + Version are what a
// producer stores to make the NEXT publish an update rather than a duplicate.
type PublishResult struct {
	ExternalID string `json:"external_id,omitempty"`
	URL        string `json:"url,omitempty"`
	Version    int    `json:"version,omitempty"`
	// Label is a human "where it went" line for the UI ("ENG · Getting Started").
	Label string `json:"label,omitempty"`
	// Updated reports whether this replaced an existing remote document.
	Updated bool `json:"updated,omitempty"`
}

// PublishDestination is implemented by an outbound integration. Like
// DocumentTarget it binds its own configuration at registration and resolves
// per user — the producer never sees a credential or a base URL.
type PublishDestination interface {
	// Kind is a stable identifier ("confluence") used to route a publish.
	Kind() string
	// Label is the human name ("Confluence").
	Label() string
	// Available reports whether this user can publish here right now. The
	// string is the reason when they can't ("no Confluence credential is
	// configured"), shown to the user verbatim — so a missing credential reads
	// as an explanation rather than a destination that silently isn't there.
	Available(user string) (bool, string)
	// Targets lists the places a document can land (spaces, parents). An empty
	// list with a nil error means the destination takes no target.
	Targets(ctx context.Context, user string) ([]PublishTarget, error)
	// Publish writes the document. It enforces the user's access itself.
	Publish(ctx context.Context, user string, req PublishRequest) (PublishResult, error)
}

var (
	publishMu    sync.RWMutex
	publishDests = map[string]PublishDestination{}
)

// RegisterPublishDestination registers an outbound destination. Re-registering
// the same Kind replaces it. Call once where the destination's config is live.
func RegisterPublishDestination(d PublishDestination) {
	if d == nil || d.Kind() == "" {
		return
	}
	publishMu.Lock()
	publishDests[d.Kind()] = d
	publishMu.Unlock()
}

func lookupDest(kind string) PublishDestination {
	publishMu.RLock()
	defer publishMu.RUnlock()
	return publishDests[strings.TrimSpace(kind)]
}

// PublishDestinationInfo is one registered destination as seen by a picker or a
// publisher agent, including whether this user can actually use it.
type PublishDestinationInfo struct {
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"` // why not, when Available is false
}

// PublishDestinations returns every registered destination with this user's
// availability resolved, sorted by label. Unavailable ones are INCLUDED with
// their reason: "Confluence needs a credential" is more useful to a user than
// Confluence quietly not being on the list.
func PublishDestinations(user string) []PublishDestinationInfo {
	publishMu.RLock()
	dests := make([]PublishDestination, 0, len(publishDests))
	for _, d := range publishDests {
		dests = append(dests, d)
	}
	publishMu.RUnlock()

	out := make([]PublishDestinationInfo, 0, len(dests))
	for _, d := range dests {
		ok, reason := d.Available(user)
		out = append(out, PublishDestinationInfo{Kind: d.Kind(), Label: d.Label(), Available: ok, Reason: reason})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// HasPublishDestinations reports whether any destination is registered at all —
// what a producer checks before showing a Publish control.
func HasPublishDestinations() bool {
	publishMu.RLock()
	defer publishMu.RUnlock()
	return len(publishDests) > 0
}

// PublishTargets lists a destination's targets for one user.
func PublishTargets(ctx context.Context, user, kind string) ([]PublishTarget, error) {
	d := lookupDest(kind)
	if d == nil {
		return nil, fmt.Errorf("no publish destination registered for %q", kind)
	}
	if ok, reason := d.Available(user); !ok {
		return nil, fmt.Errorf("%s is not available: %s", d.Label(), reason)
	}
	return d.Targets(ctx, user)
}

// PublishDocument publishes to a destination, after checking the destination is
// available AND that req.Target is one the destination actually offers.
//
// The target check is the grounding gate: a publisher agent picks from what
// Targets returned, and anything else — a space key the model recalled, a page
// id from another site — is refused here rather than becoming an outbound write
// to the wrong place. Destinations that offer no targets (a fixed webhook) skip
// the check, since there's nothing to pick wrong.
func PublishDocument(ctx context.Context, user, kind string, req PublishRequest) (PublishResult, error) {
	d := lookupDest(kind)
	if d == nil {
		return PublishResult{}, fmt.Errorf("no publish destination registered for %q", kind)
	}
	if ok, reason := d.Available(user); !ok {
		return PublishResult{}, fmt.Errorf("%s is not available: %s", d.Label(), reason)
	}
	req.Target = strings.TrimSpace(req.Target)
	targets, err := d.Targets(ctx, user)
	if err != nil {
		return PublishResult{}, fmt.Errorf("could not list %s targets: %w", d.Label(), err)
	}
	if len(targets) > 0 {
		found := false
		for _, t := range targets {
			if t.ID == req.Target {
				found = true
				break
			}
		}
		if !found {
			return PublishResult{}, fmt.Errorf("%q is not one of the %d place(s) available in %s — list them and pick one of those ids", req.Target, len(targets), d.Label())
		}
	}
	if strings.TrimSpace(req.Title) == "" {
		req.Title = req.Doc.Title
	}
	if strings.TrimSpace(req.Title) == "" {
		return PublishResult{}, fmt.Errorf("a title is required")
	}
	return d.Publish(ctx, user, req)
}

// PublishRecord is where a document was last published, stored by the PRODUCER
// alongside the document itself. Its whole job is to make the second publish an
// UPDATE rather than a duplicate: ExternalID + Version are what a destination
// needs to replace a remote document in place.
//
// It lives here rather than in each writer app because every app that publishes
// needs exactly this, and because the publisher tools read and write it without
// knowing whose document it belongs to.
type PublishRecord struct {
	Kind        string `json:"kind"`                   // destination kind ("confluence")
	Target      string `json:"target,omitempty"`       // the target id it landed in
	TargetTitle string `json:"target_title,omitempty"` // human name of that target
	Title       string `json:"title,omitempty"`        // the remote document's title
	ExternalID  string `json:"external_id,omitempty"`
	URL         string `json:"url,omitempty"`
	Version     int    `json:"version,omitempty"`
	At          string `json:"at,omitempty"` // RFC3339 of the last publish
}

// FindPublishRecord returns the record for a destination kind, and whether one
// exists. Matching is by kind alone: one document has one home per destination,
// which is what makes "publish again" mean "update the page I made last time".
func FindPublishRecord(records []PublishRecord, kind string) (PublishRecord, bool) {
	kind = strings.TrimSpace(kind)
	for _, r := range records {
		if r.Kind == kind {
			return r, true
		}
	}
	return PublishRecord{}, false
}

// UpsertPublishRecord replaces the record for rec.Kind, or appends it.
func UpsertPublishRecord(records []PublishRecord, rec PublishRecord) []PublishRecord {
	for i := range records {
		if records[i].Kind == rec.Kind {
			records[i] = rec
			return records
		}
	}
	return append(records, rec)
}
