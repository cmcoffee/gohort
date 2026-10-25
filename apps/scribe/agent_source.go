// A guide as a Source an agent can be attached to: it reads the guide,
// searches it, and reports what it learns back into it.
//
// The use this was built for is a living troubleshooting guide. An agent
// working diagnostic dumps looks things up in the guide as it goes, and what it
// learns comes back as findings. The guide is then published out (Confluence)
// for the platforms that are not gohort.
//
// The agent does not write sections. It reports a finding, which names this
// guide as the one it was attached to, and the Guide Curator places it on its
// next pass (findings.go, curator.go). An agent sees one incident at a time; the
// curator sees the guide and the batch, so three reports of one failure become
// one section rather than three, and every change it makes is in the digest
// where it can be read and undone.
//
// The report tool declares CapWrite, so an agent gets it only when its
// attachment has "Can make changes" switched on in the Sources picker; an
// agent attached read-only reads the guide and nothing more.
//
// The ITEM is one of the person's own guides. A guide someone else shared is
// not offered: a finding is filed into the reporter's own inbox, and the
// curator edits the reporter's own guides.

package scribe

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
)

type guideSource struct{ app *Scribe }

func (s guideSource) Kind() string  { return "guide" }
func (s guideSource) Label() string { return "Guides" }

func (s guideSource) HasItems(user string) bool {
	udb := s.udb(user)
	return udb != nil && len(listGuides(udb)) > 0
}

// udb is user's guide store, or nil when user cannot use Scribe: the app is
// switched off, or not one of theirs. Then their agents have no guide
// tools, rather than tools that reach a store the app's own pages would not.
func (s guideSource) udb(user string) Database {
	if s.app == nil || s.app.DB == nil || strings.TrimSpace(user) == "" || !scribeReachable(user, s.app.WebPath()) {
		return nil
	}
	return UserDB(s.app.DB, user)
}

// scribeReachable reports whether user may use the app at path: it is
// switched on, and theirs to use (an admin's, every app). The same answer the
// request gate gives, for a caller with a user and no request.
func scribeReachable(user, path string) bool {
	if !AppEnabledHere(path) {
		return false
	}
	if AuthDB == nil {
		return true
	}
	db := AuthDB()
	if db == nil || !AuthHasUsers(db) {
		return true
	}
	u, ok := AuthGetUser(db, user)
	if !ok {
		return false
	}
	if u.Admin {
		return true
	}
	for _, p := range AuthResolveUserApps(db, u) {
		if p == path || strings.HasPrefix(p, path+"/") {
			return true
		}
	}
	return false
}

func (s guideSource) List(user string) []ReferenceItem {
	udb := s.udb(user)
	if udb == nil {
		return nil
	}
	var out []ReferenceItem
	for _, g := range listGuides(udb) {
		desc := fmt.Sprintf("%d section%s", len(g.Sections), plural(len(g.Sections)))
		if sub := strings.TrimSpace(g.Subtitle); sub != "" {
			desc = sub + " - " + desc
		}
		out = append(out, ReferenceItem{ID: g.ID, Name: firstNonEmpty(g.Title, "Untitled guide"), Desc: desc})
	}
	return out
}

// guide loads one of user's own guides. Checked on every call, not only when
// the list is built: an agent can carry an attachment to a guide since
// deleted, and an item id is a string anyone can supply.
func (s guideSource) guide(user, id string) (Guide, bool) {
	udb := s.udb(user)
	if udb == nil {
		return Guide{}, false
	}
	return loadGuide(udb, id)
}

// Fetch is the generic pull: the guide's contents, or the sections that match
// query.
func (s guideSource) Fetch(ctx context.Context, user, itemID, query string) string {
	g, ok := s.guide(user, itemID)
	if !ok {
		return ""
	}
	if strings.TrimSpace(query) == "" {
		return guideContents(g)
	}
	return guideSearch(g, query)
}

func (s guideSource) ItemTools(user, itemID string) []AgentToolDef {
	return s.ItemToolsWithSession(nil, user, itemID)
}

// ItemToolsWithSession builds the tools for one guide. Reporting needs an
// agent: a finding records which agent and which conversation it came from.
// A turn with no agent (a guide's own Author pulling a sibling guide) gets
// the two read tools only. With no session at all the caller is asking what
// the guide offers (the Sources picker, which shows "Can make changes" only
// for a source with a tool that writes), so the report tool is listed, and it
// refuses if it is ever called that way.
func (s guideSource) ItemToolsWithSession(sess *ToolSession, user, itemID string) []AgentToolDef {
	g, ok := s.guide(user, itemID)
	if !ok {
		return nil
	}
	title := firstNonEmpty(g.Title, "Untitled guide")
	slug := RefToolSlug(title)
	if slug == "" {
		slug = "guide_" + RefToolSlug(itemID)
	}
	id := g.ID
	tools := []AgentToolDef{
		{
			Tool: Tool{
				Name: "read_" + slug + "_guide",
				Description: fmt.Sprintf("Read the guide %q. With no section, it lists the guide's sections with the first line of each; with a section, it returns that section in full. ", title) +
					"Look here before working something out from scratch: it is where what has already been learned is written down.",
				Parameters: map[string]ToolParam{
					"section": {Type: "string", Description: "The section's title, as the list shows it. Omit to list the sections."},
				},
				Caps: []Capability{CapRead},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				g, ok := s.guide(user, id)
				if !ok {
					return "", fmt.Errorf("the guide %q is no longer there", title)
				}
				s.app.checkPublishedSoon(user, g)
				want := strings.TrimSpace(str(args, "section"))
				if want == "" {
					return guideContents(g), nil
				}
				sec, err := findSection(g, want)
				if err != nil {
					return "", err
				}
				return "## " + sec.Title + "\n\n" + clipSection(sec.Markdown), nil
			},
		},
		{
			Tool: Tool{
				Name:        "search_" + slug + "_guide",
				Description: fmt.Sprintf("Search the guide %q for the sections that mention something: an error message, a component, a symptom. Returns the best matching sections, ranked, each in full or as the passage around the match.", title),
				Parameters: map[string]ToolParam{
					"query": {Type: "string", Description: "Words to look for, e.g. an error string or a component name."},
				},
				Required: []string{"query"},
				Caps:     []Capability{CapRead},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				g, ok := s.guide(user, id)
				if !ok {
					return "", fmt.Errorf("the guide %q is no longer there", title)
				}
				s.app.checkPublishedSoon(user, g)
				return guideSearch(g, str(args, "query")), nil
			},
		},
	}
	var agentID, sessionID string
	if sess != nil {
		if strings.TrimSpace(sess.AgentID) == "" {
			return tools
		}
		agentID, sessionID = sess.AgentID, sess.ChatSessionID
	}
	return append(tools, AgentToolDef{
		Tool: Tool{
			Name: "report_to_" + slug + "_guide",
			Description: fmt.Sprintf("Report something you learned that belongs in the guide %q: a cause and its fix, what a symptom means, where to look, a value or a step that differs from what the guide says. ", title) +
				"It is filed for the Guide Curator, which weaves it into the guide on its next pass, so it does not appear at once. " +
				"Report what you established, not what you did: \"ran the check\" is not a finding, \"a timeout here means the license server is unreachable\" is. One finding per thing learned, and do not report what the guide already says.",
			Parameters: map[string]ToolParam{
				"topic":      {Type: "string", Description: "One line: what this is about, e.g. \"License server timeouts in the boot log\". Not a section title: the curator decides where it goes."},
				"finding":    {Type: "string", Description: "What you learned, in markdown, complete enough to act on without this conversation: the symptom, what it means, what to do."},
				"confidence": {Type: "string", Enum: []string{"verified", "probable", "single-observation"}, Description: "verified: checked directly, more than once or from more than one angle. probable: consistent with what you saw, not separately confirmed. single-observation: seen once."},
			},
			Required: []string{"topic", "finding"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if agentID == "" {
				return "", fmt.Errorf("a finding is reported by an agent attached to the guide, and this call has none")
			}
			content := strings.TrimSpace(str(args, "finding"))
			if content == "" {
				return "", fmt.Errorf("a finding needs its content: what you learned")
			}
			if _, ok := s.guide(user, id); !ok {
				return "", fmt.Errorf("the guide %q is no longer there", title)
			}
			fid, err := SubmitFinding(user, "guide", DocFinding{
				Content:    content,
				Topic:      strings.TrimSpace(str(args, "topic")),
				Confidence: str(args, "confidence"),
				Document:   id,
				Origin: DocFindingOrigin{
					SourceKind: "agent",
					ItemID:     agentID,
					ItemLabel:  s.agentName(user, agentID),
					RunID:      sessionID,
					Observed:   time.Now().UTC().Format(time.RFC3339),
				},
			})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Filed (finding %s) for %q. The Guide Curator places findings in batches, so it will appear in the guide after its next pass.", fid, title), nil
		},
	})
}

// agentName is the reporting agent's name for the digest, or its id.
func (s guideSource) agentName(user, agentID string) string {
	if orch := agenthost.Get(); orch != nil {
		if a, ok := orch.LookupAppAgent(user, agentID); ok && strings.TrimSpace(a.Name) != "" {
			return a.Name
		}
	}
	return agentID
}

// guideContents is a guide's table of contents: each section with its first
// line, so a reader can tell which to open.
func guideContents(g Guide) string {
	var b strings.Builder
	b.WriteString("# " + firstNonEmpty(g.Title, "Untitled guide") + "\n")
	if sub := strings.TrimSpace(g.Subtitle); sub != "" {
		b.WriteString(sub + "\n")
	}
	secs := g.sorted()
	if len(secs) == 0 {
		b.WriteString("\nIt has no sections yet.")
		return b.String()
	}
	fmt.Fprintf(&b, "\n%d section%s:\n", len(secs), plural(len(secs)))
	for i, sec := range secs {
		line := firstLine(sec.Markdown, 120)
		if line != "" {
			line = " - " + line
		}
		fmt.Fprintf(&b, "%d. %s%s\n", i+1, firstNonEmpty(sec.Title, "(untitled)"), line)
	}
	return b.String()
}

// findSection matches a section by title: exactly (ignoring case), else the
// one title that contains what was asked.
func findSection(g Guide, want string) (Section, error) {
	w := strings.ToLower(strings.TrimSpace(want))
	var near []Section
	for _, sec := range g.sorted() {
		t := strings.ToLower(strings.TrimSpace(sec.Title))
		if t == w {
			return sec, nil
		}
		if strings.Contains(t, w) {
			near = append(near, sec)
		}
	}
	if len(near) == 1 {
		return near[0], nil
	}
	return Section{}, fmt.Errorf("no section titled %q; read the guide with no section to see its titles", want)
}

// guideSectionMax bounds one section in a tool result.
const guideSectionMax = 12000

func clipSection(md string) string {
	if len([]rune(md)) <= guideSectionMax {
		return md
	}
	return string([]rune(md)[:guideSectionMax]) + "\n\n(cut here: the section is longer than one read returns)"
}

// guideSearch ranks the guide's sections by how often they mention the
// query's words, a title match counting more, and returns the best few.
func guideSearch(g Guide, query string) string {
	var terms []string
	for _, w := range strings.Fields(strings.ToLower(query)) {
		w = strings.Trim(w, ".,;:!?\"'()[]{}")
		if len([]rune(w)) >= 3 {
			terms = append(terms, w)
		}
	}
	if len(terms) == 0 {
		return "Give a word or phrase of three letters or more to search for."
	}
	type hit struct {
		sec   Section
		score int
		at    int // where the first match is in the body, -1 for none
	}
	var hits []hit
	for _, sec := range g.sorted() {
		title, body := strings.ToLower(sec.Title), strings.ToLower(sec.Markdown)
		h := hit{sec: sec, at: -1}
		for _, t := range terms {
			h.score += 3*strings.Count(title, t) + strings.Count(body, t)
			if i := strings.Index(body, t); i >= 0 && (h.at < 0 || i < h.at) {
				h.at = i
			}
		}
		if h.score > 0 {
			hits = append(hits, h)
		}
	}
	name := firstNonEmpty(g.Title, "the guide")
	if len(hits) == 0 {
		return fmt.Sprintf("No section of %q mentions %q.", name, query)
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > 5 {
		hits = hits[:5]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d section%s of %q match %q:\n", len(hits), plural(len(hits)), name, query)
	for _, h := range hits {
		b.WriteString("\n## " + firstNonEmpty(h.sec.Title, "(untitled)") + "\n\n")
		b.WriteString(passage(h.sec.Markdown, h.at))
		b.WriteString("\n")
	}
	return b.String()
}

// passage is a short section whole, or a long one's text around at.
func passage(md string, at int) string {
	const whole, around = 1500, 700
	if len(md) <= whole || at < 0 {
		if len(md) > whole {
			return md[:whole] + " ..."
		}
		return md
	}
	start := at - around/2
	if start < 0 {
		start = 0
	}
	end := start + around
	if end > len(md) {
		end = len(md)
	}
	// Keep to whole UTF-8 characters at both ends.
	for start > 0 && !utf8Start(md[start]) {
		start--
	}
	for end < len(md) && !utf8Start(md[end]) {
		end++
	}
	out := md[start:end]
	if start > 0 {
		out = "... " + out
	}
	if end < len(md) {
		out += " ..."
	}
	return out + "\n(an excerpt: read the section for all of it)"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// firstLine is md's first non-heading line of text, cut to max runes.
func firstLine(md string, max int) string {
	for _, l := range strings.Split(md, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#>-*"))
		if l == "" {
			continue
		}
		if r := []rune(l); len(r) > max {
			return string(r[:max]) + "..."
		}
		return l
	}
	return ""
}
