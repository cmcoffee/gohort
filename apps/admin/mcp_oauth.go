package admin

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// handleMCPOAuthStart begins the per-user OAuth connect for a hosted MCP
// server. It 302-redirects the browser to the authorization server, so
// the "Connect" row button (a GET that opens this in a new tab) flows
// straight into the consent screen. The redirect_uri is derived from
// this request's host so it matches the callback route below.
func (a *AdminApp) handleMCPOAuthStart(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	user := AuthCurrentUser(r)
	if user == "" {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return
	}
	authURL, err := MCP().StartOAuth(user, name, mcpOAuthCallbackURL(r))
	if err != nil {
		mcpOAuthResultPage(w, "Could not start authorization for "+name+":\n\n"+err.Error())
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleMCPOAuthCallback redeems the authorization code. The state is the
// unguessable secret tying the callback to the pending request (and the
// authorizing user), so this only needs a logged-in session, not admin.
func (a *AdminApp) handleMCPOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if AuthCurrentUser(r) == "" {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		mcpOAuthResultPage(w, "Authorization was declined or failed: "+e+" "+q.Get("error_description"))
		return
	}
	code, state := q.Get("code"), q.Get("state")
	if code == "" || state == "" {
		mcpOAuthResultPage(w, "Missing authorization code or state.")
		return
	}
	if err := MCP().CompleteOAuth(state, code); err != nil {
		mcpOAuthResultPage(w, "Could not complete the connection:\n\n"+err.Error())
		return
	}
	mcpOAuthResultPage(w, "Connected. You can close this tab and return to Admin -> MCP Servers.")
}

// mcpOAuthCallbackURL builds the absolute callback URL from the request.
// Must be https or localhost (OAuth 2.1 + Atlassian requirement); plain
// http on a non-local host will be rejected by the authorization server.
func mcpOAuthCallbackURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/admin/api/mcp-servers/oauth/callback"
}

// mcpOAuthResultPage renders a minimal self-contained result page (no
// proxied assets needed — this tab may be a fresh OAuth popup).
func mcpOAuthResultPage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>MCP Connect</title>` +
		`<style>html,body{height:100%;margin:0}body{background:#0d1117;color:#c9d1d9;` +
		`font-family:-apple-system,system-ui,sans-serif;display:flex;align-items:center;justify-content:center}` +
		`.card{max-width:480px;width:90%;background:#161b22;border:1px solid #30363d;border-radius:10px;padding:28px;white-space:pre-wrap;line-height:1.5}</style>` +
		`</head><body><div class="card">` + htmlEscape(msg) + `</div></body></html>`))
}

// htmlEscape is a tiny escaper for the result message (avoids importing
// html/template for one string).
func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
