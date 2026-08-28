// The generic webhook destination — the escape hatch that makes "and other
// destinations" true today rather than after the next integration is written.
//
// Anything that accepts an HTTP post of a document is reachable: an internal
// wiki's API, a docs pipeline, a static-site build hook. It takes no target
// (there is exactly one URL), so the Publisher agent asks only for a title.
package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

// WebhookKind is the destination kind a producer routes to.
const WebhookKind = "webhook"

type webhookDest struct{ app *PublishApp }

func (d *webhookDest) Kind() string { return WebhookKind }

// Label uses the admin's own name for it when they set one — a destination
// called "Team wiki" is a great deal more informative in a Publish dialog than
// one called "Webhook".
func (d *webhookDest) Label() string {
	if l := strings.TrimSpace(d.app.config().WebhookLabel); l != "" {
		return l
	}
	return "Webhook"
}

func (d *webhookDest) Available(user string) (bool, string) {
	cfg := d.app.config()
	if strings.TrimSpace(cfg.WebhookURL) == "" {
		return false, "no webhook URL is configured — an admin sets one in Admin > Publishing"
	}
	return credentialUsable(user, cfg.WebhookCredential)
}

// Targets returns nothing: a webhook is a single endpoint, so there is no
// "where" to choose. An empty list is the registry's signal that this
// destination takes no target.
func (d *webhookDest) Targets(ctx context.Context, user string) ([]docs.PublishTarget, error) {
	return nil, nil
}

func (d *webhookDest) Publish(ctx context.Context, user string, req docs.PublishRequest) (docs.PublishResult, error) {
	cfg := d.app.config()
	body, contentType, err := webhookBody(cfg, req)
	if err != nil {
		return docs.PublishResult{}, err
	}
	// Content type rides on the dispatch call, so a receiver expecting
	// text/markdown isn't handed a JSON header.
	s := Secure()
	if s == nil {
		return docs.PublishResult{}, fmt.Errorf("the credential store is not initialized")
	}
	out, err := s.DispatchToolCallCT(&ToolSession{Username: user}, cfg.WebhookCredential, cfg.WebhookURL, "POST", body, contentType)
	if err != nil {
		return docs.PublishResult{}, err
	}
	status, payload := splitHTTPEnvelope(out)
	if status == 0 {
		return docs.PublishResult{}, fmt.Errorf("unexpected response from %s: %s", cfg.WebhookURL, firstLine(out))
	}
	if status >= 400 {
		return docs.PublishResult{}, fmt.Errorf("%s returned HTTP %d: %s", cfg.WebhookURL, status, trimForError(payload))
	}
	// A webhook may or may not hand back an id/url. Take them when they're
	// there so a second publish can be an update; otherwise every publish is a
	// fresh post, which is the honest behavior for an endpoint that told us
	// nothing about what it created.
	var resp struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	_ = json.Unmarshal([]byte(payload), &resp)
	return docs.PublishResult{
		ExternalID: resp.ID,
		URL:        resp.URL,
		Label:      req.Title,
		Updated:    strings.TrimSpace(req.ExternalID) != "",
	}, nil
}

// webhookBody renders the document in the configured format and reports the
// content type that goes with it.
func webhookBody(cfg PublishConfig, req docs.PublishRequest) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.WebhookFormat)) {
	case "markdown", "md":
		return req.Doc.Markdown, "text/markdown; charset=utf-8", nil
	case "html":
		if strings.TrimSpace(req.Doc.HTML) == "" {
			return "", "", fmt.Errorf("this document has no HTML rendering to post")
		}
		return req.Doc.HTML, "text/html; charset=utf-8", nil
	default: // json
		payload, err := json.Marshal(map[string]any{
			"title":       req.Title,
			"subtitle":    req.Doc.Subtitle,
			"markdown":    req.Doc.Markdown,
			"html":        req.Doc.HTML,
			"source_kind": req.Doc.SourceKind,
			"source_id":   req.Doc.SourceID,
			"external_id": req.ExternalID,
		})
		if err != nil {
			return "", "", err
		}
		return string(payload), "application/json", nil
	}
}
