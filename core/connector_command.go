// The desktop_command connector kind: materializes a DECLARED-COMMAND tool on
// the owner's own machine — a fixed executable the daemon runs per tool-call,
// with {placeholder} args filled from the call and stdout returned. The
// lightweight sibling of desktop_mcp: for a simple local capability (run a
// script, a CLI) that doesn't warrant a whole MCP server. Mirrors how gohort's
// own skills bundle shell scripts.
//
// Approval is MANDATORY and never auto (it runs a command on the user's
// machine), so it does NOT implement ConnectorAutoApprover. Two human gates sit
// in front of any local execution: admin approve (here) + the daemon's
// user-consent prompt when it applies the install, plus the normal per-call
// approval every desktop tool goes through on invoke.
package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DesktopCommandConnectorKind is the Kind value for a declared-command capability.
const DesktopCommandConnectorKind = "desktop_command"

// DesktopCommandSpec is the Spec payload for a desktop_command connector.
type DesktopCommandSpec struct {
	Command  string               `json:"command"`
	Args     []string             `json:"args,omitempty"`
	Desc     string               `json:"desc,omitempty"`
	Params   map[string]ToolParam `json:"params,omitempty"`
	Required []string             `json:"required,omitempty"`
}

func init() { RegisterConnectorKind(DesktopCommandConnectorKind, desktopCommandHandler{}) }

type desktopCommandHandler struct{}

func (desktopCommandHandler) parse(c Connector) (DesktopCommandSpec, error) {
	var s DesktopCommandSpec
	if len(c.Spec) > 0 {
		if err := json.Unmarshal(c.Spec, &s); err != nil {
			return s, fmt.Errorf("bad desktop_command spec: %w", err)
		}
	}
	s.Command = strings.TrimSpace(s.Command)
	return s, nil
}

func (h desktopCommandHandler) Validate(c Connector) error {
	if strings.TrimSpace(c.Owner) == "" {
		return fmt.Errorf("desktop_command requires an owner (the user on whose machine it runs)")
	}
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	if s.Command == "" {
		return fmt.Errorf("command is required (the executable the desktop runs, e.g. \"screencapture\")")
	}
	return nil
}

func (h desktopCommandHandler) Materialize(c Connector) error {
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	n, err := InstallToDesktop(c.Owner, DesktopInstall{
		Commands: map[string]DesktopCommand{
			c.Name: {Desc: s.Desc, Command: s.Command, Args: s.Args, Params: s.Params, Required: s.Required},
		},
	})
	if err != nil {
		return err
	}
	Log("[connector] desktop_command %q pushed to %d desktop connection(s) for %s", c.Name, n, c.Owner)
	return nil
}

func (h desktopCommandHandler) Teardown(c Connector) error {
	if _, err := InstallToDesktop(c.Owner, DesktopInstall{RemoveCommands: []string{c.Name}}); err != nil {
		Warn("[connector] desktop_command %q teardown couldn't reach %s: %v", c.Name, c.Owner, err)
	}
	return nil
}

func (h desktopCommandHandler) Summary(c Connector) string {
	s, _ := h.parse(c)
	cmd := s.Command
	if cmd == "" {
		cmd = "(no command)"
	} else if len(s.Args) > 0 {
		cmd += " " + strings.Join(s.Args, " ")
	}
	return fmt.Sprintf("desktop command: run `%s` on %s's machine (tool %s)", cmd, c.Owner, c.Name)
}
