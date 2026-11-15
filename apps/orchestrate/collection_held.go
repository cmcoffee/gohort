package orchestrate

// Held pages: a page the collection check found contradicting what the
// collection holds, waiting for its owner (docs/design/knowledge-vetting.md).
//
// A held page is a row in the collection's suggestions queue with Kind
// "held": durable, and kept from the curator, which works the rest of the
// queue. The owner settles it by hand (add it, add it in place of the
// document it contradicts, or keep the document), or has it argued out first:
// Settle it runs two voices, one for each side, that look for evidence and
// cite it, checks their citations against what they read, and rules. The
// ruling is shown on the row; nothing changes until the owner chooses.
//
//	GET  /api/collections/{id}/held                    the held pages
//	POST /api/collections/{id}/held/add?sid=           add it anyway
//	POST /api/collections/{id}/held/replace?sid=       add it, remove the one it contradicts
//	POST /api/collections/{id}/held/keep?sid=          keep the document, drop the page
//	GET  /api/collections/{id}/held/settle?sid=        the Settle it row
//	POST /api/collections/{id}/held/settle?sid=        start settling it
//	GET  /api/collections/{id}/held/settle/progress    {"progress"} or {"outcome"}
//	POST /api/collections/{id}/held/settle/stop        stop it

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/decide"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/textutil"
)

// heldFrom is who a held row says it is from.
const heldFrom = "Collection check"

// holdPage puts a page the check found contradicting the collection in front
// of its owner.
func (T *OrchestrateApp) holdPage(c Collection, cand vetCandidate, res vetResult) error {
	_, err := T.addSuggestion(c, collectionSuggestion{
		Kind:         heldKind,
		Title:        chFirst(strings.TrimSpace(cand.Title), cand.URL),
		Text:         heldText(res),
		Source:       cand.URL,
		AgentName:    heldFrom,
		AgainstDoc:   res.AgainstDoc,
		AgainstTitle: res.AgainstTitle,
	})
	if errors.Is(err, errAlreadySuggested) {
		return nil
	}
	return err
}

// heldText is what a held row says it contradicts.
func heldText(res vetResult) string {
	if res.AgainstTitle == "" {
		return "Conflicts with what the collection holds: " + res.Against
	}
	return fmt.Sprintf("Conflicts with %q: %s", res.AgainstTitle, res.Against)
}

// heldURL reports whether a page at this address is already held on c.
func (T *OrchestrateApp) heldURL(c Collection, url string) bool {
	norm := normalizeIngestURL(url)
	for _, s := range T.listHeld(c) {
		if s.Source != "" && normalizeIngestURL(s.Source) == norm {
			return true
		}
	}
	return false
}

// agentPageCheck is the check as the agent in charge of a collection meets
// it, through its scoped tool: a line for the tool result, and whether the
// page goes in. Text without an address (add_text) that contradicts the
// collection is turned away rather than held: there is no page to add later.
func (T *OrchestrateApp) agentPageCheck(ctx context.Context, c Collection, cand vetCandidate) (string, bool) {
	if cand.URL != "" && T.heldURL(c, cand.URL) {
		return "Not added: this page is already held for the owner, who decides on it from the collection's page. Do not add it another way.", false
	}
	res := vetPage(ctx, T.LLM, c, chFirst(c.Owner, c.ID), cand)
	Log("[orchestrate.vet] collection=%s page=%q answer=%q against=%q err=%v", c.ID, chFirst(cand.URL, cand.Title), res.Answer, res.AgainstTitle, res.Err)
	switch {
	case res.Answer == vetSupersedes:
		return fmt.Sprintf("It is a newer version of %q: name that one in your reply as a proposed removal.", res.AgainstTitle), true
	case res.addsIt():
		return "", true
	case res.Answer == vetConflicts && cand.URL != "":
		if err := T.holdPage(c, cand, res); err != nil {
			return "Not added: " + res.note() + ". It could not be held for the owner either (" + err.Error() + "); say so in your reply.", false
		}
		return "Not added: " + res.note() + ". It is held for the owner to decide on the collection's page; say so in your reply, and do not add it another way.", false
	case res.Answer == vetConflicts:
		return "Not added: " + res.note() + ". Say so in your reply to the owner.", false
	}
	return capitalize(res.note()) + ".", false
}

// capitalize upper-cases a line's first letter.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// heldAt resolves a held row from the page's request.
func (T *OrchestrateApp) heldAt(call *jsonapi.Call) (Collection, collectionSuggestion, error) {
	c, err := T.collectionAt(call, "suggestions")
	if err != nil {
		return c, collectionSuggestion{}, err
	}
	s, ok := T.findSuggestion(c, call.R.URL.Query().Get("sid"))
	if !ok || s.Kind != heldKind {
		return c, s, errNotFound
	}
	return c, s, nil
}

// collectionHeld lists the held pages for the page.
func (T *OrchestrateApp) collectionHeld(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "suggestions")
	if err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	for _, s := range T.listHeld(c) {
		link := ""
		if isArticleLink(s.Source) {
			link = s.Source
		}
		rows = append(rows, map[string]any{
			"id":            s.ID,
			"title":         chFirst(s.Title, s.Source),
			"source_link":   link,
			"conflict":      s.Text,
			"against_title": s.AgainstTitle,
			"has_doc":       s.AgainstDoc != "" && link != "",
			"ruling":        rulingLine(s),
			"created":       s.Created,
		})
	}
	return map[string]any{"held": rows}, nil
}

// collectionHeldAdd adds a held page anyway: the owner's call, so unchecked.
func (T *OrchestrateApp) collectionHeldAdd(call *jsonapi.Call, _ struct{}) (any, error) {
	if _, _, err := T.heldAt(call); err != nil {
		return nil, err
	}
	return T.collectionSuggestionFetch(call, struct{}{})
}

// collectionHeldReplace adds a held page and removes the document it
// contradicts.
func (T *OrchestrateApp) collectionHeldReplace(call *jsonapi.Call, _ struct{}) (any, error) {
	c, s, err := T.heldAt(call)
	if err != nil {
		return nil, err
	}
	if s.AgainstDoc == "" {
		return nil, jsonapi.BadRequest("this page does not name a document it contradicts, so there is nothing to replace: add it, or keep what is there")
	}
	out, err := T.collectionSuggestionFetch(call, struct{}{})
	if err != nil {
		return nil, err
	}
	prefix := collectionSource(c.ID)
	removed := DeleteChunksWhere(T.collectionDB(c), func(x EmbeddedChunk) bool {
		return x.ReportID == s.AgainstDoc && strings.HasPrefix(x.Source, prefix)
	})
	Log("[orchestrate.collections] user=%q replaced %q with held page %q in collection %q (%d chunks removed)", call.User, s.AgainstDoc, s.Source, c.ID, removed)
	if m, ok := out.(map[string]any); ok {
		m["removed"] = removed
	}
	return out, nil
}

// collectionHeldKeep keeps the document and drops the held page.
func (T *OrchestrateApp) collectionHeldKeep(call *jsonapi.Call, _ struct{}) (any, error) {
	c, s, err := T.heldAt(call)
	if err != nil {
		return nil, err
	}
	T.dropSuggestion(c, s.ID)
	return map[string]any{"ok": true}, nil
}

// --- Settle it -------------------------------------------------------------

// settleKey is a settle run's key in the run registry: one per held page.
func settleKey(collection, sid string) string { return "settle:" + collection + "/" + sid }

// The answers a settle run can give.
const (
	settlePage      = "page"
	settleDocument  = "document"
	settleBoth      = "both"
	settleUnsettled = "unsettled"
)

// rulingLine is what the page says about a held page's ruling.
func rulingLine(s collectionSuggestion) string {
	if s.Ruling == "" {
		return ""
	}
	var head string
	switch s.Ruling {
	case settlePage:
		head = "The page is right: replace " + chFirst(fmt.Sprintf("%q", s.AgainstTitle), "the document")
	case settleDocument:
		head = "The collection's document is right: keep it"
	case settleBoth:
		head = "Both hold, for different cases or versions: add the page beside it"
	default:
		head = "Not settled by the evidence found"
	}
	line := head + "."
	if w := strings.TrimSpace(s.RulingWhy); w != "" {
		line += " " + w
	}
	return line
}

// settleVerdict rules on the argued dispute.
var settleVerdict = decide.Verdict{
	Key: "app.orchestrate.collection.settle",
	Role: `A page about to be added to a knowledge collection contradicts a document already in it. Two sides argued it, each citing what it read, and their citations were checked against the pages read. Decide which is right.

Rule on the evidence, not on which side argued better. A claim whose citation did not hold up, or that cites nothing, counts for nothing. Between sources about the same version of the same thing, prefer the primary or more authoritative one, and then the more current. When both are right about different versions, scopes or cases, say both. When the evidence read does not decide it, say so: an honest unsettled beats a guess.`,
	Answers: []decide.Choice{
		{Key: settlePage, Means: "the new page is right on the point in dispute, and the collection's document is wrong or out of date"},
		{Key: settleDocument, Means: "the collection's document is right, and the new page is wrong"},
		{Key: settleBoth, Means: "both are right, about different versions, scopes or cases"},
		{Key: settleUnsettled, Means: "the evidence read does not decide it"},
	},
	Weigh: []decide.Step{
		{Key: "page_case", Ask: "the new page's strongest point that its evidence supports, or none"},
		{Key: "document_case", Ask: "the document's strongest point that its evidence supports, or none"},
		{Key: "why", Ask: "one or two sentences for the owner: the reason for the ruling, naming the source that decides it"},
	},
}

// settleJudge is the agent stage the settle pipeline ends on: the pipeline
// fills its prompt with the dispute, the arguments, the citation check and
// the sources, and the run hands that to settleVerdict.
const settleJudge = "settle-judge"

// settleVoices are the two sides.
var settleVoices = []string{"The new page", "The collection's document"}

// settleDef is the settle pipeline: the two sides argue over two rounds,
// looking things up when the deployment can search; their citations are
// checked; the judge rules.
func settleDef(research bool) PipelineDef {
	argue := PipelineStage{
		Name: "argue", Kind: StagePanel, Count: 2, Panel: settleVoices,
		Prompt: "You are {voice}, in a dispute over what a knowledge collection should hold. Round {iteration} of {iterations}.\n\n{input}\n\n" +
			"Argue that your side is right on the point in dispute, from your own text and from what you have read. Cite what you read by its number, as [1]. " +
			"Say plainly where the other side is right, or where the evidence does not settle it: a concession that holds beats a claim that does not. " +
			"Keep to the point in dispute, in at most 200 words.",
	}
	if research {
		argue.Research = 2
		argue.Tools = []string{"web_search", "fetch_url"}
	}
	return PipelineDef{Name: "Settle a held page", Stages: []PipelineStage{
		argue,
		{Name: "check", Kind: StageVerify, Check: "argue"},
		{Name: "rule", Kind: StageAgent, Agent: settleJudge,
			Prompt: "## The dispute\n{input}\n\n## What each side argued\n{stage:argue}\n\n## How their citations held up\n{stage:check}\n\n## What they read\n{sources}"},
	}}
}

// settleRunPipeline runs a settle pipeline. A variable so a test can stand
// in for the pipeline engine's tools.
var settleRunPipeline = func(T *OrchestrateApp, ctx context.Context, owner string, def PipelineDef, input string, h PipelineHooks) (string, error) {
	h.Tools = T.pipelineStandaloneTools(ctx, owner, def)
	out, _, err := T.RunPipelineDefHooks(ctx, def, input, h)
	return out, err
}

// settleSides is the dispute as both sides are given it: the conflict, and
// the part of each text that bears on it.
func (T *OrchestrateApp) settleSides(ctx context.Context, c Collection, s collectionSuggestion, note func(string)) (string, error) {
	note("reading the page")
	fctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	_, pageText, _, _, err := ingestFetch(fctx, s.Source)
	cancel()
	if err != nil {
		return "", fmt.Errorf("the page could not be read again: %v", err)
	}
	note("reading the collection's document")
	var docText []string
	for _, e := range collectionExcerpts(ctx, c, chFirst(c.Owner, c.ID), s.Text) {
		if e.DocID == s.AgainstDoc {
			docText = append(docText, strings.TrimSpace(e.Text))
		}
	}
	if len(docText) == 0 {
		// The search ranked other documents above it: read it whole.
		prefix := collectionSource(c.ID)
		for _, ch := range ChunksWhere(T.collectionDB(c), func(x EmbeddedChunk) bool {
			return x.ReportID == s.AgainstDoc && strings.HasPrefix(x.Source, prefix)
		}) {
			docText = append(docText, strings.TrimSpace(ch.Text))
		}
	}
	if len(docText) == 0 {
		return "", fmt.Errorf("the document it contradicts (%s) is no longer in the collection", chFirst(s.AgainstTitle, s.AgainstDoc))
	}
	var b strings.Builder
	b.WriteString("## The point in dispute\n" + s.Text + "\n\n")
	b.WriteString("## The new page: " + chFirst(s.Title, s.Source) + " (" + s.Source + ")\n")
	b.WriteString(UntrustedFence("the new page", bearingOn(pageText, s.Text, 3000)) + "\n\n")
	b.WriteString("## The collection's document: " + chFirst(s.AgainstTitle, s.AgainstDoc) + "\n")
	b.WriteString(UntrustedFence("the collection's document", textutil.Clip(strings.Join(docText, "\n\n"), 3000)))
	return b.String(), nil
}

// bearingOn is the part of text that bears on about: its paragraphs sharing
// the most words with it, in their own order, up to max characters.
func bearingOn(text, about string, max int) string {
	words := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(about)) {
		if w = strings.Trim(w, ".,;:!?\"'()[]"); len(w) > 3 {
			words[w] = true
		}
	}
	paras := strings.Split(text, "\n\n")
	type scored struct{ i, n int }
	var ranked []scored
	for i, p := range paras {
		n := 0
		for _, w := range strings.Fields(strings.ToLower(p)) {
			if words[strings.Trim(w, ".,;:!?\"'()[]")] {
				n++
			}
		}
		ranked = append(ranked, scored{i, n})
	}
	sort.SliceStable(ranked, func(a, b int) bool { return ranked[a].n > ranked[b].n })
	keep := map[int]bool{}
	size := 0
	for _, r := range ranked {
		p := strings.TrimSpace(paras[r.i])
		if p == "" || size+len(p) > max {
			continue
		}
		keep[r.i] = true
		size += len(p) + 2
	}
	var out []string
	for i, p := range paras {
		if keep[i] {
			out = append(out, strings.TrimSpace(p))
		}
	}
	if len(out) == 0 {
		return textutil.Clip(strings.TrimSpace(text), max)
	}
	return strings.Join(out, "\n\n")
}

// settle argues a held page out and records the ruling on its row.
func (T *OrchestrateApp) settle(ctx context.Context, c Collection, sid string, note func(string)) (string, error) {
	s, ok := T.findSuggestion(c, sid)
	if !ok || s.Kind != heldKind {
		return "", fmt.Errorf("that page is no longer held")
	}
	if s.AgainstDoc == "" {
		return "", fmt.Errorf("the check did not name the document it contradicts, so there is nothing to argue it against: add it or keep what is there")
	}
	input, err := T.settleSides(ctx, c, s, note)
	if err != nil {
		return "", err
	}
	var ruling decide.Ruling
	var ruled bool
	owner := chFirst(c.Owner, c.ID)
	_, err = settleRunPipeline(T, ctx, owner, settleDef(WebSearchAvailable()), input, PipelineHooks{
		Status: func(line string) { note(textutil.Clip(line, 140)) },
		Dispatch: func(ctx context.Context, agentKey, prompt string) (string, error) {
			if agentKey != settleJudge {
				// A voice is a role the worker answers as, not an agent.
				return "", ErrNoSuchAgent
			}
			note("ruling")
			r, err := settleVerdict.Judge(ctx, decide.VerdictChat(T.LLM.Chat), prompt,
				WithRouteKey(settleVerdict.Key), WithMaxTokens(900))
			if err != nil {
				return "", err
			}
			ruling, ruled = r, true
			return r.Reason, nil
		},
	})
	if err != nil {
		return "", err
	}
	if !ruled || ruling.Answer == "" {
		return "", fmt.Errorf("no ruling came back")
	}
	// The row may have been acted on while it was argued.
	cur, ok := T.findSuggestion(c, sid)
	if !ok {
		return "Settled, but the page was dealt with meanwhile.", nil
	}
	cur.Ruling, cur.RulingWhy, cur.RulingAt = ruling.Answer, strings.TrimSpace(ruling.Reason), time.Now()
	T.suggestionsDB(c).Set(collectionSuggestionsTable, c.ID+"/"+cur.ID, cur)
	return rulingLine(cur), nil
}

func (T *OrchestrateApp) collectionHeldSettleItems(call *jsonapi.Call) (any, error) {
	_, s, err := T.heldAt(call)
	if err != nil {
		return nil, err
	}
	desc := "Two sides argue it, one for the page and one for the collection's document, each citing what it reads; their citations are checked against those pages; then a ruling. Nothing changes until you choose."
	if !WebSearchAvailable() {
		desc = "Two sides argue it from the two texts (web search is not set up, so they cannot look further), then a ruling. Nothing changes until you choose."
	}
	history := "Not settled yet."
	if line := rulingLine(s); line != "" {
		history = "Ruled " + s.RulingAt.Local().Format("Mon 2 Jan") + ": " + line
	}
	return []map[string]any{{"item": "Settle it", "desc": desc, "history": history}}, nil
}

func (T *OrchestrateApp) collectionHeldSettleStart(call *jsonapi.Call, _ struct{}) (any, error) {
	c, s, err := T.heldAt(call)
	if err != nil {
		return nil, err
	}
	if T.LLM == nil {
		return nil, jsonapi.BadRequest("no model is set up to settle it")
	}
	_, started := curations.start(settleKey(c.ID, s.ID), "Settling it", 0, nil, func(ctx context.Context, note func(string)) (string, error) {
		return T.settle(ctx, c, s.ID, note)
	})
	if !started {
		return map[string]any{"running": true, "message": "Already settling this one"}, nil
	}
	return map[string]any{"running": true, "message": "Settling " + chFirst(s.Title, s.Source)}, nil
}

func (T *OrchestrateApp) collectionHeldSettleProgress(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "suggestions")
	if err != nil {
		return nil, err
	}
	if run, ok := curations.get(settleKey(c.ID, call.R.URL.Query().Get("sid"))); ok {
		return run.status(), nil
	}
	return map[string]any{}, nil
}

func (T *OrchestrateApp) collectionHeldSettleStop(call *jsonapi.Call, _ struct{}) (any, error) {
	c, err := T.collectionAt(call, "suggestions")
	if err != nil {
		return nil, err
	}
	if run, ok := curations.get(settleKey(c.ID, call.R.URL.Query().Get("sid"))); ok {
		run.stop()
	}
	return map[string]any{"ok": true}, nil
}

// checkedAddText is add_text for the agent in charge: the text is checked
// before inner adds it, and turned away when it contradicts the collection.
func (T *OrchestrateApp) checkedAddText(inner func(map[string]any, *ToolSession) (string, error)) func(map[string]any, *ToolSession) (string, error) {
	return func(args map[string]any, sess *ToolSession) (string, error) {
		if sess == nil || sess.DB == nil || sess.Username == "" {
			return inner(args, sess)
		}
		c, ok := loadCollection(sess.DB, sess.Username, strings.TrimSpace(stringArg(args, "id")))
		if !ok {
			return inner(args, sess)
		}
		ctx, cancel := context.WithTimeout(context.Background(), vetTimeout)
		note, add := T.agentPageCheck(ctx, c, vetCandidate{Title: stringArg(args, "title"), Text: stringArg(args, "text")})
		cancel()
		if !add {
			return note, nil
		}
		out, err := inner(args, sess)
		if err != nil || note == "" {
			return out, err
		}
		return out + " " + note, nil
	}
}
