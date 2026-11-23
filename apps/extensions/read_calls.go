package extensions

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

// "Calls that only read", on a credential's Security panel: the calls through
// this key that only read although they are sent as a write, such as Jira's
// search sent as a POST (SecureCredential.ReadCalls). A person this key is
// lent to for reads may make them with it. The owner's list, and the owner's
// alone: a mark on somebody else's tool never reaches it.

// readCallsPanel is the form, one call per line.
func readCallsPanel() ui.Component {
	return ui.FormPanel{
		Source:      "api/credentials?name={name}&view=read_calls",
		PostURL:     "api/credentials?action=read_calls&name={name}",
		SubmitLabel: "Save the calls that only read",
		Fields: []ui.FormField{
			{Field: "read_calls", Type: "textarea", Label: "Calls that only read",
				Placeholder: "POST /rest/api/3/search/jql",
				Help:        "One per line: the method and the path. GET, HEAD and OPTIONS read already; list a call sent as a write that only reads.",
				Detail: "A person you lend this key to for reads can make these calls with it, as well as every GET. In a path, * stands for one part (POST /rest/api/3/issue/*/transitions would cover every issue) and ** at the end for everything under it.\n\n" +
					"Marking an action of one of your own toolboxes on this key as one that only reads adds it here for you, and so does Builder when it maps the API."},
		},
		Invalidate: []string{"api/credentials"},
	}
}

// readCallsView is the form's record.
func readCallsView(c SecureCredential) map[string]string {
	return map[string]string{"read_calls": strings.Join(c.ReadCalls, "\n")}
}

// handleReadCallsSave replaces the list from the form's lines.
func (T *Extensions) handleReadCallsSave(c *jsonapi.Call, body struct {
	ReadCalls string `json:"read_calls"`
}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("missing name")
	}
	var calls []string
	for _, line := range strings.Split(body.ReadCalls, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			calls = append(calls, line)
		}
	}
	if err := Secure().SetReadCalls(c.User, name, calls); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}
