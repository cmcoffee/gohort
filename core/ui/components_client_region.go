package ui

import "encoding/json"

// ClientRegion is a region of the page the APP fills itself.
//
// The framework owns the chrome: the section, its title, its place in a tab.
// Everything inside the region is the app's. That is what lets a surface an
// app already has live inside a page rather than only inside a modal of its
// own, without the framework learning anything about what it draws.
//
// Action names a handler registered through window.uiRegisterClientAction,
// the same seam row actions and view actions use. It receives
// {host, args, ctx}: host is the element, already in the document, and args
// is whatever the page put in Args.
//
// Use it for a surface too large or too stateful to express as fields and
// tables, where the alternative is a modal the page cannot host. A control
// that CAN be a FormPanel should be one: this bypasses everything the
// framework knows about forms, including how they save.
//
// It is domain-agnostic by construction. A region naming a handler is a
// generic idea; what the handler draws is the app's business and stays in the
// app's package.
type ClientRegion struct {
	Action string         `json:"action"`
	Args   map[string]any `json:"args,omitempty"`
}

func (ClientRegion) componentType() string { return "client_region" }

func (c ClientRegion) MarshalJSON() ([]byte, error) {
	type alias ClientRegion
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"client_region", alias(c)})
}
