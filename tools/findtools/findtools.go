// find_tools — LLM-callable safety net for the framework's
// classifier-driven tool surfacing. The framework pre-selects the
// top-K most relevant tools each turn via embedding match; this
// tool lets the LLM explicitly search when its question's intent
// didn't surface what it needs (mid-conversation pivot, unusual
// phrasing, ambiguous request, or a follow-up that drifts from the
// initial topic).
//
// Always-on tool. Skipping the classifier when the LLM searches
// explicitly is intentional — the user-stated query is a stronger
// signal of intent than the latest message alone.

package findtools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(&FindToolsTool{}) }

// FindToolsTool implements ChatTool. Holds no state; the index it
// queries lives in core/tool_index.go and rebuilds lazily as
// tools register / deregister.
type FindToolsTool struct{}

func (t *FindToolsTool) Name() string { return "find_tools" }

func (t *FindToolsTool) Desc() string {
	return "Search the deployment's tool catalog by intent. Use when the question or task doesn't match any tool currently visible in your catalog: the framework only surfaces the top-K most relevant tools per turn, so specialty tools (image editing, video processing, niche APIs, etc.) may be hidden until you search for them. Pass a short query describing what you want to do (\"resize an image\", \"download a video\", \"check the weather\"). Returns matching tool names + descriptions ranked by relevance. After a match, call the specific tool by name."
}

// CapRead — the search itself is just an embed + cosine pass over
// an in-memory index, no network or filesystem writes.
func (t *FindToolsTool) Caps() []Capability { return []Capability{CapRead} }

// IsFrameworkTool — keeps find_tools out of the classifier's index
// itself (we don't want "find tools" to surface from a question
// like "find me cats" — it's a meta-tool, always available, not
// classifier-selected).
func (t *FindToolsTool) IsFrameworkTool() bool { return true }

func (t *FindToolsTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"query": {
			Type:        "string",
			Description: "Short description of the capability you want. Phrase it as the operation, not the question (\"resize image\" not \"how do I resize an image?\"). The framework matches against tool descriptions, so action verbs + nouns work better than full sentences.",
		},
		"k": {
			Type:        "integer",
			Description: "Optional. Max number of matching tools to return. Default 5; useful range 3-10. Higher values return more candidates but make picking the right one harder.",
		},
	}
}

func (t *FindToolsTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("find_tools requires a session context")
}

func (t *FindToolsTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	return t.RunWithSessionContext(context.Background(), args, sess)
}

// RunWithSessionContext is the search with the round it runs in: the tools
// the agent was given this turn (TurnToolsFromContext) are matched as well
// as the index, since a tool handed to one turn (posting to a board,
// publishing, asking the owner) is in no index, and a search that missed it
// told the agent it was not installed.
func (t *FindToolsTool) RunWithSessionContext(ctx context.Context, args map[string]any, sess *ToolSession) (string, error) {
	query := strings.TrimSpace(stringArg(args, "query"))
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	k := 5
	if v, ok := args["k"]; ok {
		switch n := v.(type) {
		case int:
			if n > 0 {
				k = n
			}
		case float64:
			if int(n) > 0 {
				k = int(n)
			}
		}
	}
	if k > 25 {
		k = 25
	}
	// Background ctx is fine — Embed() has its own timeout. The
	// search itself is in-memory cosine; no per-call ctx needed.
	hits := LookupToolsByQuery(context.Background(), query, k)
	// The tools this round already has: the ones the query names, flagged
	// so the agent calls them instead of concluding they do not exist.
	inHand := map[string]bool{}
	if turn := turnToolMatches(TurnToolsFromContext(ctx)); len(turn) > 0 {
		seen := map[string]bool{}
		for _, h := range hits {
			seen[h.Name] = true
		}
		for _, m := range loadableMatches(query, turn) {
			inHand[m.Name] = true
			if !seen[m.Name] {
				hits = append(hits, m)
			}
		}
	}
	// The turn's load-before-use tools are not in the index: add the ones
	// the query names, flagged so the caller loads them first.
	loadable := map[string]bool{}
	if sess != nil && sess.LoadableTools != nil {
		for _, m := range loadableMatches(query, sess.LoadableTools()) {
			loadable[m.Name] = true
			hits = append(hits, m)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	// What the app knows about this capability that no index holds.
	note := ""
	if sess != nil && sess.FindToolsNote != nil {
		if n := strings.TrimSpace(sess.FindToolsNote(query)); n != "" {
			note = "\n\nNOTE: " + n
		}
	}
	if len(hits) == 0 {
		return fmt.Sprintf("No matching tools for %q. The deployment's tool index may be empty, OR the embedder is offline, OR no registered tool's description matches that intent. Try a different phrasing; if still nothing, the capability may genuinely not be installed.", query) + note, nil
	}
	type out struct {
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Score       float32 `json:"score"`
		Note        string  `json:"note,omitempty"`
	}
	rows := make([]out, len(hits))
	for i, h := range hits {
		rows[i] = out{Name: h.Name, Description: h.Description, Score: h.Score}
		switch {
		case loadable[h.Name]:
			rows[i].Note = "not loaded yet: call load_tool(names=[\"" + h.Name + "\"]) first, then use it"
		case inHand[h.Name]:
			rows[i].Note = "already in your tools this turn: call it directly"
		}
	}
	b, _ := json.Marshal(rows)
	return string(b) + note, nil
}

// turnToolMatches is the round's tools as search candidates, less the
// meta-tools no search is ever for.
func turnToolMatches(tools []Tool) []ToolMatch {
	var out []ToolMatch
	for _, t := range tools {
		switch t.Name {
		case "", "find_tools", "load_tool":
			continue
		}
		out = append(out, ToolMatch{Name: t.Name, Description: t.Description})
	}
	return out
}

// stringArg coerces an arbitrary tool-arg value to string. Local
// copy to avoid pulling orchestrate's helper into this leaf package.
func stringArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// loadableMatches scores the turn's load-before-use tools against the query by
// word overlap, name and description against the query's words. Overlap and
// not embeddings, because these tools are assembled per turn and a handful of
// names is not worth an embedding call per search. A hit needs a third of the
// query's words, or its name in the query.
func loadableMatches(query string, tools []ToolMatch) []ToolMatch {
	qw := searchWords(query)
	if len(qw) == 0 {
		return nil
	}
	var out []ToolMatch
	for _, t := range tools {
		tw := map[string]bool{}
		for _, w := range searchWords(t.Name + " " + t.Description) {
			tw[w] = true
		}
		matched, nameHit := 0, false
		name := strings.ToLower(t.Name)
		for _, w := range qw {
			if tw[w] {
				matched++
			}
			if strings.Contains(name, w) {
				nameHit = true
			}
		}
		score := float32(matched) / float32(len(qw))
		if nameHit && score < 0.5 {
			score = 0.5
		}
		if score < 0.34 {
			continue
		}
		out = append(out, ToolMatch{Name: t.Name, Description: t.Description, Score: score})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// searchWords lowercases, splits on anything but letters and digits, keeps
// words of three letters or more, and trims the commonest English endings so
// "scheduled", "scheduling" and "schedule" are one word. Distinct, in order.
func searchWords(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		w = stemWord(w)
		if len(w) < 3 || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

func stemWord(w string) string {
	for _, suf := range []string{"ing", "ed", "es", "s"} {
		if len(w)-len(suf) >= 3 && strings.HasSuffix(w, suf) {
			w = strings.TrimSuffix(w, suf)
			break
		}
	}
	return strings.TrimSuffix(w, "e")
}
