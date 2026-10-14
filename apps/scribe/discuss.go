// Discussing a section, or the guide's writing brief, with the Guide Author
// before it is written.
//
// A section's Discuss opens the shared assist window in its discuss mode: the
// Author asks what the section is for, proposes what it will write, and
// writes only once the person agrees. Nothing reaches the guide until they
// use the draft, which saves through the same path as the section editor,
// with a revision.
//
// It is the guide's own Author, not a bare model call: it follows the
// writing brief and the user's Rules, and it can look things up (the guide's
// other sections, its knowledge, its Sources, and web research unless the
// guide is Private) instead of asking the person what it could have found.
// Its tools that change the guide are withheld: the text comes back in the
// reply, and the person decides whether it goes in.
//
// The conversation is the Author's own sub-session, one per open window, so
// it remembers what was said without the browser resending the history.
//
// The brief is discussed the same way from the Edit guide dialog: it is the
// text that is hardest to write in one go, because it is an account of what
// the guide is for. Its draft goes back into the dialog's box, saved with the
// dialog like any other setting.

package scribe

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/textutil"

	"github.com/cmcoffee/gohort/apps/orchestrate"
)

// discussTurn is one message of a discussion about a section or the brief.
type discussTurn struct {
	guide   Guide
	brief   bool    // the guide's writing brief, not a section
	section Section // zero ID for a section not written yet
	title   string  // the section's title, or the working title of a new one
	message string
	draft   string // the text on the left, as the person may have edited it
	first   bool
	context string // the user's Rules and the guide's brief
}

// prompt is what the Author is sent. The first turn carries the whole
// framing; later ones carry the current text, since the person can edit it
// between turns, and their message.
func (d discussTurn) prompt() string {
	var b strings.Builder
	what := "section"
	if d.brief {
		what = "brief"
	}
	if d.first && d.brief {
		fmt.Fprintf(&b, "You are helping the author of the guide %q write its WRITING BRIEF, before anything is written.\n\n", firstNonEmpty(d.guide.Title, "Untitled guide"))
		if d.context != "" {
			b.WriteString(d.context + "\n\n")
		}
		fmt.Fprintf(&b, "The brief is a standing instruction to everything that writes this guide: the Guide Author when the author edits it, and the Guide Curator, which files findings agents report into it. It says what the guide is for, who reads it, how each section is written, what to leave out, and how a new finding that repeats something already covered is folded in. When you write it, write the brief only, as plain instructions to those writers, with no heading, in at most %d characters.\n\n", guideBriefMax)
		var titles []string
		for _, s := range d.guide.sorted() {
			titles = append(titles, s.Title)
		}
		if len(titles) > 0 {
			fmt.Fprintf(&b, "The guide's sections so far: %s. Read them when it helps to know what it already holds.\n\n", strings.Join(titles, "; "))
		}
		b.WriteString("You can look things up with your tools rather than asking for what you can find. You cannot change the guide yourself: the brief you write goes back to the author, who decides whether to keep it.\n\n")
		b.WriteString("## How to reply\n\n" + textutil.DiscussReplyContract + "\n")
	} else if d.first {
		fmt.Fprintf(&b, "You are discussing one section of the guide %q with its author, before anything is written.\n\n", firstNonEmpty(d.guide.Title, "Untitled guide"))
		if d.context != "" {
			b.WriteString(d.context + "\n\n")
		}
		if d.section.ID == "" {
			fmt.Fprintf(&b, "The section is a NEW one, not written yet; its working title is %q. When you write it, write its body only, with no heading.\n\n", d.title)
		} else {
			fmt.Fprintf(&b, "The section is %q. When you write it, write its body only, with no heading.\n\n", d.title)
		}
		var others []string
		for _, s := range d.guide.sorted() {
			if s.ID != d.section.ID {
				others = append(others, s.Title)
			}
		}
		if len(others) > 0 {
			fmt.Fprintf(&b, "The guide's other sections, which this one should not repeat: %s.\n\n", strings.Join(others, "; "))
		}
		b.WriteString("You can look things up with your tools (list_sections, the guide's knowledge and Sources, research) rather than asking for what you can find. You cannot change the guide yourself: the text you write goes back to the author, who decides whether it goes in.\n\n")
		b.WriteString("## How to reply\n\n" + textutil.DiscussReplyContract + "\n")
	}
	if strings.TrimSpace(d.draft) == "" {
		fmt.Fprintf(&b, "## The %s's current text\n\n(empty: nothing written yet)\n\n", what)
	} else {
		f := DocFence(d.draft)
		fmt.Fprintf(&b, "## The %s's current text\n\n%s\n%s\n%s\n\n", what, f, d.draft, f)
	}
	b.WriteString("## The author says\n\n" + d.message)
	return b.String()
}

// discussSessionRe keeps the browser's conversation id to a safe key.
var discussSessionRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// handleSectionDiscuss runs one turn of a section discussion: POST
// section/discuss?guide=<id>[&section=<sid>] with {title, message, draft,
// history, session}. Returns {reply, value}; value is the section's text when
// the Author wrote it this turn, empty otherwise.
func (T *Scribe) handleSectionDiscuss(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	guideID := strings.TrimSpace(r.URL.Query().Get("guide"))
	g, _, _, canEdit, found := T.resolve(r, udb, user, guideID)
	if !found {
		http.NotFound(w, r)
		return
	}
	if !canEdit {
		http.Error(w, "You need edit access to this guide to work on its sections.", http.StatusForbidden)
		return
	}
	var body discussBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		http.Error(w, "say something to discuss", http.StatusBadRequest)
		return
	}
	turn := discussTurn{guide: g, title: strings.TrimSpace(body.Title), message: strings.TrimSpace(body.Message),
		draft: body.Draft, first: len(body.History) == 0, context: T.authorBackgroundContext(udb, user, g.ID)}
	if sid := strings.TrimSpace(r.URL.Query().Get("section")); sid != "" {
		for _, s := range g.Sections {
			if s.ID == sid {
				turn.section = s
			}
		}
		if turn.section.ID == "" {
			http.Error(w, "that section is no longer in the guide", http.StatusNotFound)
			return
		}
		turn.title = turn.section.Title
	}
	if turn.title == "" {
		turn.title = "Untitled section"
	}
	T.runDiscussTurn(w, r, udb, user, turn, body.Session)
}

// discussBody is what the discuss window sends each turn.
type discussBody struct {
	Title   string           `json:"title"`
	Message string           `json:"message"`
	Draft   string           `json:"draft"`
	History []map[string]any `json:"history"`
	Session string           `json:"session"`
}

// handleBriefDiscuss runs one turn of a discussion about the guide's writing
// brief: POST brief/discuss?id=<guide> with {message, draft, history,
// session}. The owner's, like the rest of the guide's settings. Returns
// {reply, value}; value is the brief when the Author wrote it this turn.
func (T *Scribe) handleBriefDiscuss(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	g, _, canManage, _, found := T.resolve(r, udb, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		http.NotFound(w, r)
		return
	}
	if !canManage {
		http.Error(w, "Only the owner can change this guide's brief.", http.StatusForbidden)
		return
	}
	var body discussBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		http.Error(w, "say something to discuss", http.StatusBadRequest)
		return
	}
	// The user's Rules only: the guide's current brief is the text being
	// discussed, and handing it over as an instruction too would have the
	// Author following the very thing it is helping rewrite.
	T.runDiscussTurn(w, r, udb, user, discussTurn{guide: g, brief: true, title: "Writing brief",
		message: strings.TrimSpace(body.Message), draft: body.Draft, first: len(body.History) == 0,
		context: strings.TrimSpace(DocRulesSection(udb, rulesNamespace))}, body.Session)
}

// runDiscussTurn sends a turn to the guide's Author and answers {reply,
// value}.
func (T *Scribe) runDiscussTurn(w http.ResponseWriter, r *http.Request, udb Database, user string, turn discussTurn, sessionKey string) {
	g := turn.guide
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "orchestrate not initialized", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	tools := discussTools(T.coauthorTools(coauthorScope{Ctx: ctx, UDB: udb, Orch: orch, User: user, CanEdit: true, Guide: g.ID}), g.Private)
	if g.Private {
		ctx = WithNetworkConnector(ctx, NewNetworkConnector(true))
	}
	session := discussSessionRe.ReplaceAllString(sessionKey, "")
	if session == "" {
		session = "default"
	}
	res, err := orch.RunAgentSyncContinuingRich(ctx, orchestrate.AgentSyncRun{
		AgentOwner:   user,
		RuntimeUser:  user,
		AgentKey:     guideAgentID,
		SubSessionID: "guide-discuss:" + g.ID + ":" + session,
		FreshSession: turn.first,
		Message:      turn.prompt(),
		AppTools:     tools,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	reply, value := SplitDraftReply(res.Text)
	writeJSON(w, map[string]any{"reply": reply, "value": value})
}

// discussTools is the Author's kit for a discussion: everything but the tools
// that change the guide, and for a Private guide nothing that reaches the
// network or executes, as on a chat turn.
func discussTools(all []AgentToolDef, private bool) []AgentToolDef {
	var out []AgentToolDef
	for _, t := range all {
		writes := false
		for _, c := range t.Tool.Caps {
			if c == CapWrite {
				writes = true
			}
		}
		if !writes {
			out = append(out, t)
		}
	}
	if private {
		out = withoutTools(out, "research")
		out = FilterToolsByCaps(out, []Capability{CapRead})
	}
	return out
}
