package ui

import (
	"encoding/json"
)

// CodeEditorPanel is a two-pane code workbench: a snippet sidebar + a code
// editor (with optional Context block) on the left, and a dual-mode chat
// ("Chat" discusses, "Edit" proposes fenced code that's applied via inline
// diff) on the right. Supports {{NAME}} variable substitution, a saved-values
// library, and a saved-contexts library. Distinct enough from ArticleEditor /
// ChatPanel that it gets its own component type rather than overloading
// either. Every endpoint is host-supplied, so the panel names no app.
type CodeEditorPanel struct {
	// Snippet CRUD endpoints.
	ListURL   string `json:"list_url"`             // GET → array of snippets
	LoadURL   string `json:"load_url,omitempty"`   // GET {id} → snippet (defaults to "{list_url}/{id}" pattern when blank)
	SaveURL   string `json:"save_url"`             // POST snippet
	DeleteURL string `json:"delete_url,omitempty"` // DELETE {id}

	// Chat endpoint. POST {name, lang, code, context, message, mode, history}
	// → {response, code?} — code present only on mode="edit" successes.
	ChatURL string `json:"chat_url"`

	// Optional toolbar / library endpoints. Leave blank to hide.
	SuggestNameURL   string `json:"suggest_name_url,omitempty"`
	RevisionsListURL string `json:"revisions_list_url,omitempty"` // GET {id}
	RevisionLoadURL  string `json:"revision_load_url,omitempty"`  // GET {revid}
	ValuesListURL    string `json:"values_list_url,omitempty"`
	ValueURL         string `json:"value_url,omitempty"` // GET/PUT/DELETE {id}
	ContextsListURL  string `json:"contexts_list_url,omitempty"`
	ContextURL       string `json:"context_url,omitempty"` // GET/PUT/DELETE {id}

	// CollectionsListURL, when set, turns on the reference-collections
	// picker: a multi-select populated from this endpoint (GET → array
	// of {id, name}). The IDs the user checks are sent as a "collections"
	// array on every chat POST, so the handler can RAG-retrieve grounding
	// from those corpora. Domain-agnostic — collections are a framework
	// primitive (core.SearchCollections); leave blank to hide the picker.
	CollectionsListURL string `json:"collections_list_url,omitempty"`
	// CollectionsNoun is the user-facing label for the picker (the chip-bar
	// label, the "+ Add <noun>" button, the modal title). Host-supplied so
	// core/ui names no specific app; defaults to a generic label when blank.
	CollectionsNoun string `json:"collections_noun,omitempty"`
	// ReferenceSourcesURL, when set, renders the generic reference picker in
	// the chat header. GET → []core.ReferenceGroup
	// ({kind, label, items:[{id, name, desc}]}). The selected item rides
	// with each chat POST as `references` ([{kind, item_id}]); the app's
	// ChatURL handler injects that source's text into the model context.
	// Same wire shape as ArticleEditor's picker — see core.ReferenceSource.
	ReferenceSourcesURL string `json:"reference_sources_url,omitempty"`

	// ProfilesListURL, when set, renders the PROFILE BAR under the toolbar: the
	// roster of named configurations this surface can work under, one of which
	// is in force at a time and rides every chat POST as `profile`.
	//
	// A profile is a SNAPSHOT OF THIS PANEL'S SETTINGS — language, sources,
	// collections — saved under a name and restored by clicking it. Not an
	// entity with a life of its own: there is nothing to manage on another
	// page, because there is nothing in a profile that isn't already a control
	// on this panel.
	//
	// It exists because those controls reset to their defaults on reload, so a
	// recurring kind of work meant re-picking the same handful of settings at
	// the start of every session — and the cost of not bothering was a turn
	// that ran without the material it needed, silently.
	//
	// Its own bar spanning the panel, not a control inside either pane: the
	// settings it restores belong to the editor as much as the chat. A roster
	// rather than a dropdown because switching quickly is the point, and a
	// dropdown hides the set behind the one name it displays.
	//
	// GET → [{id, name, description}]. The distinction from the pickers above
	// is persistence and scope: collections and reference sources are per-turn
	// attachments a user ticks for one question, while a profile is the standing
	// answer to "which kind of writer am I talking to" — it survives reloads and
	// applies to everything written under it.
	//
	// Named PROFILE rather than after any host's noun, and labelled by
	// ProfilesNoun, so this component keeps naming no specific app. What a
	// profile MEANS is entirely the host's business: the panel selects one and
	// reports it, and never interprets it.
	ProfilesListURL string `json:"profiles_list_url,omitempty"`
	// ProfilesNoun is the user-facing label for the picker (defaults to
	// "Profile"). Same host-supplied-noun contract as CollectionsNoun.
	ProfilesNoun string `json:"profiles_noun,omitempty"`
	// ProfilesSaveURL, when set, adds "Save current" to the bar: it POSTs the
	// panel's CURRENT control state — {name, lang, collections, references} —
	// and the new profile joins the roster.
	//
	// Saving from the bar rather than editing a record on some other page is
	// the whole ergonomic point. A profile is a snapshot of settings you have
	// already dialled in by working, so the moment you want to keep them is the
	// moment you are looking at them; sending someone to a form to re-describe
	// what the panel is already set to is asking them to enter the same
	// information twice, from memory.
	ProfilesSaveURL string `json:"profiles_save_url,omitempty"`
	// ProfilesDeleteURL, when set, puts a × on the selected profile's pill.
	// {id} is replaced with the profile's id.
	ProfilesDeleteURL string `json:"profiles_delete_url,omitempty"`

	// Field name mapping — defaults match SnippetRecord.
	IDField   string `json:"id_field,omitempty"`   // default "id"
	NameField string `json:"name_field,omitempty"` // default "name"
	LangField string `json:"lang_field,omitempty"` // default "lang"
	CodeField string `json:"code_field,omitempty"` // default "code"
	DateField string `json:"date_field,omitempty"` // default "date"

	// Languages populates the lang dropdown. Leave nil to use defaults
	// (bash, sql, python, powershell, go, markdown, regex, other).
	Languages []string `json:"languages,omitempty"`

	// Templates are starting skeletons offered when the language is
	// markdown, via a "Templates" button next to the outline toggle.
	// Picking one replaces the editor (confirmed first when the buffer
	// isn't empty). Leave nil to hide the button.
	//
	// These are the BUILT-IN skeletons: the handful of shapes people
	// write over and over and shouldn't have to remember the headings
	// for. They can't be deleted. Users add their own via the endpoints
	// below, and the picker shows both lists together.
	Templates []DocTemplate `json:"templates,omitempty"`

	// User-saved templates. Set all three to let the picker save the
	// current document as a reusable template, list what's been saved,
	// and delete one. Leave blank for built-ins only.
	//
	// TemplatesListURL: GET → [{id, name, description, body, date}],
	// POST {name, description, body} to create. Re-saving under an
	// existing name replaces it rather than accumulating near-duplicates.
	// TemplateURL: GET/DELETE {id}.
	TemplatesListURL string `json:"templates_list_url,omitempty"`
	TemplateURL      string `json:"template_url,omitempty"`

	// RulesURL turns on the "Rules" editor: per-user standing
	// instructions the assistant must follow on every call in this app.
	// GET → {rules}; POST {rules}. Namespaced per app server-side, since
	// the constraints that fit prose don't fit SQL.
	RulesURL string `json:"rules_url,omitempty"`

	// AssistURL turns on the draft-with-me workbench for markdown
	// documents: the text beside a conversation, with a walk back
	// through earlier versions. A ✨ appears on each section in the
	// outline, and a whole-document one in raw view.
	//
	// POST {name, section, message, draft, context, history} →
	// {reply, value}. section is "" for the whole document. value empty
	// means the model answered without proposing a change. Same contract
	// as the agent editor's suggest endpoint, so both drive the one
	// workbench. Leave blank to hide the affordance.
	AssistURL string `json:"assist_url,omitempty"`

	// Empty-state copy + placeholder text.
	EmptyText       string `json:"empty_text,omitempty"`
	PlaceholderName string `json:"placeholder_name,omitempty"`
	PlaceholderCode string `json:"placeholder_code,omitempty"`
	PlaceholderCtx  string `json:"placeholder_ctx,omitempty"`
	PlaceholderChat string `json:"placeholder_chat,omitempty"`
}

// DocTemplate is one starting skeleton in CodeEditorPanel.Templates.
// Body is the document the editor is filled with; Name doubles as the
// snippet name when the user hasn't typed one yet.
//
// Write the Body as a real outline with headings, not prose about what
// to write: it lands in the sections editor as navigable blocks, and a
// heading the author can fill beats a paragraph telling them to.
type DocTemplate struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Body        string `json:"body"`
}

func (CodeEditorPanel) componentType() string { return "code_editor_panel" }

func (c CodeEditorPanel) MarshalJSON() ([]byte, error) {
	type alias CodeEditorPanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"code_editor_panel", alias(c)})
}
