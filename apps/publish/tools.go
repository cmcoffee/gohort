// The Publisher agent's tool kit, built by whichever app owns the document.
//
// A writer app calls BuildPublishTools with a resolver for "the document the
// user has open" and a way to persist where it landed; everything else — the
// registry calls, the update-vs-create decision, the phrasing the agent reads —
// lives here, so a second writer app wires publishing in a dozen lines.
package publish

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

// Document is the thing being published, resolved fresh for each tool call by
// the host app — the same closure discipline the guides co-author tools use, so
// the agent always publishes what the document says NOW rather than a snapshot
// taken when the turn started.
type Document struct {
	Doc docs.PublishDoc
	// Records is where this document has already been published.
	Records []docs.PublishRecord
	// Save persists a publish result against the document. The host app owns
	// storage (and, in guides' case, records a revision), so this is a callback
	// rather than a store handle.
	Save func(docs.PublishRecord) error
}

// BuildPublishTools returns the Publisher agent's tools for one chat turn.
// open resolves the open document; returning false means nothing is open, which
// every tool reports as such rather than publishing something unexpected.
//
// ctx is the TURN's context so a Stop actually cancels an in-flight publish,
// matching the app-tools contract elsewhere.
func BuildPublishTools(ctx context.Context, user string, open func() (Document, bool)) []AgentToolDef {
	listDestinations := AgentToolDef{
		Tool: Tool{
			Name: "list_publish_destinations",
			Description: "List the configured publish destinations, whether this user can publish to each one right now (and if not, why), and where the open document has ALREADY been published. Call this first. " +
				"A destination that reports available=false cannot be used — pass its reason on to the user as their next step.",
			Parameters: map[string]ToolParam{},
		},
		Handler: func(args map[string]any) (string, error) {
			doc, ok := open()
			if !ok {
				return "", fmt.Errorf("no document is open to publish")
			}
			dests := docs.PublishDestinations(user)
			if len(dests) == 0 {
				return "", fmt.Errorf("no publish destinations are registered on this deployment — an admin configures them in Admin > Publishing")
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Document: %q\n\nDestinations:\n", doc.Doc.Title)
			for _, d := range dests {
				if d.Available {
					fmt.Fprintf(&b, "- %s (destination id: %s) — available\n", d.Label, d.Kind)
				} else {
					fmt.Fprintf(&b, "- %s (destination id: %s) — NOT available: %s\n", d.Label, d.Kind, d.Reason)
				}
			}
			if len(doc.Records) == 0 {
				b.WriteString("\nThis document has not been published anywhere yet.\n")
				return b.String(), nil
			}
			b.WriteString("\nAlready published:\n")
			for _, r := range doc.Records {
				where := r.TargetTitle
				if where == "" {
					where = r.Target
				}
				fmt.Fprintf(&b, "- %s: %q", r.Kind, r.Title)
				if where != "" {
					fmt.Fprintf(&b, " in %s", where)
				}
				if r.Version > 0 {
					fmt.Fprintf(&b, " (version %d)", r.Version)
				}
				if r.At != "" {
					fmt.Fprintf(&b, ", last published %s", r.At)
				}
				if r.URL != "" {
					fmt.Fprintf(&b, " — %s", r.URL)
				}
				b.WriteString("\n  Publishing here again with update_existing=true REPLACES that page rather than making a second one.\n")
			}
			return b.String(), nil
		},
	}

	listTargets := AgentToolDef{
		Tool: Tool{
			Name: "list_publish_targets",
			Description: "List the places inside a destination a document can land — Confluence spaces, for example. The ids returned here are the ONLY values publish_document accepts as its target: pick one of them, never a space key or id from memory. " +
				"An empty list means the destination takes no target (a webhook posts to one fixed endpoint) — publish without one.",
			Parameters: map[string]ToolParam{
				"destination": {Type: "string", Description: "The destination id from list_publish_destinations (e.g. \"confluence\")."},
			},
			Required: []string{"destination"},
		},
		Handler: func(args map[string]any) (string, error) {
			kind := strings.TrimSpace(fmt.Sprint(args["destination"]))
			targets, err := docs.PublishTargets(ctx, user, kind)
			if err != nil {
				return "", err
			}
			if len(targets) == 0 {
				return fmt.Sprintf("%s takes no target — publish to it without one.", kind), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d place(s) in %s. Pass one of these ids as target:\n", len(targets), kind)
			for _, t := range targets {
				fmt.Fprintf(&b, "- id: %s — %s", t.ID, t.Title)
				if t.Desc != "" {
					fmt.Fprintf(&b, " (%s)", t.Desc)
				}
				b.WriteString("\n")
			}
			return b.String(), nil
		},
	}

	publishDoc := AgentToolDef{
		Tool: Tool{
			Name: "publish_document",
			Description: "Publish the open document to a destination and return the link. " +
				"Set update_existing=true to REPLACE the page this document was published to before (list_publish_destinations shows whether there is one); leave it false to create a new one. " +
				"The target must be an id from list_publish_targets for this destination — anything else is refused.",
			Parameters: map[string]ToolParam{
				"destination":     {Type: "string", Description: "The destination id from list_publish_destinations."},
				"target":          {Type: "string", Description: "The target id from list_publish_targets (e.g. a Confluence space id). Omit only for a destination that lists no targets."},
				"title":           {Type: "string", Description: "The name the document gets in the destination. Defaults to the document's own title."},
				"update_existing": {Type: "boolean", Description: "Replace the previously published page instead of creating a new one. Only meaningful when this document has been published to this destination before."},
			},
			Required: []string{"destination"},
		},
		// One publish per round: two calls in a batch is how a document ends up
		// on a wiki twice.
		SingleFirePerBatch: true,
		Handler: func(args map[string]any) (string, error) {
			doc, ok := open()
			if !ok {
				return "", fmt.Errorf("no document is open to publish")
			}
			if strings.TrimSpace(doc.Doc.Markdown) == "" {
				return "", fmt.Errorf("the document is empty — there is nothing to publish yet")
			}
			kind := strings.TrimSpace(fmt.Sprint(args["destination"]))
			req := docs.PublishRequest{
				Target: strings.TrimSpace(fmt.Sprint(args["target"])),
				Title:  strings.TrimSpace(fmt.Sprint(args["title"])),
				Doc:    doc.Doc,
			}
			if req.Target == "<nil>" {
				req.Target = ""
			}
			if req.Title == "" || req.Title == "<nil>" {
				req.Title = doc.Doc.Title
			}
			// An update only happens when the model asked for one AND a record
			// actually exists — asking to update a document that was never
			// published would otherwise send an empty external id downstream.
			prev, hadPrev := docs.FindPublishRecord(doc.Records, kind)
			if truthy(args["update_existing"]) && hadPrev {
				req.ExternalID = prev.ExternalID
				req.Version = prev.Version
			}

			res, err := docs.PublishDocument(ctx, user, kind, req)
			if err != nil {
				return "", err
			}

			rec := docs.PublishRecord{
				Kind:        kind,
				Target:      req.Target,
				TargetTitle: targetTitle(ctx, user, kind, req.Target),
				Title:       req.Title,
				ExternalID:  res.ExternalID,
				URL:         res.URL,
				Version:     res.Version,
				At:          time.Now().UTC().Format(time.RFC3339),
			}
			if doc.Save != nil {
				if err := doc.Save(rec); err != nil {
					// The remote write succeeded; only the bookkeeping failed.
					// Say both, because the page IS there and a retry would
					// create a second one.
					return fmt.Sprintf("Published to %s — %s. NOTE: the link could not be recorded against the document (%v), so publishing again will create a new page rather than updating this one.", kind, linkOrLabel(res), err), nil
				}
			}
			verb := "Published"
			if res.Updated {
				verb = "Updated"
			}
			return fmt.Sprintf("%s %q in %s — %s", verb, req.Title, kind, linkOrLabel(res)), nil
		},
	}

	return []AgentToolDef{listDestinations, listTargets, publishDoc}
}

// targetTitle resolves a target id back to its human name for the stored
// record, so the UI can say "Engineering" rather than "98304". A failure here
// is not worth failing a successful publish over — the id stands in.
func targetTitle(ctx context.Context, user, kind, target string) string {
	if strings.TrimSpace(target) == "" {
		return ""
	}
	targets, err := docs.PublishTargets(ctx, user, kind)
	if err != nil {
		return target
	}
	for _, t := range targets {
		if t.ID == target {
			return t.Title
		}
	}
	return target
}

func linkOrLabel(res docs.PublishResult) string {
	if res.URL != "" {
		return res.URL
	}
	if res.Label != "" {
		return res.Label
	}
	return "the destination accepted it but returned no link"
}

// truthy reads a boolean tool argument that may arrive as a bool or as a string
// ("true"), which is the shape some models emit.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "true" || s == "yes" || s == "1"
	}
	return false
}
