// Article-kind support: the co-author kit for a single-body document, the
// house conventions the Guide Author writes articles under, the header-image
// generator, and the HTML importer. Everything here is what TechWriter did
// that a sectioned guide has no place for.
package scribe

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// articleModePrompt is appended to the Guide Author's prompt for a turn on an
// article. It carries the writing conventions TechWriter's users relied on:
// commands kept verbatim in code blocks, warnings on their own line, short
// sentences, citations preserved.
const articleModePrompt = "\n\nARTICLE MODE — the open document is an ARTICLE (one markdown body under a title), so the section tools are absent and read_article / write_article / draft_article stand in for them. " +
	"Write it as documentation for people who need to do something: numbered steps for procedures, bullets for lists, fenced code blocks for every command, with a line before each command saying what it does and what to expect. " +
	"Warnings, prerequisites and notes go on their own line as a blockquote (> **WARNING:** …), never buried in a paragraph. " +
	"Keep sentences short — one idea each; break anything past about 120 characters. " +
	"Use ## headings for the main parts and ### for sub-parts; do not write a table of contents, the page has none. " +
	"Preserve every citation ([1], [2], a ## Sources section) exactly as it appears. " +
	"Before revising, read_article; when writing, send the complete body — the viewer shows what you write, not what you say."

// articleTools swaps the guide kit's section tools for the article kit, keeping
// every tool that is not about sections (research, knowledge, references,
// per-source tools). Built from the guide kit rather than from scratch so the
// shared tools stay defined in one place. udb + user resolve the open document
// exactly as the section tools do: the active marker is per-user, the document
// may live in another owner's store when shared.
func (T *Scribe) articleTools(udb Database, user string, guideKit []AgentToolDef) []AgentToolDef {
	sectionTools := map[string]bool{
		"add_section": true, "edit_section": true, "draft_section": true, "list_sections": true,
		"delete_section": true, "rename_section": true, "move_section": true,
	}
	var out []AgentToolDef
	for _, t := range guideKit {
		if sectionTools[t.Tool.Name] {
			continue
		}
		out = append(out, t)
	}
	openGuide := func() (Guide, Database, string, bool) {
		id := activeGuideID(udb)
		if id == "" {
			return Guide{}, nil, "", false
		}
		g, owner, oudb, ok := resolveGuide(T.DB, udb, user, id)
		return g, oudb, owner, ok
	}

	readArticle := AgentToolDef{
		Tool: Tool{
			Name:        "read_article",
			Description: "Return the OPEN article's current markdown body. Call this before any revision so you work from what is there now — the user may have edited it directly since you last saw it. No arguments.",
			Parameters:  map[string]ToolParam{},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			g, _, _, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no article is open — ask the user to select or create one first")
			}
			body := strings.TrimSpace(g.body())
			if body == "" {
				return fmt.Sprintf("The article %q is empty.", g.Title), nil
			}
			return fmt.Sprintf("Article %q:\n\n%s", g.Title, body), nil
		},
	}

	writeArticle := AgentToolDef{
		Tool: Tool{
			Name:        "write_article",
			Description: "Replace the OPEN article's whole body with new markdown. Send the COMPLETE article every time — this is a replacement, not an append — keeping every command, fact and citation the user gave unless asked to change it. The viewer updates and the previous body is kept in History.",
			Parameters: map[string]ToolParam{
				"markdown": {Type: "string", Description: "The full article body as markdown (## headings, fenced code, lists). No top-level # title — the title is separate."},
			},
			Required: []string{"markdown"},
		},
		SingleFirePerBatch: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			md := strings.TrimSpace(fmt.Sprint(args["markdown"]))
			if md == "" {
				return "", fmt.Errorf("markdown is required — pass the article body")
			}
			g, ownerUDB, _, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no article is open — ask the user to select or create one first")
			}
			if cleaned, changed := sanitizeGuideArtifacts(md); changed {
				md = cleaned
			}
			g.setBody(md)
			saveGuideRev(ownerUDB, g, "Rewrote article")
			return fmt.Sprintf("Wrote the body of %q (%d words).", g.Title, len(strings.Fields(md))), nil
		},
	}

	draftArticle := AgentToolDef{
		Tool: Tool{
			Name:        "draft_article",
			Description: "Write the OPEN article GROUNDED in its own backing. Deterministically gathers material from the article's knowledge collections AND every attached Source on the topic, then writes the whole body from that material and commits it — you do not gather first, it does. Give it a brief of what the article should cover. Errors if nothing attached has anything on the topic — then use `research` (web) or write it yourself with write_article.",
			Parameters: map[string]ToolParam{
				"instructions": {Type: "string", Description: "What the article should cover: the angle, scope, audience, and any specifics to include."},
			},
			Required: []string{"instructions"},
		},
		SingleFirePerBatch: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			instr := strings.TrimSpace(fmt.Sprint(args["instructions"]))
			if instr == "" {
				return "", fmt.Errorf("instructions are required")
			}
			g, ownerUDB, ownerUser, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no article is open — ask the user to select or create one first")
			}
			grounding, found := gatherGroundingFor(context.Background(), ownerUser, g, g.Title+" — "+instr)
			if !found {
				return "", fmt.Errorf("no grounding found in this article's knowledge collections or attached Sources — attach a Source/collection with anything on this topic, use the `research` tool for a public/web topic, or write it yourself with write_article")
			}
			sys := fmt.Sprintf("You are the Guide Author writing an ARTICLE titled %q. Write the whole body as clean markdown — ## headings, numbered steps for procedures, fenced code for every command, warnings as their own blockquote line. Do NOT write the title as a heading. Ground every specific (commands, values, names, versions, paths) STRICTLY in the provided material; do not invent anything it doesn't contain. If the material is thin, write only what it supports. Output ONLY the markdown body, nothing else.", g.Title) + "\n" + BannedWordsRule
			userMsg := fmt.Sprintf("What to cover:\n%s\n\nGrounding material gathered from this article's knowledge collections and attached Sources — write from THIS and nothing else:\n\n%s", instr, grounding)
			// A Private article stays off the wire, this completion included.
			chat := T.LeadChat
			if g.Private {
				chat = T.WorkerChat
			}
			resp, err := chat(context.Background(), []Message{{Role: "user", Content: userMsg}}, WithSystemPrompt(sys), WithTemperature(0.3), WithThink(false))
			if err != nil {
				return "", fmt.Errorf("draft failed: %w", err)
			}
			md := strings.TrimSpace(resp.Content)
			if md == "" {
				return "", fmt.Errorf("the draft came back empty")
			}
			if cleaned, changed := sanitizeGuideArtifacts(md); changed {
				md = cleaned
			}
			g.setBody(md)
			saveGuideRev(ownerUDB, g, "Drafted from sources")
			return fmt.Sprintf("Drafted %q from its knowledge + attached Sources (%d words).", g.Title, len(strings.Fields(md))), nil
		},
	}
	return append([]AgentToolDef{readArticle, writeArticle, draftArticle}, out...)
}

// generateHeaderImage produces a banner for an article from its title through
// the deployment's image generator and returns something a browser can show:
// the generator's remote URL, or a data URL when it wrote a local file. The
// prompt asks for no rendered text — image models still misspell typography —
// so the title is a subject descriptor; the page shows the title itself.
func generateHeaderImage(ctx context.Context, title string) (string, error) {
	prompt := `A wide banner-style header image evoking the topic: "` + strings.TrimSpace(title) + `". ` +
		"Visual style: clean, professional, editorial / magazine quality, suitable as a top-of-article banner. " +
		"Wide aspect ratio (around 3:1). Do NOT render any text, words, letters, captions, or typography in the image — purely visual."
	result, err := GenerateImageLandscape(ctx, "", prompt)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(result.URL, "http://") || strings.HasPrefix(result.URL, "https://") {
		return result.URL, nil
	}
	data, err := os.ReadFile(result.URL)
	os.Remove(result.URL)
	if err != nil {
		return "", fmt.Errorf("could not read the generated image: %w", err)
	}
	mime := "image/png"
	if strings.HasSuffix(result.URL, ".jpg") || strings.HasSuffix(result.URL, ".jpeg") {
		mime = "image/jpeg"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

var (
	htmlTitleRe = regexp.MustCompile(`(?is)<title>([^<]+)</title>`)
	htmlH1Re    = regexp.MustCompile(`(?is)<h1[^>]*>([^<]+)</h1>`)
	htmlMetaRe  = regexp.MustCompile(`(?is)<div class="meta">.*?</div>`)
	htmlHeadRe  = regexp.MustCompile(`(?is)<header class="guide-doc-head">.*?</header>`)
	htmlBrandRe = regexp.MustCompile(`(?is)<div class="guide-brand">.*?</div>`)
	htmlFootRe  = regexp.MustCompile(`(?is)<footer class="guide-foot">.*?</footer>`)
)

// articleFromHTML recovers a title and a markdown body from an exported page —
// one of Scribe's own, or TechWriter's — so an article can be brought back in.
// The title comes from <title> (then <h1>); the body is what lies between the
// <body> tags with the title block, meta line, brand and footer stripped, then
// converted back to markdown.
func articleFromHTML(html string) (title, body string) {
	if m := htmlTitleRe.FindStringSubmatch(html); m != nil {
		title = strings.TrimSpace(HTMLUnescape(m[1]))
	} else if m := htmlH1Re.FindStringSubmatch(html); m != nil {
		title = strings.TrimSpace(HTMLUnescape(m[1]))
	}
	body = html
	if i := strings.Index(strings.ToLower(body), "<body"); i >= 0 {
		if end := strings.Index(body[i:], ">"); end >= 0 {
			body = body[i+end+1:]
		}
	}
	if i := strings.Index(strings.ToLower(body), "</body>"); i >= 0 {
		body = body[:i]
	}
	body = htmlHeadRe.ReplaceAllString(body, "")
	body = htmlH1Re.ReplaceAllString(body, "")
	body = htmlMetaRe.ReplaceAllString(body, "")
	body = htmlBrandRe.ReplaceAllString(body, "")
	body = htmlFootRe.ReplaceAllString(body, "")
	body = strings.TrimSpace(HTMLToMarkdown(body))
	return title, body
}
