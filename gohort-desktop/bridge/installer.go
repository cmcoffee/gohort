package bridge

import (
	"github.com/cmcoffee/gohort/gohort-desktop/command"
	"github.com/cmcoffee/gohort/gohort-desktop/core"
	"github.com/cmcoffee/gohort/gohort-desktop/mcp"
	"github.com/cmcoffee/gohort/gohort-desktop/wsbridge"
)

// daemonInstaller applies server-pushed capability installs by driving the
// local MCP host (desktop_mcp) and declared-command host (desktop_command). The
// user-consent gate lives in wsbridge (it reuses the Approver before calling
// these), so this adapter is purely mechanical. Satisfies wsbridge.Installer.
type daemonInstaller struct{}

func (daemonInstaller) Install(name, cmd string, args []string, env map[string]string) error {
	return mcp.Install(name, cmd, args, env)
}

func (daemonInstaller) Remove(name string) error {
	return mcp.Remove(name)
}

func (daemonInstaller) InstallCommand(name string, spec wsbridge.CommandSpec) error {
	return command.Install(name, command.Spec{
		Desc:     spec.Desc,
		Command:  spec.Command,
		Args:     spec.Args,
		Params:   spec.Params,
		Required: spec.Required,
	})
}

func (daemonInstaller) RemoveCommand(name string) error {
	return command.Remove(name)
}

// InstallBridge / RemoveBridge flip a built-in relay's enabled state in the
// daemon-owned bridges.json; startNativeServices reads it to decide whether to
// run the relay. No subprocess — the relay is compiled in.
func (daemonInstaller) InstallBridge(service string, pollSecs int) error {
	return core.SetBridgeService(service, pollSecs)
}

func (daemonInstaller) RemoveBridge(service string) error {
	return core.RemoveBridgeService(service)
}
