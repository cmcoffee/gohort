package prompts

// Check, its readings: what a model takes a block to mean.
//
// Wording that reads plainly to the person who wrote it can read otherwise to
// the model it is for, and the first sign is usually a build that went wrong
// for a reason no one can see. Here the model says back, in a few sentences,
// what the open block tells it to do; for a tool's description, when it would
// reach for the tool first; for a parameter's, what it would put in it. The
// worker and the lead each read the wording they would be sent, so a split
// block is read in both its versions, and the two readings sit side by side.
//
// A reading is what the model says it understood, not what it does. Check is
// the quick look while editing; a section another package adds to it (the
// tuning harness's probe of what Builder reaches for first) shows what it
// does.

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/prompts"
)

// readBackMaxText bounds the text one reading sends, so a pasted wall of text
// is not a runaway prompt.
const readBackMaxText = 24000

// readBackSystem is the brief every reading gets: the same for every block and
// every model, so two readings differ by the model and the wording only.
const readBackSystem = "You are reading part of your own instructions. Say back, in plain words and at most three sentences, what you understand it to mean for what you do. " +
	"Say what you take it to mean, not what it should say. If any part is unclear to you, or could be read two ways, name that part in one more sentence. " +
	"Do not quote it back, rewrite it, or praise it."

// readBackAsk is the question for block b with text: a prompt block, a tool's
// description, or one of its parameters'.
func readBackAsk(b PromptBlock, text string) string {
	rest, isTool := strings.CutPrefix(b.Key, prompts.ToolBlockPrefix)
	if !isTool {
		return "Your instructions include this text:\n\n" + text + "\n\nWhat does it tell you to do, and when?"
	}
	tool, param, isParam := strings.Cut(rest, ".")
	if isParam {
		return "One of the tools you are offered, " + tool + ", takes a parameter named " + param + ", described as:\n\n" + text +
			"\n\nWhat would you put in " + param + ", and when would you leave it out?"
	}
	return "One of the tools you are offered is named " + tool + ", described as:\n\n" + text +
		"\n\nWhat does this tool do, and when would you reach for it first rather than another tool?"
}

// readBackText is the wording tier would be sent for block b, given the text
// open in the editor under variant: the open text when it is that tier's
// version, or the shared version the tier reads for want of its own; else the
// tier's own saved wording. wording says which.
func readBackText(b PromptBlock, tier, variant, open string) (text, wording string) {
	_, own := prompts.PromptTierOverride(tier, b.Key)
	switch {
	case variant == tier:
		return open, "the open text"
	case (variant == variantAll || variant == "") && !own:
		return open, "the open text"
	}
	text, _ = variantText(b, tier)
	if own {
		return text, "its own saved wording"
	}
	return text, "the shared saved wording"
}

// handleReadBack is POST {id, body, variant, tier}: one model's reading of a
// block. One model per call, so each reading shows as soon as it is in, and
// closing the dialog cancels the call still running.
func (T *PromptsApp) handleReadBack(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID      string `json:"id"`
		Body    string `json:"body"`
		Variant string `json:"variant"`
		Tier    string `json:"tier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Tier != prompts.TierWorker && req.Tier != prompts.TierLead {
		writeJSON(w, map[string]any{"error": "tier is worker or lead"})
		return
	}
	b, ok := lookupBlock(req.ID)
	if !ok {
		writeJSON(w, map[string]any{"error": "no prompt block " + req.ID})
		return
	}
	out := map[string]any{"tier": req.Tier, "model": shortModel(tierModel(req.Tier))}
	if req.Tier == prompts.TierLead && !LeadIsDistinct() {
		out["skipped"] = "There is no separate lead model: the worker serves the lead's calls."
		writeJSON(w, out)
		return
	}
	text, wording := readBackText(b, req.Tier, req.Variant, req.Body)
	out["wording"] = wording
	if strings.TrimSpace(text) == "" {
		out["error"] = "The block is empty."
		writeJSON(w, out)
		return
	}
	if len(text) > readBackMaxText {
		text = text[:readBackMaxText]
	}
	msgs := []Message{
		{Role: "system", Content: readBackSystem},
		{Role: "user", Content: readBackAsk(b, text)},
	}
	chat := T.WorkerChat
	if req.Tier == prompts.TierLead {
		chat = T.LeadChat
	}
	resp, err := chat(r.Context(), msgs)
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, out)
		return
	}
	out["reading"] = strings.TrimSpace(resp.Content)
	writeJSON(w, out)
}
