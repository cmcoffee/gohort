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
	if server := d.viaMCP(); server != "" {
		return mcpUsable(user, server)
	}
	return credentialUsable(user, d.app.config().ConfluenceCredential)
}

// viaMCP is the MCP server Confluence publishes through, or "" for the REST
// API. An MCP server is used only when no API credential is named: the
// credential is the direct path, and the two are never mixed in one publish.
func (d *confluenceDest) viaMCP() string {
	cfg := d.app.config()
	if strings.TrimSpace(cfg.ConfluenceCredential) != "" {
		return ""
	}
	return strings.TrimSpace(cfg.ConfluenceMCP)
}

// confluenceMCPTargetID is the one target Confluence offers through an MCP
// server. The spaces cannot be listed up front without a run of their own, so
// the space is a question on the form instead.
const confluenceMCPTargetID = "confluence"

func confluenceMCPTarget(server string) docs.PublishTarget {
	return docs.PublishTarget{ID: confluenceMCPTargetID, Title: "Confluence", Desc: "through " + server, Group: "Confluence"}
}

// confluenceMCPFields are the questions a publish through an MCP server asks.
var confluenceMCPFields = []docs.PublishField{
	{Name: "space", Label: "Space", Required: true, Help: "The space it goes in: its key, like DOCS, or its name."},
	{Name: "parent", Label: "Under page", Help: "The title or link of the page to put it under. Empty puts it at the top of the space."},
}

// TargetSpecs describes the MCP route as a target with its questions. The REST
// route lists its spaces through Targets instead and has no questions.
func (d *confluenceDest) TargetSpecs(ctx context.Context, user string) []docs.PublishTargetSpec {
	server := d.viaMCP()
	if server == "" {
		return nil
	}
	return []docs.PublishTargetSpec{{Kind: ConfluenceKind, Target: confluenceMCPTarget(server), Fields: confluenceMCPFields}}
}

// publishViaMCP publishes through the MCP server's tools: a run that holds only
// that server's tools, minus any that delete, remove or archive, follows the
// instruction and reports the page's address (docs.PublishViaCredential).
func (d *confluenceDest) publishViaMCP(ctx context.Context, user, server string, req docs.PublishRequest) (docs.PublishResult, error) {
	if strings.TrimSpace(req.Doc.Markdown) == "" {
		return docs.PublishResult{}, fmt.Errorf("the document is empty: there's nothing to publish")
	}
	prev := strings.TrimSpace(req.ExternalID)
	if strings.TrimSpace(req.Answers["space"]) == "" && prev == "" {
		return docs.PublishResult{}, fmt.Errorf("say which Confluence space it goes in")
	}
	said, url, err := docs.PublishViaCredential(ctx, user, docs.MCPIntegrationPrefix+server, confluenceMCPInstruction(req))
	if err != nil {
		return docs.PublishResult{}, err
	}
	Log("[publish.confluence] user=%q published %q through MCP server %q", user, req.Title, server)
	label := chFirst(strings.TrimSpace(req.Title), strings.TrimSpace(req.Doc.Title))
	if note := clipToLabel(said); note != "" && label == "" {
		label = note
	}
	// The address rides back as the ExternalID, so a republish hands it to the
	// next run as the page to update.
	return docs.PublishResult{URL: url, ExternalID: chFirst(url, prev), Label: label, Updated: prev != ""}, nil
}

// confluenceMCPInstruction is what the publishing run is handed: where the page
// goes, its title, whether it updates an earlier one, then the document.
func confluenceMCPInstruction(req docs.PublishRequest) string {
	title := chFirst(strings.TrimSpace(req.Title), strings.TrimSpace(req.Doc.Title))
	space := strings.TrimSpace(req.Answers["space"])
	parent := strings.TrimSpace(req.Answers["parent"])
	prev := strings.TrimSpace(req.ExternalID)
	var b strings.Builder
	b.WriteString("Publish the document below to Confluence as a page.\n\n")
	b.WriteString("Title: " + title + "\n")
	if space != "" {
		b.WriteString("Space: " + space + " (a space key or name: find the space it names)\n")
	}
	if parent != "" {
		b.WriteString("Under page: " + parent + " (find that page in the space and put the new page under it)\n")
	}
	if prev != "" {
		b.WriteString("\nThis document was published here before, as " + prev + ". Update that page's title and body rather than creating a new page.\n")
	} else {
		b.WriteString("\nCreate a new page with this title and the document as its body.\n")
	}
	b.WriteString("Keep the document's headings, lists, tables, links and code blocks, in the body format the tool expects. If the space or the page to put it under cannot be found, say so and do not put the page anywhere else.\n\n---\n\n")
	b.WriteString(req.Doc.Markdown)
	return b.String()
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
	if server := d.viaMCP(); server != "" {
		return []docs.PublishTarget{confluenceMCPTarget(server)}, nil
	}
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
			desc += ", showing the first " + strconv.Itoa(len(resp.Results)) + " spaces; this site has more"
		}
		out = append(out, docs.PublishTarget{ID: s.ID, Title: s.Name, Desc: desc, Group: "Spaces"})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the credential reached Confluence but no spaces came back: check that its allowed endpoints include /wiki/api/v2/**")
	}
	return out, nil
}

// Publish creates a page in the target space, or updates an existing one when
// the request carries its ExternalID. An update has to send version.number+1;
// the current version is re-read rather than trusted from the caller's record,
// so a page edited in Confluence since the last publish still updates cleanly
// instead of failing on a stale version.
func (d *confluenceDest) Publish(ctx context.Context, user string, req docs.PublishRequest) (docs.PublishResult, error) {
	if server := d.viaMCP(); server != "" {
		return d.publishViaMCP(ctx, user, server, req)
	}
	cfg := d.app.config()
	storage := MarkdownToConfluence(req.Doc.Markdown)
	if strings.TrimSpace(storage) == "" {
		return docs.PublishResult{}, fmt.Errorf("the document is empty: there's nothing to publish")
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
		return docs.PublishResult{}, fmt.Errorf("could not read the existing page %s (it may have been deleted in Confluence, publish it as a new page instead): %w", id, err)
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
