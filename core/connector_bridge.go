// The messaging_bridge connector kind: the first TWO-SIDED connector. Unlike
// remote_mcp/rest_poll (server-only) or desktop_mcp/desktop_command
// (client-only), a messaging bridge has a mandatory server half (a BridgeKey +
// routing in the bridges app) AND a client half (a built-in relay compiled into
// the user's gohort-desktop daemon — e.g. iMessage). Materializing provisions
// BOTH: it ensures the server-side routing key exists + enabled, and pushes an
// enable frame to the owner's desktop so the built-in relay turns on.
//
// The client half is CONFIGURE-only, never install: the relay is compiled into
// the daemon (macOS iMessage), so the server can only enable/point it, not ship
// code. The auth key never travels — the daemon auto-negotiates its own with
// the server it's pointed at; the connector carries only the shape (service +
// poll interval). That is what makes it a portable, secret-free artifact that
// rides the same connector export/import pack as every other kind.
//
// Approval is MANDATORY and never auto: it turns on a capability on the user's
// own machine, so it does NOT implement ConnectorAutoApprover — it stays pending
// until an admin approves, and the daemon then gates applying it behind its own
// user-consent prompt. Two independent human gates, same as desktop_mcp.
package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MessagingBridgeConnectorKind is the Kind value for a two-sided messaging bridge.
const MessagingBridgeConnectorKind = "messaging_bridge"

// messagingBridgeServices is the allow-list of built-in bridge services the
// daemon can enable. iMessage is the first (macOS-only, compiled in). Slack /
// Telegram would be server-side pollers added here as they land.
var messagingBridgeServices = map[string]bool{"imessage": true}

// MessagingBridgeSpec is the Spec payload: which built-in service to bridge and
// how often the device-side relay polls. No secret — the daemon auto-negotiates
// its key with whatever server it's pointed at.
type MessagingBridgeSpec struct {
	Service  string `json:"service"`
	PollSecs int    `json:"poll_secs,omitempty"`
}

// --- server-side provisioner seam --------------------------------------------
//
// The server half (ensure a BridgeKey for the service is present + reflects the
// connector's enabled state) lives in the bridges app, which core must not
// import. The app registers an ensurer here at route time (when its store is
// live). When no ensurer is registered (bridges app not loaded), the client
// half still works and the server half soft-warns.

// BridgeProvisioner ensures the owner has a routing bridge for the service and
// sets its enabled state. Registered by the bridges app.
type BridgeProvisioner func(owner, service string, enabled bool) error

var bridgeProvisioner BridgeProvisioner

// RegisterBridgeProvisioner installs the server-side bridge ensurer. Call once
// at route-registration time.
func RegisterBridgeProvisioner(fn BridgeProvisioner) { bridgeProvisioner = fn }

func provisionServiceBridge(owner, service string, enabled bool) error {
	if bridgeProvisioner == nil {
		Warn("[connector] no bridge provisioner registered — skipping server-side %s bridge key for %s", service, owner)
		return nil
	}
	return bridgeProvisioner(owner, service, enabled)
}

func init() { RegisterConnectorKind(MessagingBridgeConnectorKind, messagingBridgeHandler{}) }

type messagingBridgeHandler struct{}

func (messagingBridgeHandler) parse(c Connector) (MessagingBridgeSpec, error) {
	var s MessagingBridgeSpec
	if len(c.Spec) > 0 {
		if err := json.Unmarshal(c.Spec, &s); err != nil {
			return s, fmt.Errorf("bad messaging_bridge spec: %w", err)
		}
	}
	s.Service = strings.ToLower(strings.TrimSpace(s.Service))
	return s, nil
}

func (h messagingBridgeHandler) Validate(c Connector) error {
	if strings.TrimSpace(c.Owner) == "" {
		return fmt.Errorf("messaging_bridge requires an owner (the user whose device runs the relay)")
	}
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	if s.Service == "" {
		return fmt.Errorf("service is required (e.g. \"imessage\")")
	}
	if !messagingBridgeServices[s.Service] {
		return fmt.Errorf("unsupported bridge service %q (supported: imessage)", s.Service)
	}
	return nil
}

// Materialize provisions BOTH sides: ensure the server-side BridgeKey is present
// + enabled, then push an enable frame to the owner's desktop so the built-in
// relay turns on. The server half applies regardless; the client half needs the
// user's desktop online (surfaced as LastError so an admin re-approves once it is).
func (h messagingBridgeHandler) Materialize(c Connector) error {
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	if err := provisionServiceBridge(c.Owner, s.Service, true); err != nil {
		return fmt.Errorf("server bridge key: %w", err)
	}
	n, err := InstallToDesktop(c.Owner, DesktopInstall{
		Bridges: map[string]DesktopBridge{s.Service: {PollSecs: s.PollSecs}},
	})
	if err != nil {
		return fmt.Errorf("server bridge key set, but client enable failed: %w", err)
	}
	Log("[connector] messaging_bridge %q enabled %s on %d desktop connection(s) for %s", c.Name, s.Service, n, c.Owner)
	return nil
}

// Teardown disables both halves: drop routing (disable the key) and tell the
// desktop to turn the relay off. Best-effort on the client — an offline desktop
// is fine, the connector is going away anyway.
func (h messagingBridgeHandler) Teardown(c Connector) error {
	s, _ := h.parse(c)
	if s.Service == "" {
		return nil
	}
	_ = provisionServiceBridge(c.Owner, s.Service, false)
	if _, err := InstallToDesktop(c.Owner, DesktopInstall{RemoveBridges: []string{s.Service}}); err != nil {
		Warn("[connector] messaging_bridge %q client-disable couldn't reach %s: %v", c.Name, c.Owner, err)
	}
	return nil
}

func (h messagingBridgeHandler) Summary(c Connector) string {
	s, _ := h.parse(c)
	svc := s.Service
	if svc == "" {
		svc = "(no service)"
	}
	poll := ""
	if s.PollSecs > 0 {
		poll = fmt.Sprintf(", poll %ds", s.PollSecs)
	}
	return fmt.Sprintf("messaging bridge: %s relay on %s's device%s → agents via channels", svc, c.Owner, poll)
}
