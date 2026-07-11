// The desktop_mcp connector kind: materializes a LOCAL MCP server on the
// connector owner's own machine (a stdio subprocess hosted by their
// gohort-desktop bridge), so its tools become callable by agents as
// <name>.<tool> via the from_client.* surface. This is the desktop-side analog
// of remote_mcp — the same "declare a bridge type, no code change" idea, but
// the capability runs on the user's laptop instead of a remote HTTP endpoint.
//
// Approval is MANDATORY and never auto: materializing runs a command on the
// user's machine, so it does NOT implement ConnectorAutoApprover — it stays
// pending until an admin approves. The push then reaches the user's daemon,
// which gates APPLYING it behind its own consent layer. Two independent human
// gates (admin approve + daemon consent) sit in front of any local execution.
package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DesktopMCPConnectorKind is the Kind value for a desktop-hosted MCP server.
const DesktopMCPConnectorKind = "desktop_mcp"

// DesktopMCPSpec is the Spec payload for a desktop_mcp connector.
type DesktopMCPSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

func init() { RegisterConnectorKind(DesktopMCPConnectorKind, desktopMCPHandler{}) }

type desktopMCPHandler struct{}

func (desktopMCPHandler) parse(c Connector) (DesktopMCPSpec, error) {
	var s DesktopMCPSpec
	if len(c.Spec) > 0 {
		if err := json.Unmarshal(c.Spec, &s); err != nil {
			return s, fmt.Errorf("bad desktop_mcp spec: %w", err)
		}
	}
	s.Command = strings.TrimSpace(s.Command)
	return s, nil
}

func (h desktopMCPHandler) Validate(c Connector) error {
	if strings.TrimSpace(c.Owner) == "" {
		return fmt.Errorf("desktop_mcp requires an owner (the user on whose machine the server runs)")
	}
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	if s.Command == "" {
		return fmt.Errorf("command is required (the executable the desktop launches, e.g. \"npx\")")
	}
	return nil
}

// Materialize pushes the install to the owner's connected desktop bridge. Errors
// if the user's desktop isn't online (surfaced as the connector's LastError so
// an admin re-approves once it is).
func (h desktopMCPHandler) Materialize(c Connector) error {
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	n, err := InstallToDesktop(c.Owner, DesktopInstall{
		Servers: map[string]DesktopMCPServer{
			c.Name: {Command: s.Command, Args: s.Args, Env: s.Env},
		},
	})
	if err != nil {
		return err
	}
	Log("[connector] desktop_mcp %q pushed to %d desktop connection(s) for %s", c.Name, n, c.Owner)
	return nil
}

// Teardown asks the owner's desktop to drop the server. Best-effort: if the
// desktop is offline we can't reach it, but the connector is being removed
// anyway — don't block deletion on an unreachable machine.
func (h desktopMCPHandler) Teardown(c Connector) error {
	if _, err := InstallToDesktop(c.Owner, DesktopInstall{Remove: []string{c.Name}}); err != nil {
		Warn("[connector] desktop_mcp %q teardown couldn't reach %s: %v", c.Name, c.Owner, err)
	}
	return nil
}

func (h desktopMCPHandler) Summary(c Connector) string {
	s, _ := h.parse(c)
	cmd := s.Command
	if cmd == "" {
		cmd = "(no command)"
	} else if len(s.Args) > 0 {
		cmd += " " + strings.Join(s.Args, " ")
	}
	return fmt.Sprintf("desktop MCP: run `%s` on %s's machine → tools as %s.<tool>", cmd, c.Owner, c.Name)
}
