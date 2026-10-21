package ui

import "encoding/json"

// AssistPanel is the ✨ assist beside a piece of work: the work on the left,
// a form usually, and a conversation with the model on the right that
// already knows what is being worked on. The model is told the form's
// fields and what they hold now; what it proposes fills them, as a version
// the person can walk back from (◀ ▶), and nothing is saved until the
// form's own Save. Put it in a ModalAction or a ModalButton for the window:
//
//	ui.ModalAction("✨ Fix", ui.AssistPanel{
//		Work: ui.FormPanel{Source: "api/thing?id={id}", PostURL: "api/thing", SubmitLabel: "Save", Fields: fields},
//		URL:  "api/thing/assist?id={id}",
//		Ask:  "Why does this fail, and what would fix it?",
//	})
//
// URL is an endpoint built with core/assist.Handler, which runs the worker
// with the app's tools and says what it is doing while it works. Its {field}
// placeholders fill from the row, as the Work's do, so the endpoint knows
// which record the window is about without trusting the browser to say.
type AssistPanel struct {
	Work Component `json:"-"` // the left side: a FormPanel, whose fields the model fills
	URL  string    `json:"url"`
	// Ask is an opening request, sent once the work has loaded, as though
	// the person had typed it: a window opened to fix something starts on
	// the fixing, rather than on an empty conversation.
	Ask string `json:"ask,omitempty"`
	// Web offers "Look things up on the web", off until the person turns it
	// on: a search sends what is searched for to an outside engine. The
	// endpoint must allow it too (assist.Spec.Web).
	Web bool `json:"web,omitempty"`
	// Attach offers a 📎 for files the endpoint's tools use (a sample to try
	// a conversion on): kept for the window, never shown to the model. The
	// endpoint must take them too (assist.Spec.Attach).
	Attach      bool   `json:"attach,omitempty"`
	Intro       string `json:"intro,omitempty"`       // the conversation's opening note
	Placeholder string `json:"placeholder,omitempty"` // the composer's
	WorkLabel   string `json:"work_label,omitempty"`  // its tab on a phone (default "Form")
	// Walk goes through a list of records in turn, the panel for each: the
	// Work's and URL's {field} placeholders fill from the record shown, and
	// saving the form moves on to the next. For "fix the ones that are
	// broken", one after another, without going back to the table.
	Walk *AssistWalk `json:"walk,omitempty"`
}

// AssistWalk is the list an AssistPanel walks.
type AssistWalk struct {
	Source string `json:"source"`          // GET: the records, as a Table's source answers
	Label  string `json:"label"`           // the field naming each record in "2 of 3: ..."
	Empty  string `json:"empty,omitempty"` // shown when there are none
}

func (AssistPanel) componentType() string { return "assist_panel" }

func (p AssistPanel) MarshalJSON() ([]byte, error) {
	type alias AssistPanel
	return json.Marshal(struct {
		Type string          `json:"type"`
		Work json.RawMessage `json:"work"`
		alias
	}{"assist_panel", marshalComponent(p.Work), alias(p)})
}

// AssistWidth is the dialog width an AssistPanel wants: both sides side by
// side, as wide as the screen allows.
const AssistWidth = "min(1180px, 96vw)"

// AssistAction is a row's ✨ button opening an AssistPanel in a dialog.
// The label gets its ✨, the mark every assist carries.
func AssistAction(label string, p AssistPanel) RowAction {
	a := ModalAction("✨ "+label, p)
	a.Width = AssistWidth
	return a
}
