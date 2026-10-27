// Describe a CHANGE to a skill, in plain words, the way a machine or a
// pipeline already takes one.
//
// The skill form edits four fields by hand, and the one that matters,
// the instructions, is a block of prose. "Also cover refunds over $500,
// and ask for the order number first" is a sentence somebody can say; it is
// not an edit they should have to find the place for. The worker is handed
// the skill as it stands and asked to return it with that change made and
// nothing else touched.
//
// Only the fields the form owns are revised: name, description, triggers,
// instructions. Tools, collections, the playbook and the shares are left
// exactly as they were. The save goes through the ordinary path, so the
// version it replaced is in the skill's Version history and can be put back.

package extensions

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// skillRevisable is the part of a skill a description can change.
type skillRevisable struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Triggers     []string `json:"triggers"`
	Instructions string   `json:"instructions"`
}

const maxSkillChange = 2000

const skillReviseSystem = "You revise a skill: standing instructions an assistant applies when its triggers or description match a message. " +
	"You are given the skill as JSON and a change to make. Return the WHOLE skill as JSON with exactly these keys: " +
	"name, description, triggers (a list of short phrases), instructions. " +
	"Make the change asked for and nothing else: every word the change does not touch stays exactly as it was. " +
	"Keep the name unless the change is about the name. Reply with ONLY the JSON object."

// skillReviseBody is the change to make, in plain words.
type skillReviseBody struct {
	Description string `json:"description"`
}

// handleSkillRevise is POST /api/skills/{id}/revise {"description": "..."}
// -> {"changed": [fields]}, on one of the caller's own skills.
func (T *Extensions) handleSkillRevise(c *jsonapi.Call, body skillReviseBody) (any, error) {
	own, found := findOwnSkill(c.User, c.R.PathValue("id"))
	if !found {
		return nil, jsonapi.NotFound("no such skill")
	}
	want := strings.TrimSpace(body.Description)
	if want == "" {
		return nil, jsonapi.BadRequest("say what should change")
	}
	if len(want) > maxSkillChange {
		want = want[:maxSkillChange]
	}
	before := skillRevisable{Name: own.Name, Description: own.Description, Triggers: own.Triggers, Instructions: own.Instructions}
	current, _ := json.MarshalIndent(before, "", "  ")
	ask := "WHAT SHOULD CHANGE:\n" + want + "\n\nTHE SKILL AS IT STANDS:\n" + string(current)

	revised, err := T.reviseSkillOnce(c.R, ask)
	if err != nil {
		// One retry that names what was wrong, as the machine reviser does.
		if revised, err = T.reviseSkillOnce(c.R, ask+"\n\nYour previous reply could not be used: "+err.Error()+"\nReply with ONLY the JSON object."); err != nil {
			return nil, jsonapi.Status(http.StatusBadGateway, "the change could not be made: "+err.Error())
		}
	}
	name := strings.TrimSpace(revised.Name)
	if !strings.EqualFold(name, strings.TrimSpace(own.Name)) {
		if other, taken := ownSkillNamed(own.user, name, own.ID); taken {
			return nil, jsonapi.Status(http.StatusConflict, "the change would rename it to "+other.Name+", which you already have")
		}
	}
	var changed []string
	if name != own.Name {
		changed = append(changed, "name")
	}
	if strings.TrimSpace(revised.Description) != strings.TrimSpace(own.Description) {
		changed = append(changed, "description")
	}
	if strings.Join(cleanTriggers(revised.Triggers), "\n") != strings.Join(own.Triggers, "\n") {
		changed = append(changed, "triggers")
	}
	if strings.TrimSpace(revised.Instructions) != strings.TrimSpace(own.Instructions) {
		changed = append(changed, "instructions")
	}
	if len(changed) == 0 {
		return map[string]any{"changed": []string{}, "message": "Nothing needed to change."}, nil
	}
	rec := own.SkillRecord
	rec.Name, rec.Description = name, strings.TrimSpace(revised.Description)
	rec.Triggers, rec.Instructions = cleanTriggers(revised.Triggers), revised.Instructions
	if _, err := SaveSkillAs(AuthDB(), own.user, rec, "revised from a description: "+truncateWords(want, 12)); err != nil {
		// The store's refusal is about the skill (a name, a size), so it is
		// the person's to read.
		return nil, jsonapi.Status(http.StatusInternalServerError, err.Error())
	}
	return map[string]any{"changed": changed,
		"message": "Changed " + strings.Join(changed, ", ") + ". The version before is in Version history."}, nil
}

// reviseSkillOnce asks the worker once and reads its reply.
func (T *Extensions) reviseSkillOnce(r *http.Request, ask string) (skillRevisable, error) {
	resp, err := T.WorkerChat(r.Context(), []Message{{Role: "user", Content: ask}},
		WithSystemPrompt(skillReviseSystem), WithJSONMode(), WithMaxTokens(4096), WithThink(false))
	if err != nil {
		return skillRevisable{}, err
	}
	if resp == nil {
		return skillRevisable{}, Error("no reply")
	}
	text := strings.TrimSpace(resp.Content)
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		text = text[i : j+1]
	}
	var out skillRevisable
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return skillRevisable{}, Error("not a JSON skill: " + err.Error())
	}
	if strings.TrimSpace(out.Name) == "" {
		return skillRevisable{}, Error("the revised skill has no name")
	}
	if strings.TrimSpace(out.Instructions) == "" {
		return skillRevisable{}, Error("the revised skill has no instructions")
	}
	return out, nil
}

// cleanTriggers trims and drops empty trigger phrases.
func cleanTriggers(in []string) []string {
	var out []string
	for _, t := range in {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// truncateWords keeps the first n words, for a history note.
func truncateWords(s string, n int) string {
	w := strings.Fields(s)
	if len(w) <= n {
		return strings.Join(w, " ")
	}
	return strings.Join(w[:n], " ") + "…"
}
