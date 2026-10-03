package prompts

// Per-tier text: a block worded differently for the lead and for the worker.
//
// The exception, not the rule. A block stays one text for both tiers until
// there is evidence the two models want different words (the tuning harness
// is how a deployment gets that evidence). So this is its own small section
// under the editor, listing the blocks that have split, rather than two more
// text boxes on every block.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/prompts"
	"github.com/cmcoffee/gohort/core/sections"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() {
	RegisterAdminSectionSource(func(r *http.Request) []sections.AdminSectionEntry {
		if !RequestIsAdmin(r) {
			return nil
		}
		return []sections.AdminSectionEntry{{App: "/prompts", Section: tierTextSection()}}
	})
}

func (T *PromptsApp) tierRoutes() {
	T.HandleFunc("/api/tier", T.adminGated(T.handleTierList))        // GET -> rows
	T.HandleFunc("/api/tier/add", T.adminGated(T.handleTierAdd))     // POST {block, tier, text}
	T.HandleFunc("/api/tier/one", T.adminGated(T.handleTierOne))     // GET/POST ?ref=tier|key {text}
	T.HandleFunc("/api/tier/remove", T.adminGated(T.handleTierDrop)) // POST ?ref=
}

// tierModel is what a tier runs now, as configured: the lead falls back to
// the worker when there is no separate one, so its text is read by the worker.
func tierModel(tier string) string {
	worker, lead := LiveLLMs()
	if tier == prompts.TierLead && lead != "" {
		return lead
	}
	return worker
}

func tierRef(tier, key string) string { return tier + "|" + key }

// tierTextProblem says why text cannot be a tier's own wording for block b,
// or "" when it can. The swap into a prompt is made on the way out, by
// finding the block's shared text and putting this in its place, so the
// shared text has to be findable and this has to fill only the placeholders
// the shared text fills.
func tierTextProblem(b PromptBlock, text string) string {
	shared := EffectivePromptText(b.Key, b.Text)
	if why := prompts.TierTextPlaceable(b.Key, shared); why != "" {
		return "This block cannot be worded per tier: " + why + "."
	}
	if why := prompts.TierPlaceholderProblem(shared, text); why != "" {
		return "This text " + why + "."
	}
	return ""
}

// ApplyTierEdit sets a tier's own text for a block from outside this page (a
// promotion from the tuning harness), stamped with the model the tier runs
// now. Empty text removes it, so the tier reads the block's text again.
func ApplyTierEdit(tier, key, text string) error {
	if tier != prompts.TierLead && tier != prompts.TierWorker {
		return fmt.Errorf("a tier is lead or worker, not %q", tier)
	}
	b, ok := lookupBlock(key)
	if !ok {
		return fmt.Errorf("no prompt block %q", key)
	}
	if strings.TrimSpace(text) != "" {
		if why := tierTextProblem(b, text); why != "" {
			return fmt.Errorf("%s", why)
		}
	}
	prompts.SetPromptTierOverride(tier, key, text, tierModel(tier))
	return nil
}

func (T *PromptsApp) handleTierList(w http.ResponseWriter, r *http.Request) {
	type row struct {
		Ref   string    `json:"ref"`
		Block string    `json:"block"`
		Title string    `json:"title"`
		Tier  string    `json:"tier"`
		Model string    `json:"model"`
		Stale bool      `json:"stale"`
		Set   time.Time `json:"set"`
		// Used is when this text last went out to its tier, since the
		// server started; Unused says it has not, so a text that never
		// reaches a prompt is visible rather than assumed in effect.
		Used    *time.Time `json:"used,omitempty"`
		Unused  bool       `json:"unused"`
		Preview string     `json:"preview"`
	}
	out := []row{}
	for _, b := range AllPromptBlocks() {
		for _, tier := range prompts.Tiers() {
			o, ok := prompts.PromptTierOverride(tier, b.Key)
			if !ok {
				continue
			}
			rw := row{Ref: tierRef(tier, b.Key), Block: b.Key, Title: b.Title, Tier: tier,
				Model: o.Model, Stale: prompts.TierOverrideStale(tier, b.Key, tierModel(tier)), Set: o.At,
				Preview: oneLine(o.Text, 160)}
			if at := prompts.TierTextApplied(tier, b.Key); !at.IsZero() {
				rw.Used = &at
			} else {
				rw.Unused = true
			}
			out = append(out, rw)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Title+out[i].Tier < out[j].Title+out[j].Tier })
	writeJSON(w, out)
}

// handleTierAdd gives a tier its own text for a block. GET is the form's
// starting values.
func (T *PromptsApp) handleTierAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, map[string]any{"block": "", "tier": prompts.TierWorker})
		return
	}
	var body struct {
		Block string `json:"block"`
		Tier  string `json:"tier"`
		Text  string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	b, ok := lookupBlock(body.Block)
	if !ok {
		http.Error(w, "pick a block", http.StatusBadRequest)
		return
	}
	if body.Tier != prompts.TierLead && body.Tier != prompts.TierWorker {
		http.Error(w, "pick the lead or the worker", http.StatusBadRequest)
		return
	}
	text := body.Text
	if strings.TrimSpace(text) == "" {
		// Start the tier from what it reads now, so the split begins as a copy
		// to change rather than as an empty block that silences it.
		text = prompts.EffectivePromptTextFor(body.Tier, b.Key, b.Text)
	}
	if why := tierTextProblem(b, text); why != "" {
		http.Error(w, why, http.StatusBadRequest)
		return
	}
	prompts.SetPromptTierOverride(body.Tier, b.Key, text, tierModel(body.Tier))
	writeJSON(w, map[string]any{"ok": true})
}

// handleTierOne reads or rewrites one tier's text. ?ref=tier|key.
func (T *PromptsApp) handleTierOne(w http.ResponseWriter, r *http.Request) {
	tier, key, _ := strings.Cut(r.URL.Query().Get("ref"), "|")
	b, ok := lookupBlock(key)
	if !ok {
		http.Error(w, "no such block", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodPost {
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.Text) != "" {
			if why := tierTextProblem(b, body.Text); why != "" {
				http.Error(w, why, http.StatusBadRequest)
				return
			}
		}
		prompts.SetPromptTierOverride(tier, b.Key, body.Text, tierModel(tier))
	}
	o, _ := prompts.PromptTierOverride(tier, b.Key)
	writeJSON(w, map[string]any{"text": o.Text, "all_tiers": EffectivePromptText(b.Key, b.Text), "model": o.Model})
}

func (T *PromptsApp) handleTierDrop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	tier, key, _ := strings.Cut(r.URL.Query().Get("ref"), "|")
	prompts.ClearPromptTierOverride(tier, key)
	writeJSON(w, map[string]any{"ok": true})
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

const tierSource = "/prompts/api/tier"

func tierTextSection() ui.Section {
	var blocks []ui.SelectOption
	for _, b := range AllPromptBlocks() {
		blocks = append(blocks, ui.SelectOption{Value: b.Key, Label: chFirst(b.Category, "Other") + ": " + b.Title})
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].Label < blocks[j].Label })
	tiers := []ui.SelectOption{{Value: prompts.TierWorker, Label: "The worker"}, {Value: prompts.TierLead, Label: "The lead"}}
	edit := ui.FormPanel{Source: "/prompts/api/tier/one?ref={ref}", PostURL: "/prompts/api/tier/one?ref={ref}",
		Invalidate: []string{tierSource},
		Fields: []ui.FormField{
			{Field: "text", Type: "textarea", Rows: 18, Label: "This tier's text"},
			{Field: "all_tiers", Type: "readonly", Label: "What every other tier reads"},
		}}
	editAction := ui.ModalAction("Edit", edit)
	editAction.Width = "900px"
	return ui.Section{
		Group:    "Prompts",
		Title:    "Per-tier text",
		Subtitle: "A block worded differently for the lead and for the worker. Only for a block with evidence that the two models want different words.",
		Detail: "A tier reads its own text here when it has one, else the block's text from the editor above, else what gohort ships. " +
			"Each one remembers the model its tier was running when it was written, and is flagged when that tier now runs a different model: " +
			"words fitted to one model are a guess for the next.\n\n" +
			"Adding one starts it as a copy of what the tier reads now; edit it from its row.\n\n" +
			"The tier's wording is put in at the moment a call goes out, once it is known which model is answering: a lead call that falls back to the worker carries the worker's wording. " +
			"Last used says when that last happened since the server started. A text never used is one whose block is in no prompt that tier was sent. " +
			"Keep the block's placeholders, like {rounds}: they are filled with the same values. The prompt viewer shows the shared text, not the tier's.",
		Wide: true,
		Body: ui.Stack{Children: []ui.Component{
			ui.FormPanel{Source: "/prompts/api/tier/add", PostURL: "/prompts/api/tier/add", SubmitLabel: "Add",
				Invalidate: []string{tierSource},
				Fields: []ui.FormField{
					{Field: "block", Type: "select", Label: "Block", Options: blocks},
					{Field: "tier", Type: "select", Label: "Tier", Options: tiers},
				}},
			ui.Table{Source: tierSource, RowKey: "ref",
				Columns: []ui.Col{
					{Field: "title", Label: "Block", Flex: 3},
					{Field: "tier", Label: "Tier"},
					{Field: "stale", Label: "", Type: "badge", Badges: []ui.BadgeMapping{{Value: true, Label: "model changed", Color: "warning"}}},
					{Field: "model", Label: "Written for", Flex: 2, Mute: true},
					{Field: "set", Label: "Set", Format: "reltime", Mute: true},
					{Field: "used", Label: "Last used", Format: "reltime", Mute: true},
					{Field: "unused", Label: "", Type: "badge", Badges: []ui.BadgeMapping{{Value: true, Label: "not used yet", Color: "mute"}}},
					{Field: "preview", Label: "", Flex: 5, Mute: true, Line: 2},
				},
				RowActions: []ui.RowAction{
					editAction,
					{Type: "button", Label: "Remove", Variant: "danger", Compact: true, PostTo: "/prompts/api/tier/remove?ref={ref}",
						Confirm: "Remove this tier's own text? The tier reads the block's text from the editor again."},
				},
				EmptyText: "No block has a tier-specific text. Every tier reads the editor's text."},
		}},
	}
}

func chFirst(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
