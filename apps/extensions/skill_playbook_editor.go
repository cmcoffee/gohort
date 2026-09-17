// A playbook editor made of questions instead of JSON — the same argument
// the machine editor makes for itself, at a smaller scale. A rule is
// "establish Y; if yes Z, if no U", and a textarea of JSON hides that
// sentence behind braces; the form asks each part at the point of choice
// and reads the sentence back so the author can see the fields mean what
// they think.
//
// One form per rule, each posting to its own endpoint and MERGED into the
// rule, so a form that holds six fields cannot take the other rules with
// it. The page is rebuilt from the record on every load, which is what
// lets a choice rule grow one arm per value: the values field reloads the
// page, and the page then has a textarea for each value that exists.
//
// A rule is saved half-built on purpose. Storage stores; the checklist under
// each rule says what is still missing, and the resolver skips a rule with
// problems so an unfinished one never runs. The JSON textarea on the admin
// skill form stays: it is what the Builder writes and what an export carries.
//
// It lives HERE, not in admin, because a skill is the calling user's own
// record — admin edits everyone's, this edits yours, and a user with no admin
// rights could otherwise only author a playbook by asking Builder.

package extensions

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// handleSkillPlaybookPage serves /extensions/skill-playbook?id=<skill>.
func (T *Extensions) handleSkillPlaybookPage(w http.ResponseWriter, r *http.Request) {
	username, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// Scoped to the caller's own pool by the lookup itself: a skill id that
	// is not theirs is not found, which is the same answer as one that does
	// not exist and tells a prober nothing either way.
	skill, found := findSkill(AuthDB(), username, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		http.NotFound(w, r)
		return
	}
	skillPlaybookPage(skill).ServeHTTP(w, r)
}

func findSkill(db Database, username, id string) (SkillRecord, bool) {
	for _, s := range LoadSkills(db, username) {
		if s.ID == id {
			return s, true
		}
	}
	return SkillRecord{}, false
}

// skillPlaybookPage builds the page: one section per rule with its form,
// then a section that adds one. Split from the handler so the wiring is
// assertable without a server.
func skillPlaybookPage(skill SkillRecord) ui.Page {
	base := "api/skill-playbook?id=" + url.QueryEscape(skill.ID) + "&rule="
	var sections []ui.Section
	for i, rule := range skill.Playbook {
		probs := rule.Problems("rule "+strconv.Itoa(i+1), 1)
		sub := rule.Sentence()
		if len(probs) > 0 {
			sub = "Still missing: " + strings.Join(trimRulePrefix(probs), "; ") + ". Until it is complete this rule is skipped."
		}
		sections = append(sections, ui.Section{
			Title:    ruleTitle(i, rule),
			Subtitle: sub,
			Body:     rulePanel(base+strconv.Itoa(i), rule),
		})
	}
	sections = append(sections, ui.Section{
		Title:    "Add a rule",
		Subtitle: "A rule is one condition: establish a fact, then do one thing if it holds and another if it does not. The prose that does not branch belongs in the skill's instructions.",
		Body: ui.FormPanel{
			Source:         base + "new",
			PostURL:        base + "add",
			Method:         "POST",
			SubmitLabel:    "Add rule",
			RedirectURL:    "skill-playbook?id=" + url.QueryEscape(skill.ID),
			RedirectTarget: "_self",
			Fields: []ui.FormField{
				{Field: "fact", Type: "text", Label: "What must be established first?", Placeholder: "queue_draining",
					Help: "One word, no spaces — it is the name of a field the check fills in."},
				{Field: "how", Type: "textarea", Rows: 3, Label: "How is it established?", Placeholder: "Read the consumer lag for the orders queue over the last five minutes.",
					Help: "Written to whoever runs the check, with the skill's tools. Say what to look at and what counts."},
				{Field: "then", Type: "textarea", Rows: 2, Label: "If yes, then…", Placeholder: "Look at the consumer: its log, restart count, lag trend."},
				{Field: "else", Type: "textarea", Rows: 2, Label: "If no, then…", Placeholder: "Look at the broker: connectivity from the consumer host, partition state, disk."},
			},
		},
	})
	return ui.Page{
		Title:      skill.Name + " — playbook",
		ShowTitle:  true,
		BackURL:    "/extensions",
		Nav:        HubNav("/extensions"),
		SectionNav: true,
		MaxWidth:   "900px",
		Sections:   sections,
	}
}

func ruleTitle(i int, r PlaybookRule) string {
	fact := strings.TrimSpace(r.Fact)
	if fact == "" {
		fact = "(unnamed)"
	}
	return fmt.Sprintf("Rule %d: %s", i+1, fact)
}

// trimRulePrefix drops the "rule N: " each problem carries, since the
// section it sits under already says which rule.
func trimRulePrefix(probs []string) []string {
	out := make([]string, 0, len(probs))
	for _, p := range probs {
		if i := strings.Index(p, ": "); i >= 0 && strings.HasPrefix(p, "rule ") {
			p = p[i+2:]
		}
		out = append(out, p)
	}
	return out
}

// rulePanel is the form for one rule. Fields are gated on the rule's own
// answers: a choice rule shows its values and one arm per value, a bool rule
// shows its two arms, and each bool arm can turn into a nested rule, whose
// fields appear in place. Nesting below that is the JSON door's.
func rulePanel(endpoint string, rule PlaybookRule) ui.FormPanel {
	fields := []ui.FormField{
		{Field: "sentence", Type: "readonly", Label: "Reads as"},
		{Field: "when", Type: "tags", Label: "Applies when the message mentions (optional)",
			Help: "Matched like the skill's triggers: case-insensitive substrings of the message. Empty means every time the skill is consulted."},
		{Field: "fact", Type: "text", Label: "What must be established first?",
			Help: "One word, no spaces — the name of the field the check fills in."},
		{Field: "how", Type: "textarea", Rows: 3, Label: "How is it established?",
			Help: "Written to whoever runs the check, with the skill's tools. Say what to look at and what counts."},
		{Field: "type", Type: "select", Label: "The answer is", ReloadOnChange: true,
			Options: []ui.SelectOption{
				{Value: "bool", Label: "yes or no"},
				{Value: "choice", Label: "one of several values"},
			}},
		{Field: "values", Type: "tags", Label: "The possible values", ShowWhen: "type:choice", ReloadOnChange: true,
			Help: "At least two. An arm appears below for each once saved."},
	}
	if rule.Type == "choice" || strings.EqualFold(rule.Type, "choice") {
		for i, v := range rule.Values {
			fields = append(fields, ui.FormField{Field: "case_" + strconv.Itoa(i), Type: "textarea", Rows: 2,
				Label: "If " + v + ", then…", ShowWhen: "type:choice"})
		}
	} else {
		fields = append(fields,
			ui.FormField{Field: "then_head", Type: "header", Label: "If yes", ShowWhen: "type:bool"},
			ui.FormField{Field: "then_is_rule", Type: "toggle", Label: "Establish another fact first instead", ShowWhen: "type:bool",
				Help: "Turns this arm into a nested rule with its own check and its own two arms. One level only."},
			ui.FormField{Field: "then", Type: "textarea", Rows: 2, Label: "Then…", ShowWhen: "type:bool;!then_is_rule"},
			ui.FormField{Field: "then_fact", Type: "text", Label: "Nested: what must be established?", ShowWhen: "type:bool;then_is_rule"},
			ui.FormField{Field: "then_how", Type: "textarea", Rows: 2, Label: "Nested: how?", ShowWhen: "type:bool;then_is_rule"},
			ui.FormField{Field: "then_then", Type: "textarea", Rows: 2, Label: "Nested: if yes, then…", ShowWhen: "type:bool;then_is_rule"},
			ui.FormField{Field: "then_else", Type: "textarea", Rows: 2, Label: "Nested: if no, then…", ShowWhen: "type:bool;then_is_rule"},
			ui.FormField{Field: "else_head", Type: "header", Label: "If no", ShowWhen: "type:bool"},
			ui.FormField{Field: "else_is_rule", Type: "toggle", Label: "Establish another fact first instead", ShowWhen: "type:bool",
				Help: "Turns this arm into a nested rule with its own check and its own two arms. One level only."},
			ui.FormField{Field: "else", Type: "textarea", Rows: 2, Label: "Then…", ShowWhen: "type:bool;!else_is_rule"},
			ui.FormField{Field: "else_fact", Type: "text", Label: "Nested: what must be established?", ShowWhen: "type:bool;else_is_rule"},
			ui.FormField{Field: "else_how", Type: "textarea", Rows: 2, Label: "Nested: how?", ShowWhen: "type:bool;else_is_rule"},
			ui.FormField{Field: "else_then", Type: "textarea", Rows: 2, Label: "Nested: if yes, then…", ShowWhen: "type:bool;else_is_rule"},
			ui.FormField{Field: "else_else", Type: "textarea", Rows: 2, Label: "Nested: if no, then…", ShowWhen: "type:bool;else_is_rule"},
		)
	}
	fields = append(fields,
		ui.FormField{Field: "remove_head", Type: "header", Label: "Remove", Collapsed: true},
		ui.FormField{Field: "remove", Type: "toggle", Label: "Remove this rule", ReloadOnChange: true,
			Help: "Saving with this on deletes the rule. There is no undo beyond adding it again."},
	)
	return ui.FormPanel{Source: endpoint, PostURL: endpoint, Method: "POST", Fields: fields}
}

// ruleForm flattens a rule into the form's values.
func ruleForm(rule PlaybookRule) map[string]any {
	kind := "bool"
	if strings.EqualFold(strings.TrimSpace(rule.Type), "choice") {
		kind = "choice"
	}
	out := map[string]any{
		"sentence": rule.Sentence(),
		"when":     nonNil(rule.When),
		"fact":     rule.Fact,
		"how":      rule.How,
		"type":     kind,
		"values":   nonNil(rule.Values),
		"remove":   false,
	}
	if kind == "choice" {
		for i, v := range rule.Values {
			out["case_"+strconv.Itoa(i)] = rule.Cases[v]
		}
		return out
	}
	arm := func(prefix, text string, nested *PlaybookRule) {
		out[prefix] = text
		out[prefix+"_is_rule"] = nested != nil
		if nested != nil {
			out[prefix+"_fact"], out[prefix+"_how"], out[prefix+"_then"], out[prefix+"_else"] = nested.Fact, nested.How, nested.Then, nested.Else
		}
	}
	arm("then", rule.Then, rule.ThenRule)
	arm("else", rule.Else, rule.ElseRule)
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// mergeRuleForm applies the fields a form posted onto the rule. Only keys
// present in the body change, so a form holding six fields leaves the rest
// alone; a nested arm is rebuilt from its four fields whenever its toggle is
// on and dropped whenever it is off.
func mergeRuleForm(rule PlaybookRule, body map[string]any) PlaybookRule {
	str := func(k string) (string, bool) {
		v, ok := body[k]
		if !ok {
			return "", false
		}
		return strings.TrimSpace(fmt.Sprint(v)), true
	}
	list := func(k string) ([]string, bool) {
		v, ok := body[k]
		if !ok {
			return nil, false
		}
		var out []string
		switch t := v.(type) {
		case []string:
			for _, x := range t {
				if x = strings.TrimSpace(x); x != "" {
					out = append(out, x)
				}
			}
		case []any:
			for _, x := range t {
				if s := strings.TrimSpace(fmt.Sprint(x)); s != "" {
					out = append(out, s)
				}
			}
		case string:
			var arr []string
			if json.Unmarshal([]byte(t), &arr) == nil {
				for _, x := range arr {
					if x = strings.TrimSpace(x); x != "" {
						out = append(out, x)
					}
				}
			} else if t = strings.TrimSpace(t); t != "" {
				out = append(out, t)
			}
		}
		return out, true
	}
	if v, ok := list("when"); ok {
		rule.When = v
	}
	if v, ok := str("fact"); ok {
		rule.Fact = v
	}
	if v, ok := str("how"); ok {
		rule.How = v
	}
	if v, ok := str("type"); ok {
		rule.Type = v
		if v == "choice" {
			rule.Then, rule.Else, rule.ThenRule, rule.ElseRule = "", "", nil, nil
		} else {
			rule.Values, rule.Cases, rule.CaseRules = nil, nil, nil
		}
	}
	if strings.EqualFold(rule.Type, "choice") {
		if v, ok := list("values"); ok {
			rule.Values = v
		}
		cases := map[string]string{}
		for i, val := range rule.Values {
			if text, ok := str("case_" + strconv.Itoa(i)); ok {
				if text != "" {
					cases[val] = text
				}
			} else if prior, had := rule.Cases[val]; had {
				cases[val] = prior
			}
		}
		if len(cases) > 0 {
			rule.Cases = cases
		} else {
			rule.Cases = nil
		}
		return rule
	}
	arm := func(prefix string, text *string, nested **PlaybookRule) {
		if v, ok := body[prefix+"_is_rule"]; ok && BoolArg(map[string]any{"v": v}, "v") {
			n := &PlaybookRule{}
			if *nested != nil {
				cp := **nested
				n = &cp
			}
			if v, ok := str(prefix + "_fact"); ok {
				n.Fact = v
			}
			if v, ok := str(prefix + "_how"); ok {
				n.How = v
			}
			if v, ok := str(prefix + "_then"); ok {
				n.Then = v
			}
			if v, ok := str(prefix + "_else"); ok {
				n.Else = v
			}
			*nested, *text = n, ""
			return
		}
		if _, ok := body[prefix+"_is_rule"]; ok {
			*nested = nil
		}
		if v, ok := str(prefix); ok {
			*text = v
		}
	}
	arm("then", &rule.Then, &rule.ThenRule)
	arm("else", &rule.Else, &rule.ElseRule)
	return rule
}

// handleSkillPlaybookRule serves one rule's form: GET reads it, POST merges
// and saves; "new" reads an empty form and "add" appends a rule.
func (T *Extensions) handleSkillPlaybookRule(w http.ResponseWriter, r *http.Request) {
	username, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	skillID := strings.TrimSpace(r.URL.Query().Get("id"))
	rest := strings.Trim(strings.TrimSpace(r.URL.Query().Get("rule")), "/")
	skill, found := findSkill(AuthDB(), username, skillID)
	if !found {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case rest == "new" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]any{"fact": "", "how": "", "then": "", "else": ""})
		return
	case rest == "add" && r.Method == http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		skill.Playbook = append(skill.Playbook, mergeRuleForm(PlaybookRule{}, body))
		if _, err := SaveSkill(AuthDB(), username, skill); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "rules": len(skill.Playbook)})
		return
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 || n >= len(skill.Playbook) {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(ruleForm(skill.Playbook[n]))
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if BoolArg(body, "remove") {
			skill.Playbook = append(skill.Playbook[:n], skill.Playbook[n+1:]...)
		} else {
			skill.Playbook[n] = mergeRuleForm(skill.Playbook[n], body)
		}
		saved, err := SaveSkill(AuthDB(), username, skill)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := map[string]any{"ok": true, "rules": len(saved.Playbook)}
		if n < len(saved.Playbook) {
			out["problems"] = saved.Playbook[n].Problems("rule "+strconv.Itoa(n+1), 1)
			out["sentence"] = saved.Playbook[n].Sentence()
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
