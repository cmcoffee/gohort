// The remote_mcp connector kind: materializes a remote Model Context Protocol
// server. "Create a calendar bridge type" = draft one of these pointing at a
// calendar MCP server; on approval its tools register as <name>.<tool> for
// every agent (via the existing MCPManager). This is the first ConnectorHandler
// and the template for future kinds.
//
// Secret-free auth only. The LLM must never carry a secret, so the kind accepts
// none (public), secure_api (mint a bearer from a registered SecureAPI
// credential, referenced by name), and oauth (per-user hosted login). A static
// bearer token IS a secret — those servers are added by the admin directly in
// Admin > MCP Servers, not through a connector.
package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RemoteMCPConnectorKind is the Kind value for a remote-MCP-server connector.
const RemoteMCPConnectorKind = "remote_mcp"

// RemoteMCPSpec is the Spec payload for a remote_mcp connector. Mirrors the
// LLM-authorable subset of MCPServerConfig (no token, no bearer).
type RemoteMCPSpec struct {
	URL        string `json:"url"`
	AuthMode   string `json:"auth_mode"`   // "" (none) | secure_api | oauth
	SecureCred string `json:"secure_cred"` // credential name (secure_api mode)
	// oauth-mode discovery fallbacks (optional; blank ⇒ auto-discovered)
	OAuthClientID     string `json:"oauth_client_id,omitempty"`
	OAuthAuthorizeURL string `json:"oauth_authorize_url,omitempty"`
	OAuthTokenURL     string `json:"oauth_token_url,omitempty"`
	OAuthScopes       string `json:"oauth_scopes,omitempty"`
	OAuthAudience     string `json:"oauth_audience,omitempty"`
}

func init() { RegisterConnectorKind(RemoteMCPConnectorKind, remoteMCPHandler{}) }

type remoteMCPHandler struct{}

// parse unmarshals + trims the Spec.
func (remoteMCPHandler) parse(c Connector) (RemoteMCPSpec, error) {
	var s RemoteMCPSpec
	if len(c.Spec) > 0 {
		if err := json.Unmarshal(c.Spec, &s); err != nil {
			return s, fmt.Errorf("bad remote_mcp spec: %w", err)
		}
	}
	s.URL = strings.TrimSpace(s.URL)
	s.AuthMode = strings.TrimSpace(s.AuthMode)
	s.SecureCred = strings.TrimSpace(s.SecureCred)
	return s, nil
}

func (h remoteMCPHandler) Validate(c Connector) error {
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(s.URL, "https://") && !strings.HasPrefix(s.URL, "http://") {
		return fmt.Errorf("url must be http(s)")
	}
	switch MCPAuthMode(s.AuthMode) {
	case MCPAuthNone, MCPAuthOAuth:
		// no secret referenced
	case MCPAuthSecure:
		if s.SecureCred == "" {
			return fmt.Errorf("auth_mode secure_api requires secure_cred (a registered SecureAPI credential name)")
		}
		if exists, _, _ := Secure().CredentialStatus(s.SecureCred); !exists {
			return fmt.Errorf("no credential named %q — draft it first (draft_oauth_credential) and have the admin enable it in Admin > APIs", s.SecureCred)
		}
	case MCPAuthBearer:
		return fmt.Errorf("bearer auth carries a static secret — add a bearer MCP server directly in Admin > MCP Servers, not via a connector")
	default:
		return fmt.Errorf("auth_mode must be one of: none, secure_api, oauth")
	}
	return nil
}

// Materialize saves + connects an enabled MCP server named for the connector.
// MCP().Save is an upsert, so approve / re-save / restart all converge on the
// same server record.
func (h remoteMCPHandler) Materialize(c Connector) error {
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	cfg := MCPServerConfig{
		Name:              c.Name,
		URL:               s.URL,
		AuthMode:          MCPAuthMode(s.AuthMode),
		SecureCred:        s.SecureCred,
		ExposeTools:       true,
		OAuthClientID:     s.OAuthClientID,
		OAuthAuthorizeURL: s.OAuthAuthorizeURL,
		OAuthTokenURL:     s.OAuthTokenURL,
		OAuthScopes:       s.OAuthScopes,
		OAuthAudience:     s.OAuthAudience,
		Enabled:           true,
	}
	if err := MCP().Save(cfg, ""); err != nil {
		return err
	}
	MCP().Reload()
	return nil
}

// Teardown removes the materialized MCP server (already-registered proxy tools
// go inert until the next restart, per MCPManager.Delete).
func (h remoteMCPHandler) Teardown(c Connector) error {
	return MCP().Delete(c.Name)
}

func (h remoteMCPHandler) Summary(c Connector) string {
	s, _ := h.parse(c)
	auth := s.AuthMode
	if auth == "" {
		auth = "none"
	}
	url := s.URL
	if url == "" {
		url = "(no url)"
	}
	return fmt.Sprintf("remote MCP server %s (auth: %s) → tools as %s.<tool>", url, auth, c.Name)
}
