package admin

import (
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// sourceHooksSections is the source hooks part of the admin page: Source Hooks.
func (a *AdminApp) sourceHooksSections() []ui.Section {
	return []ui.Section{
		{
			Title:    "Source Hooks",
			Subtitle: "Curated external sources (PubMed, OpenAlex, EDGAR, custom API/RAG endpoints). Flip \"Expose to LLM\" and the hook becomes a per-hook agent tool (e.g. pubmed_search) any orchestrate agent can call directly; otherwise it's reachable only by the research/debate pipelines via topic routing.",
			Body: ui.Stack{
				Children: []ui.Component{
					ui.Table{
						Source: "api/source-hooks",
						RowKey: "name",
						Columns: []ui.Col{
							{Field: "name", Flex: 1},
							{Field: "type", Mute: true},
							{Field: "effective_tool", Label: "Tool", Mute: true, Flex: 1},
							{
								Field: "expose_to_llm", Type: "badge", Label: "LLM",
								Badges: []ui.BadgeMapping{
									{Value: true, Label: "Exposed", Color: "success"},
									{Value: false, Label: "Hidden", Color: "mute"},
								},
							},
							{
								Field: "has_auth", Type: "badge", Label: "Auth",
								Badges: []ui.BadgeMapping{
									{Value: true, Label: "Key set", Color: "success"},
									{Value: false, Label: "None", Color: "mute"},
								},
							},
							{
								Field: "disabled", Type: "badge", Label: "Status",
								Badges: []ui.BadgeMapping{
									{Value: true, Label: "Disabled", Color: "warning"},
									{Value: false, Label: "Active", Color: "success"},
								},
							},
						},
						RowActions: []ui.RowAction{
							ui.Expand("Edit", ui.FormPanel{
								Source:      "api/source-hooks?name={name}",
								PostURL:     "api/source-hooks",
								Invalidate:  costSources,
								SubmitLabel: "Save changes",
								Templates:   sourceHookFormTemplates(),
								Fields:      sourceHookFormFields(),
							}),
							{Type: "button", Label: "Enable",
								PostTo: "api/source-hooks?action=enable&name={name}",
								Method: "POST", OnlyIf: "disabled", Variant: "success",
								Confirm: "Enable this source hook? Once active it receives search queries at its endpoint (topic routing, LLM tools) — review the endpoint and mappings first, and add its auth key if it needs one."},
							{Type: "button", Label: "Disable",
								PostTo: "api/source-hooks?action=disable&name={name}",
								Method: "POST", HideIf: "disabled", Variant: "warning"},
							{Type: "button", Label: "Expose to LLM",
								PostTo: "api/source-hooks?action=expose&name={name}",
								Method: "POST", HideIf: "expose_to_llm", Variant: "success"},
							{Type: "button", Label: "Hide from LLM",
								PostTo:  "api/source-hooks?action=hide&name={name}",
								Method:  "POST",
								OnlyIf:  "expose_to_llm",
								Variant: "warning"},
							{Type: "button", Label: "Delete",
								PostTo:  "api/source-hooks?name={name}",
								Method:  "DELETE",
								Confirm: "Delete this source hook? Its encrypted auth key goes with it.",
								Variant: "danger"},
						},
						EmptyText: "No source hooks configured. Add one with the button below.",
					},
					// Add lives in the same card, below the listed sources;
					// pops the create form in a modal. Edit an existing hook
					// from its row (leave the auth key blank to keep the secret).
					ui.ModalButton{
						Label:    "Add source",
						Title:    "Add source hook",
						Subtitle: "For API/RAG hooks the field mappings tell the adapter how to read the endpoint's JSON.",
						Variant:  "primary",
						Width:    "640px",
						Body: ui.FormPanel{
							PostURL:     "api/source-hooks",
							Invalidate:  costSources,
							SubmitLabel: "Create source hook",
							Templates:   sourceHookFormTemplates(),
							Fields:      sourceHookFormFields(),
						},
					},
				},
			},
		},
	}
}

func sourceHookFormFields() []ui.FormField {
	return []ui.FormField{
		{Field: "ident", Type: "header", Label: "Identity"},
		{Field: "name", Label: "Name", Placeholder: "e.g. PubMed", Help: "Display name and unique key — re-using a name updates that hook."},
		{Field: "type", Label: "Type", Type: "select", Options: []ui.SelectOption{
			{Value: "api", Label: "API — search endpoint returning results"},
			{Value: "rag", Label: "RAG — endpoint returning document chunks"},
			{Value: "paywall", Label: "Paywall — adds auth headers to fetch_url (not a search tool)"},
		}},
		{Field: "endpoint", Label: "Endpoint URL", Placeholder: "https://api.example.com/search", Help: "Base URL for API/RAG. Leave blank for paywall hooks."},
		{Field: "auth", Type: "header", Label: "Authentication"},
		{Field: "auth_type", Label: "Auth type", Type: "select", Options: []ui.SelectOption{
			{Value: "none", Label: "None"},
			{Value: "api_key", Label: "API key"},
			{Value: "bearer", Label: "Bearer token"},
		}},
		{Field: "auth_key", Label: "Auth key / token", Type: "password", Help: "Stored encrypted. Leave blank to keep an existing secret unchanged."},
		{Field: "mapping", Type: "header", Label: "Result mapping (API / RAG)", Help: "How to read the endpoint's JSON response."},
		{Field: "query_param", Label: "Query param", Placeholder: "q / query / term", Help: "Name of the query parameter the endpoint expects."},
		{Field: "results_path", Label: "Results path", Placeholder: "results / data.items", Help: "Dotted JSON path to the results array."},
		{Field: "title_field", Label: "Title field", Placeholder: "title"},
		{Field: "url_field", Label: "URL field", Placeholder: "url"},
		{Field: "snippet_field", Label: "Snippet field", Placeholder: "snippet"},
		{Field: "content_field", Label: "Content field (RAG)", Placeholder: "content"},
		{Field: "activation", Type: "header", Label: "Activation"},
		{Field: "trigger_domains", Label: "Trigger domains", Type: "tags", Help: "Topic domains that activate this hook in research/debate (e.g. legal, medical)."},
		{Field: "always_active", Label: "Always active", Type: "toggle", Help: "Query this hook for every topic, regardless of trigger domains."},
		{Field: "domains", Label: "Paywall domains", Type: "tags", Help: "(paywall only) domains this hook attaches auth to, e.g. wsj.com."},
		{Field: "max_rps", Label: "Max requests/sec", Type: "number", Min: 0, Max: 100, Help: "0 = unlimited."},
		{Field: "cost_per_call", Label: "Cost per call ($)", Type: "number", Decimals: 6, Min: 0, Help: "Optional. Dollar cost of one real (cache-miss) call to this hook, for the Costs tab chart + per-source breakdown. 0 = untracked (free endpoint)."},
		{Field: "llm", Type: "header", Label: "LLM exposure"},
		{Field: "expose_to_llm", Label: "Expose as an agent tool", Type: "toggle", Help: "When on, agents can call this hook directly as a named tool. Paywall hooks are never exposed."},
		{Field: "tool_name", Label: "Tool name", Placeholder: "pubmed_search", Help: "Lowercase, underscores. Defaults to \"<name>_search\" when blank."},
		{Field: "tool_description", Label: "Tool description", Type: "textarea", Rows: 3, Help: "Tells the model when to choose this over web_search."},
	}
}

func sourceHookFormTemplates() []ui.FormTemplate {
	tpls := SourceHookTemplates()
	out := make([]ui.FormTemplate, 0, len(tpls))
	for _, t := range tpls {
		h := t.Hook
		label := h.Name
		if t.Description != "" {
			label += " — " + t.Description
		}
		vals := map[string]any{
			"name":          h.Name,
			"type":          string(h.Type),
			"endpoint":      h.Endpoint,
			"auth_type":     string(h.AuthType),
			"query_param":   h.QueryParam,
			"results_path":  h.ResultsPath,
			"title_field":   h.TitleField,
			"url_field":     h.URLField,
			"snippet_field": h.SnippetField,
		}
		if h.ContentField != "" {
			vals["content_field"] = h.ContentField
		}
		if len(h.TriggerDomains) > 0 {
			vals["trigger_domains"] = h.TriggerDomains
		}
		if len(h.Domains) > 0 {
			vals["domains"] = h.Domains
		}
		out = append(out, ui.FormTemplate{Label: label, Values: vals})
	}
	return out
}
