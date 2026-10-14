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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cmcoffee/gohort/core/internal/mcpclient"
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
			return fmt.Errorf("no credential named %q: draft it first (draft_oauth_credential) and have the admin enable it in Admin > APIs", s.SecureCred)
		}
		// Its token is sent only inside the credential's allow-list, so a
		// server outside it would fail on every call: say so now, where the
		// admin approving it can see the mismatch.
		if cred, ok := Secure().Load(s.SecureCred); ok && !urlAllowedByCredential(cred, s.URL) {
			return fmt.Errorf("the MCP url %s is outside credential %q's allowed addresses (%s): its token is not sent there", s.URL, s.SecureCred, firstNonBlank(cred.BaseURL, cred.AllowedURLPattern))
		}
	case MCPAuthBearer:
		return fmt.Errorf("bearer auth carries a static secret: add a bearer MCP server directly in Admin > MCP Servers, not via a connector")
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
	if err := MCP().Save(h.serverConfig(c, s), ""); err != nil {
		return err
	}
	MCP().Reload()
	return nil
}

// serverConfig is the MCP server this connector materializes into, and the
// one its probe connects to.
func (remoteMCPHandler) serverConfig(c Connector, s RemoteMCPSpec) MCPServerConfig {
	return MCPServerConfig{
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
}

// probe connects to the server the way approval would, without saving it:
// the MCP handshake and its tool list. A server that signs in per person
// refuses a check with nobody signed in; reaching it and being asked to sign
// in is the expected answer, not a failure.
func (h remoteMCPHandler) probe(ctx context.Context, c Connector) (string, error) {
	s, err := h.parse(c)
	if err != nil {
		return "", err
	}
	msg, err := MCP().Test(ctx, h.serverConfig(c, s), "")
	switch {
	case err == nil:
		return msg, nil
	case errors.Is(err, mcpclient.ErrUnauthorized) && MCPAuthMode(s.AuthMode) == MCPAuthOAuth:
		return fmt.Sprintf("Reached %s, which asks for sign-in, as a server that signs in per person does. Once the connector is approved, each person connects it under their account, and its tools list then.", s.URL), nil
	case errors.Is(err, mcpclient.ErrUnauthorized) && MCPAuthMode(s.AuthMode) == MCPAuthSecure:
		return "", fmt.Errorf("reached %s, but it refused credential %q: check the credential is enabled in Admin > APIs and grants access to this server (%v)", s.URL, s.SecureCred, err)
	case errors.Is(err, mcpclient.ErrUnauthorized):
		return "", fmt.Errorf("reached %s, but it requires sign-in: set auth_mode to secure_api (with a credential) or oauth (%v)", s.URL, err)
	}
	return "", fmt.Errorf("could not connect to %s: %v", s.URL, err)
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
	if s.AuthMode == string(MCPAuthSecure) && s.SecureCred != "" {
		// Name the key it spends, for the admin approving it.
		auth += " via credential " + s.SecureCred
	}
	return fmt.Sprintf("remote MCP server %s (auth: %s) → tools as %s.<tool>", url, auth, c.Name)
}
