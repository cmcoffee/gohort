// The Confluence publish destination: a guide becomes a Confluence page, and
// publishing it again UPDATES that page instead of making a second one.
//
// Everything Confluence-shaped is contained here — the v2 REST paths, the
// storage-format body, the version number an update has to carry. The writer
// app publishing through this knows none of it.
package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

// ConfluenceKind is the destination kind a producer routes to.
const ConfluenceKind = "confluence"

// confluenceSpaceLimit caps the spaces listed in one call. Pagination isn't
// followed: a site with more spaces than this reports the cap in the target
// list rather than silently showing a truncated set as if it were all of them.
const confluenceSpaceLimit = 100

type confluenceDest struct{ app *PublishApp }

func (d *confluenceDest) Kind() string  { return ConfluenceKind }
func (d *confluenceDest) Label() string { return "Confluence" }

func (d *confluenceDest) Available(user string) (bool, string) {
	return credentialUsable(user, d.app.config().ConfluenceCredential)
}

// siteURL returns the Confluence site root with any trailing /wiki removed, so
// apiURL and link building can each add what they need. Prefers the configured
// override, else the credential's own BaseURL.
func (d *confluenceDest) siteURL(user string) string {
	cfg := d.app.config()
	base := strings.TrimSpace(cfg.ConfluenceBaseURL)
	if base == "" {
		if s := Secure(); s != nil {
			if c, ok := s.Resolve(cfg.ConfluenceCredential, user); ok {
				base = strings.TrimSpace(c.BaseURL)
			}
		}
	}
	base = strings.TrimRight(base, "/")
	return strings.TrimSuffix(base, "/wiki")
}

func (d *confluenceDest) apiURL(user, path string) string {
	return d.siteURL(user) + "/wiki/api/v2" + path
}

// Targets lists the site's spaces — the "where" a page lands. Space IDs are
// numeric in the v2 API (the KEY is shown as the description, because that's
// what a person recognizes).
func (d *confluenceDest) Targets(ctx context.Context, user string) ([]docs.PublishTarget, error) {
	cfg := d.app.config()
	body, err := callAPI(user, cfg.ConfluenceCredential, "GET",
		d.apiURL(user, "/spaces?limit="+strconv.Itoa(confluenceSpaceLimit)), "")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Results []struct {
			ID   string `json:"id"`
			Key  string `json:"key"`
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"results"`
		Links struct {
			Next string `json:"next"`
		} `json:"_links"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("could not read the space list from Confluence: %w", err)
	}
	out := make([]docs.PublishTarget, 0, len(resp.Results))
	for _, s := range resp.Results {
		desc := s.Key
		// The cap is stated on the last row rather than logged, so whoever is
		// picking a space can see the list is partial instead of concluding a
		// missing space doesn't exist.
		if resp.Links.Next != "" && len(out) == len(resp.Results)-1 {
			desc += " — showing the first " + strconv.Itoa(len(resp.Results)) + " spaces; this site has more"
		}
		out = append(out, docs.PublishTarget{ID: s.ID, Title: s.Name, Desc: desc, Group: "Spaces"})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the credential reached Confluence but no spaces came back — check that its allowed endpoints include /wiki/api/v2/**")
	}
	return out, nil
}

// Publish creates a page in the target space, or updates an existing one when
// the request carries its ExternalID. An update has to send version.number+1;
// the current version is re-read rather than trusted from the caller's record,
// so a page edited in Confluence since the last publish still updates cleanly
// instead of failing on a stale version.
func (d *confluenceDest) Publish(ctx context.Context, user string, req docs.PublishRequest) (docs.PublishResult, error) {
	cfg := d.app.config()
	storage := MarkdownToConfluence(req.Doc.Markdown)
	if strings.TrimSpace(storage) == "" {
		return docs.PublishResult{}, fmt.Errorf("the document is empty — there's nothing to publish")
	}

	if strings.TrimSpace(req.ExternalID) != "" {
		return d.update(user, cfg, req, storage)
	}

	payload, err := json.Marshal(map[string]any{
		"spaceId": req.Target,
		"status":  "current",
		"title":   req.Title,
		"body":    map[string]any{"representation": "storage", "value": storage},
	})
	if err != nil {
		return docs.PublishResult{}, err
	}
	body, err := callAPI(user, cfg.ConfluenceCredential, "POST", d.apiURL(user, "/pages"), string(payload))
	if err != nil {
		return docs.PublishResult{}, err
	}
	return d.result(user, body, false)
}

func (d *confluenceDest) update(user string, cfg PublishConfig, req docs.PublishRequest, storage string) (docs.PublishResult, error) {
	id := strings.TrimSpace(req.ExternalID)
	version := req.Version
	if cur, err := d.currentVersion(user, cfg, id); err == nil && cur > 0 {
		version = cur
	} else if err != nil {
		// The page is gone (someone deleted it in Confluence) — say so plainly,
		// because the useful next move is publishing it as a new page, not
		// retrying the update.
		return docs.PublishResult{}, fmt.Errorf("could not read the existing page %s (it may have been deleted in Confluence — publish it as a new page instead): %w", id, err)
	}
	payload, err := json.Marshal(map[string]any{
		"id":     id,
		"status": "current",
		"title":  req.Title,
		"body":   map[string]any{"representation": "storage", "value": storage},
		"version": map[string]any{
			"number":  version + 1,
			"message": "Updated from gohort",
		},
	})
	if err != nil {
		return docs.PublishResult{}, err
	}
	body, err := callAPI(user, cfg.ConfluenceCredential, "PUT", d.apiURL(user, "/pages/"+id), string(payload))
	if err != nil {
		return docs.PublishResult{}, err
	}
	return d.result(user, body, true)
}

func (d *confluenceDest) currentVersion(user string, cfg PublishConfig, id string) (int, error) {
	body, err := callAPI(user, cfg.ConfluenceCredential, "GET", d.apiURL(user, "/pages/"+id), "")
	if err != nil {
		return 0, err
	}
	var page struct {
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return 0, err
	}
	return page.Version.Number, nil
}

// result reads a page response into a publish result, building the page's web
// link from the response's own _links when Confluence supplies them.
func (d *confluenceDest) result(user string, body []byte, updated bool) (docs.PublishResult, error) {
	var page struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
		Links struct {
			Base  string `json:"base"`
			WebUI string `json:"webui"`
		} `json:"_links"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return docs.PublishResult{}, fmt.Errorf("Confluence accepted the page but its response could not be read: %w", err)
	}
	if strings.TrimSpace(page.ID) == "" {
		return docs.PublishResult{}, fmt.Errorf("Confluence accepted the request but returned no page id")
	}
	base := strings.TrimRight(page.Links.Base, "/")
	if base == "" {
		base = d.siteURL(user) + "/wiki"
	}
	url := ""
	if page.Links.WebUI != "" {
		url = base + page.Links.WebUI
	}
	return docs.PublishResult{
		ExternalID: page.ID,
		URL:        url,
		Version:    page.Version.Number,
		Label:      page.Title,
		Updated:    updated,
	}, nil
}
