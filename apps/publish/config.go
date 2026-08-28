// Deployment configuration for the publish destinations, plus the admin section
// that edits it.
//
// One instance per destination kind, on purpose. A deployment with two
// Confluence sites is a real thing, but it is the case the declarative
// publish_http connector kind is for — inventing an instance-naming scheme here
// would build half of that badly. Until then: one Confluence, one webhook, both
// pointed at an already-governed SecureAPI credential.
package publish

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/gohort/core/ui"
)

const (
	publishConfigTable = "publish_config"
	publishConfigKey   = "config"
)

// PublishConfig is the deployment's destination wiring. Every field names an
// already-governed resource (a SecureAPI credential) rather than carrying a
// secret — the credential holds the secret, this holds its name.
type PublishConfig struct {
	// Confluence.
	ConfluenceCredential string `json:"confluence_credential,omitempty"`
	// ConfluenceBaseURL overrides the credential's own BaseURL for building
	// page links. Usually empty: the credential already pins the site.
	ConfluenceBaseURL string `json:"confluence_base_url,omitempty"`

	// Webhook — the flexible catch-all: POST the document somewhere.
	WebhookCredential string `json:"webhook_credential,omitempty"`
	WebhookURL        string `json:"webhook_url,omitempty"`
	// WebhookFormat is what gets posted: "json" (default — the whole document
	// as a JSON object), "markdown", or "html".
	WebhookFormat string `json:"webhook_format,omitempty"`
	// WebhookLabel renames the destination in the UI, so "Webhook" can read as
	// "Team wiki" or "Docs pipeline" — the one place a generic destination gets
	// to say what it actually is.
	WebhookLabel string `json:"webhook_label,omitempty"`
}

// config loads the deployment's publish configuration. A missing record is the
// zero value, which leaves every destination unavailable — fail closed.
func (T *PublishApp) config() PublishConfig {
	var c PublishConfig
	if T == nil || T.DB == nil {
		return c
	}
	T.DB.Get(publishConfigTable, publishConfigKey, &c)
	return c
}

func (T *PublishApp) saveConfig(c PublishConfig) {
	if T == nil || T.DB == nil {
		return
	}
	T.DB.Set(publishConfigTable, publishConfigKey, &c)
}

// credentialUsable reports whether a named SecureAPI credential exists, is
// enabled, and is reachable by this user — the single gate every destination's
// Available() runs. The returned string is the reason it can't be used, phrased
// for the person who will read it in chat.
func credentialUsable(user, name string) (bool, string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, "no credential is configured for it yet — an admin sets one in Admin > Publishing"
	}
	s := Secure()
	if s == nil {
		return false, "the credential store is not initialized"
	}
	c, ok := s.Resolve(name, user)
	if !ok {
		return false, "its credential (" + name + ") no longer exists — an admin needs to re-point it in Admin > Publishing"
	}
	if c.Disabled {
		return false, "its credential (" + name + ") is disabled"
	}
	if !s.UserMayUse(c, user) {
		return false, "you don't have access to its credential (" + name + ")"
	}
	// A per-user credential with no secret for THIS user means they haven't
	// connected their own account yet — the actionable case, so say where to go.
	if c.IsPerUser() && !s.HasUserSecret(name, user) {
		return false, "you haven't connected your account yet — do that on your Account page, under Connected accounts"
	}
	return true, ""
}

// --- endpoints ---------------------------------------------------------------

// handleConfig serves + saves the deployment configuration. Admin-only: it
// names which credential reaches out to which external system, which is a
// deployment decision, not a personal one.
func (T *PublishApp) handleConfig(w http.ResponseWriter, r *http.Request) {
	if !RequestIsAdmin(r) {
		http.Error(w, "Publishing settings are admin-only: they name which credential writes to which external system.", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost {
		var in PublishConfig
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		in.ConfluenceCredential = strings.TrimSpace(in.ConfluenceCredential)
		in.ConfluenceBaseURL = strings.TrimRight(strings.TrimSpace(in.ConfluenceBaseURL), "/")
		in.WebhookCredential = strings.TrimSpace(in.WebhookCredential)
		in.WebhookURL = strings.TrimSpace(in.WebhookURL)
		in.WebhookFormat = strings.TrimSpace(in.WebhookFormat)
		in.WebhookLabel = strings.TrimSpace(in.WebhookLabel)
		T.saveConfig(in)
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	writeJSON(w, T.config())
}

// handleDestinations lists the registered destinations with THIS user's
// availability resolved — what a producer app shows before opening a publisher
// chat, and what tells someone a destination exists but needs connecting.
func (T *PublishApp) handleDestinations(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	writeJSON(w, map[string]any{"destinations": docs.PublishDestinations(user)})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// --- admin section -----------------------------------------------------------

func adminSection() ui.Section {
	return ui.Section{
		Group:    "Apps",
		Title:    "Publishing",
		Subtitle: "Where a finished document can be published from a writer app's Publish button. Each destination points at a SecureAPI credential, which is what actually holds the secret and what an admin can disable to cut off publishing entirely. A destination with no credential is not offered.",
		Body: ui.FormPanel{
			Source:      "/publish/api/config",
			SubmitLabel: "Save publishing settings",
			Fields: []ui.FormField{
				{
					Field: "confluence_credential", Label: "Confluence credential", Type: "text",
					Placeholder: "name of a SecureAPI credential",
					Help:        "The credential used to create and update pages. Its Base URL should be the Confluence site (e.g. https://acme.atlassian.net) and its allowed endpoints must include /wiki/api/v2/**. Set the credential's scope to per-user if each person should publish as themselves.",
				},
				{
					Field: "confluence_base_url", Label: "Confluence site URL", Type: "text",
					Placeholder: "https://acme.atlassian.net (optional)",
					Help:        "Only needed when page links should be built from a different host than the credential's Base URL. Leave empty to use the credential's.",
				},
				{
					Field: "webhook_label", Label: "Webhook name", Type: "text",
					Placeholder: "Team wiki",
					Help:        "What this destination is called in the Publish dialog. Leave empty for \"Webhook\".",
				},
				{
					Field: "webhook_credential", Label: "Webhook credential", Type: "text",
					Placeholder: "name of a SecureAPI credential",
					Help:        "The credential that authorizes the POST. The generic destination: anything that accepts an HTTP post of a document.",
				},
				{
					Field: "webhook_url", Label: "Webhook URL", Type: "text",
					Placeholder: "https://example.com/api/docs",
					Help:        "Absolute URL the document is posted to. Must be allowed by the credential's Base URL and endpoint list.",
				},
				{
					Field: "webhook_format", Label: "Webhook body", Type: "select",
					Options: []ui.SelectOption{
						{Value: "json", Label: "JSON — the whole document as an object"},
						{Value: "markdown", Label: "Markdown — the document body only"},
						{Value: "html", Label: "HTML — the rendered document"},
					},
					Help: "What gets posted. JSON carries the title, markdown, html, and the source document's id.",
				},
			},
		},
	}
}
