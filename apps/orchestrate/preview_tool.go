package orchestrate

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// show_html — the chat-side viewer/previewer pane (the "Claude Desktop
// artifacts" shape, generalized). The LLM either authors a complete,
// self-contained HTML document (a dashboard, report, mockup) or points
// at a same-origin served page (a just-authored custom app); the client
// renders it in a slide-in pane to the right of the conversation, with a
// compact card in the transcript to reopen it. Emission is a generic
// {kind:"block", type:"html_artifact"} SSE event (renderer lives in
// core/ui's prelude — domain-agnostic); persistence is a UIBlock upsert
// on the session, replayed on reload.

// maxArtifactHTML caps one authored document. Roomy enough for a rich
// page with inline CSS/JS; small enough that a runaway generation can't
// balloon the session record or the SSE stream.
const maxArtifactHTML = 300 * 1024

// upsert_ui_block replaces the first persisted block whose ID matches
// blk (or that same_surface says is the same destination — e.g. a link
// card to the same URL, an artifact with the same title) and drops any
// later blocks matching either, so repeated emissions stay ONE card
// instead of stacking at the bottom of a replayed session. Appends when
// nothing matches. Caller holds the session lock.
func (s *ChatSession) upsert_ui_block(blk UIBlock, same_surface func(*UIBlock) bool) {
	out := s.UIBlocks[:0]
	placed := false
	for i := range s.UIBlocks {
		b := s.UIBlocks[i]
		if b.Type == blk.Type && (b.ID == blk.ID || (same_surface != nil && same_surface(&b))) {
			if placed {
				continue
			}
			b = blk
			placed = true
		}
		out = append(out, b)
	}
	if !placed {
		out = append(out, blk)
	}
	s.UIBlocks = out
}

// showHTMLToolDef builds the per-turn show_html tool. Framework display
// tool — always in the catalog (like compact_context): it has no side
// effects beyond the user's own screen and session record, so it isn't
// gated on AllowedTools, capabilities, or private mode.
func (t *chatTurn) showHTMLToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "show_html",
			Description: "Show the user a rendered HTML surface in a viewer pane beside the chat — a dashboard, report, diagram, front-end mockup, or a live preview of a gohort page. Two modes (pass exactly one): `html` = a COMPLETE, self-contained document you author, with ALL CSS and JavaScript inline (it renders sandboxed — no access to the app, its cookies, or external files); `url` = a same-origin path to preview a page this server already serves (e.g. a custom app you just created: \"/custom/<slug>/\"). Authored pages are offline snapshots by default; to make one LIVE, declare data_urls — the page then refreshes itself by calling the injected gohort.fetch(path) helper. Use this tool when the user asks for a dashboard/visualization/mockup, or to show an app/page you just built — NOT for ordinary answers, lists, or code; reply in text for those. To UPDATE an artifact you already showed, call again with the SAME id (returned by the first call) and the full revised content.",
			Parameters: map[string]ToolParam{
				"title": {
					Type:        "string",
					Description: "Short human title, shown on the pane header and the in-chat card.",
				},
				"html": {
					Type:        "string",
					Description: "Authored mode: the complete HTML document (doctype through </html>), fully self-contained — inline all CSS and JS, no external stylesheets/scripts/images. Keep it under ~300KB. Omit when passing url.",
				},
				"url": {
					Type:        "string",
					Description: "Preview mode: a same-origin path starting with \"/\" (e.g. \"/custom/myapp/\") to render that served page in the pane. External URLs are refused. Omit when passing html.",
				},
				"data_urls": {
					Type:        "array",
					Description: "Optional, html mode only: up to 8 same-origin GET paths (each starting with \"/\", e.g. \"/custom/myapp/data/metrics\") the page may fetch LIVE while the user views it. The viewer injects window.gohort.fetch(path) — returns a Promise of {ok, status, body} (body is the response text; JSON.parse it yourself). Combine with setInterval for an auto-refreshing dashboard. Paths NOT listed here are blocked, so declare everything the page needs up front.",
					Items:       &ToolParam{Type: "string"},
				},
				"id": {
					Type:        "string",
					Description: "Omit when showing a NEW artifact (an id is generated and returned). Pass a previously returned id to update that artifact in place instead of adding another.",
				},
			},
			Required: []string{"title"},
		},
		Handler: func(args map[string]any) (string, error) {
			title := strings.TrimSpace(stringArg(args, "title"))
			html := stringArg(args, "html")
			url := strings.TrimSpace(stringArg(args, "url"))
			hasHTML := strings.TrimSpace(html) != ""
			if hasHTML == (url != "") {
				return "", fmt.Errorf("pass exactly ONE of html (an authored document) or url (a same-origin path to preview)")
			}
			if hasHTML && len(html) > maxArtifactHTML {
				return "", fmt.Errorf("html too large (%d bytes; cap %d) — trim the document (inline data tables are the usual culprit) and call again", len(html), maxArtifactHTML)
			}
			// The url mode renders WITHOUT a sandbox (it's this server's own
			// page), so it must never point off-origin: require a single
			// leading "/" — no scheme, no protocol-relative "//host". The
			// client-side pane enforces the same rule.
			if url != "" && (!strings.HasPrefix(url, "/") || strings.HasPrefix(url, "//")) {
				return "", fmt.Errorf("url must be a same-origin path starting with \"/\" (got %q) — external pages can't be previewed; to show external content, author an html document instead", url)
			}
			// Live-data allowlist (html mode). Same same-origin-path rule as
			// url mode — the pane proxies these WITH the user's cookies, so
			// off-origin or scheme-carrying entries are refused outright.
			dataURLs := stringSliceFromArgs(args, "data_urls")
			if len(dataURLs) > 0 && !hasHTML {
				return "", fmt.Errorf("data_urls applies to html mode only — a url preview is already live")
			}
			if len(dataURLs) > 8 {
				return "", fmt.Errorf("data_urls is capped at 8 paths (got %d) — consolidate your data endpoints", len(dataURLs))
			}
			for _, u := range dataURLs {
				if !strings.HasPrefix(u, "/") || strings.HasPrefix(u, "//") {
					return "", fmt.Errorf("data_urls entry %q must be a same-origin path starting with \"/\"", u)
				}
			}
			if title == "" {
				title = "Artifact"
			}
			// Same-surface rule: a url preview matches on the url; an
			// authored document matches on the title. Both feed the
			// dedupe below AND id adoption — the model routinely forgets
			// to pass the id back on an update, and without adoption
			// each revision minted a fresh block, stacking duplicate
			// cards (each carrying a full HTML copy) on the session.
			same_surface := func(b *UIBlock) bool {
				if url != "" {
					return b.URL == url
				}
				return b.URL == "" && strings.EqualFold(strings.TrimSpace(b.Title), title)
			}
			id := strings.TrimSpace(stringArg(args, "id"))
			isUpdate := id != ""
			if id == "" && t.session != nil {
				t.toolMu.Lock()
				for i := range t.session.UIBlocks {
					b := &t.session.UIBlocks[i]
					if b.Type == "html_artifact" && same_surface(b) {
						id = b.ID
						isUpdate = true
						break
					}
				}
				t.toolMu.Unlock()
			}
			if id == "" {
				id = "artifact-" + UUIDv4()[:8]
			}
			// Live emission: open:true pops the pane now. The persisted
			// copy below carries no open hint, so a session reload shows
			// the card without hijacking the screen.
			payload := map[string]any{
				"kind":  "block",
				"type":  "html_artifact",
				"id":    id,
				"title": title,
				"open":  true,
			}
			if hasHTML {
				payload["html"] = html
				if len(dataURLs) > 0 {
					payload["data_urls"] = dataURLs
				}
			} else {
				payload["url"] = url
			}
			t.sse.Send(payload)
			if t.session != nil {
				blk := UIBlock{Type: "html_artifact", ID: id, Title: title, URL: url}
				if hasHTML {
					blk.HTML = html
					blk.DataURLs = dataURLs
				}
				t.toolMu.Lock()
				t.session.upsert_ui_block(blk, same_surface)
				t.toolMu.Unlock()
			}
			// The document already lives on the session artifact; don't
			// let the activity wrapper persist a second full copy in the
			// tool-call record (args are recorded AFTER the handler runs).
			if hasHTML {
				args["html"] = fmt.Sprintf("(%d-byte HTML document — stored as session artifact %q)", len(html), id)
			}
			if isUpdate {
				return fmt.Sprintf("Artifact %q (id %q) updated in place — the user's pane refreshed.", title, id), nil
			}
			return fmt.Sprintf("Artifact %q is now showing beside the chat (id %q). Call show_html again with this id to update it in place.", title, id), nil
		},
	}
}

// show_link — a clickable navigation card in the transcript. Prose like
// "go to /custom/myapp/" leaves the user copy-pasting a path; this drops
// a card with a real Open button instead. Same framework-display-tool
// posture as show_html: no side effects beyond the user's screen and
// session record, so it's always in the catalog, ungated. Emits a
// generic {kind:"block", type:"link_hint"} SSE event (renderer in
// core/ui's prelude) and persists as a UIBlock so it replays on reload.
func (t *chatTurn) showLinkToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "show_link",
			Description: "Drop a clickable link card into the chat pointing the user at a page. Use it WHENEVER your reply tells the user to go somewhere or open something — an app you just created or updated (app_def returns its url), a settings/admin page, or an external site they must visit (e.g. to create an API key). A bare path in prose is not clickable; this card gives them an Open button. Pass a same-origin path starting with \"/\" (e.g. \"/custom/myapp/\") or a full http(s):// URL. NOT for rendering content — show_html displays a page or dashboard beside the chat; show_link only offers navigation.",
			Parameters: map[string]ToolParam{
				"url": {
					Type:        "string",
					Description: "Where the link goes: a same-origin path starting with \"/\" (e.g. \"/custom/myapp/\") or a full http(s):// URL. No other schemes.",
				},
				"title": {
					Type:        "string",
					Description: "Short human label for the destination, shown on the card (e.g. \"Hacker News dashboard\"). Name the PLACE, not the action — the card supplies its own Open button.",
				},
				"note": {
					Type:        "string",
					Description: "Optional one-line hint under the title: what the user will find or should do there (e.g. \"Paste the API key into the token field\"). Omit when the title says it all.",
				},
			},
			Required: []string{"url", "title"},
		},
		Handler: func(args map[string]any) (string, error) {
			url := strings.TrimSpace(stringArg(args, "url"))
			title := strings.TrimSpace(stringArg(args, "title"))
			note := strings.TrimSpace(stringArg(args, "note"))
			// Same trust rule as show_html's url mode plus plain http(s):
			// a single-leading-slash path (same-origin) or an explicit
			// http/https URL. Everything else (javascript:, data:,
			// protocol-relative "//host") is refused.
			sameOrigin := strings.HasPrefix(url, "/") && !strings.HasPrefix(url, "//")
			external := strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
			if !sameOrigin && !external {
				return "", fmt.Errorf("url must be a same-origin path starting with \"/\" or a full http(s):// URL (got %q)", url)
			}
			if title == "" {
				title = url
			}
			// One card per destination: agents re-announce the same page
			// across turns (an app after every update, a settings page on
			// every mention), and appending each time stacked duplicate
			// link cards on the replayed session. Reusing the existing
			// block's id makes the live card retitle in place (addBlock
			// routes a repeated id into onUpdate) and the upsert below
			// keeps the persisted record at one card, sweeping any
			// duplicates the old append-always path left behind.
			same_surface := func(b *UIBlock) bool { return b.URL == url }
			id := ""
			if t.session != nil {
				t.toolMu.Lock()
				for i := range t.session.UIBlocks {
					b := &t.session.UIBlocks[i]
					if b.Type == "link_hint" && same_surface(b) {
						id = b.ID
						break
					}
				}
				t.toolMu.Unlock()
			}
			isUpdate := id != ""
			if id == "" {
				id = "link-" + UUIDv4()[:8]
			}
			payload := map[string]any{
				"kind":  "block",
				"type":  "link_hint",
				"id":    id,
				"title": title,
				"url":   url,
				// Always present (even empty): a repeated id routes into the
				// renderer's onUpdate, which only overwrites fields that are
				// non-null — an omitted text would leave a stale note behind.
				"text": note,
			}
			t.sse.Send(payload)
			if t.session != nil {
				t.toolMu.Lock()
				t.session.upsert_ui_block(UIBlock{Type: "link_hint", ID: id, Title: title, URL: url, Text: note}, same_surface)
				t.toolMu.Unlock()
			}
			if isUpdate {
				return fmt.Sprintf("Link card %q → %s refreshed in place (one card per destination). Don't repeat the raw URL in your reply — the card carries it.", title, url), nil
			}
			return fmt.Sprintf("Link card %q → %s is now showing in the chat. Don't repeat the raw URL in your reply — the card carries it.", title, url), nil
		},
	}
}
