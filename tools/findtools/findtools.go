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
	return "Search the deployment's tool catalog by intent. Use when the question or task doesn't match any tool currently visible in your catalog — the framework only surfaces the top-K most relevant tools per turn, so specialty tools (image editing, video processing, niche APIs, etc.) may be hidden until you search for them. Pass a short query describing what you want to do (\"resize an image\", \"download a video\", \"check the weather\"). Returns matching tool names + descriptions ranked by relevance. After a match, call the specific tool by name."
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
	if len(hits) == 0 {
		return fmt.Sprintf("No matching tools for %q. The deployment's tool index may be empty, OR the embedder is offline, OR no registered tool's description matches that intent. Try a different phrasing; if still nothing, the capability may genuinely not be installed.", query), nil
	}
	type out struct {
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Score       float32 `json:"score"`
	}
	rows := make([]out, len(hits))
	for i, h := range hits {
		rows[i] = out{Name: h.Name, Description: h.Description, Score: h.Score}
	}
	b, _ := json.Marshal(rows)
	return string(b), nil
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
