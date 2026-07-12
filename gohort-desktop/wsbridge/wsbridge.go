// Package wsbridge is the WebSocket client that connects the headless
// gohort-bridge daemon to the gohort server and exposes the daemon's
// locally-registered tools (see core/tool.go + macos/*) to the
// server's agent loop. It turns "tool registered in the daemon" into
// "tool callable by an agent on the server."
//
// Protocol mirror of core/desktop_bridge.go on the server:
//
//   - On connect: send {type:"announce", tools:[…catalog…]}.
//   - Server sends {type:"invoke", id, name, args} for each tool call.
//   - Reply with {type:"result", id, result, error}.
//
// Auth: the daemon authenticates with the unified API key (X-API-Key
// header) — the same key it uses for phantom's /api/hook + /api/poll.
// The server accepts it via the validator hook registered in
// apps/phantom (see core.RegisterAPIKeyValidator). Without a key the
// bridge backs off and retries; tools simply aren't available until
// the daemon is configured (gohort-bridge --setup).
//
// Reconnect: exponential backoff, capped at 30s. The bridge is
// non-essential — agents work fine without local tools — so failures
// are warn-logged and never block startup.
//
// Migrated from gohort-desktop/ws_client.go during the bridge
// consolidation. The only behavioral changes from that file are the
// auth swap (cookie → X-API-Key) and the Approver indirection that
// replaces the Wails *App dependency, keeping this package — and the
// daemon that imports it — free of Wails.
package wsbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
	"github.com/gorilla/websocket"
)

const (
	wsPath             = "/api/desktop/ws"
	wsInitialBackoff   = 2 * time.Second
	wsMaxBackoff       = 30 * time.Second
	wsHandshakeTimeout = 8 * time.Second
	wsPingInterval     = 25 * time.Second
	wsReadDeadline     = 70 * time.Second
)

// Approver gates server-initiated tool invocations behind user
// consent. The daemon supplies an implementation (a tray prompt, a
// native alert, or an auto-approve toggle). A nil Approver means
// every invocation runs without a prompt — acceptable only when the
// daemon's own config opts into auto-approve.
type Approver interface {
	RequestApprovalBlocking(id, name string, args map[string]any) bool
}

// Installer applies a server-pushed capability install to the daemon: host a
// local MCP server (Install) or drop one (Remove). The daemon supplies an
// implementation backed by the mcp host. A nil Installer means the daemon
// IGNORES install frames — server-push is off unless the daemon opts in. The
// consent gate for applying an install lives in the bridge (it reuses the
// Approver), so an implementation here just does the mechanical work.
type Installer interface {
	Install(name, command string, args []string, env map[string]string) error
	Remove(name string) error
	InstallCommand(name string, spec CommandSpec) error
	RemoveCommand(name string) error
	// InstallBridge enables a built-in messaging relay (e.g. iMessage) with the
	// given poll interval; RemoveBridge disables it. No command runs — the relay
	// is compiled into the daemon; this only flips its enabled state.
	InstallBridge(service string, pollSecs int) error
	RemoveBridge(service string) error
}

// CommandSpec is a declared-command capability (a fixed executable run per
// tool-call). The mechanical shape the daemon's command host consumes.
type CommandSpec struct {
	Desc     string
	Command  string
	Args     []string
	Params   map[string]core.ToolParam
	Required []string
}

// Config is the live config source the client reads on every (re)connect
// — so a config edit takes effect without restarting. Both *core.Config
// and a sidecar-backed adapter satisfy it.
type Config interface {
	ServerURL() string
	APIKey() string
}

// wsClient runs the long-lived bridge. One instance per daemon; the
// reconnect loop is the only goroutine, plus per-connection read/
// ping pumps spawned when a connection is live.
type wsClient struct {
	cfg       Config
	approver  Approver
	installer Installer

	mu      sync.Mutex
	stop    chan struct{}
	conn    *websocket.Conn
	writeMu sync.Mutex // WS connections aren't safe for concurrent writes
}

// StartClient spawns the bridge loop in the background. Caller keeps
// the returned function around to cleanly tear it down on shutdown.
// Safe to call before the daemon is configured; the loop keeps
// retrying with backoff until a server URL + API key are set.
func StartClient(cfg Config, approver Approver, installer Installer) func() {
	c := &wsClient{cfg: cfg, approver: approver, installer: installer, stop: make(chan struct{})}
	// Re-announce the catalog whenever the dynamic tool set changes (an MCP
	// server coming online, a declared command installed) — so a new capability
	// reaches the server without waiting for a reconnect.
	core.SetRegistryChangeHook(c.reannounce)
	go c.runForever()
	return func() {
		core.SetRegistryChangeHook(nil)
		close(c.stop)
		c.mu.Lock()
		if c.conn != nil {
			c.conn.Close()
		}
		c.mu.Unlock()
	}
}

// reannounce pushes the current tool catalog on the live connection, if any.
// Wired to core.SetRegistryChangeHook. No-op when disconnected — the next
// connect announces the fresh catalog anyway.
func (c *wsClient) reannounce() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return
	}
	if err := c.announce(conn); err != nil {
		core.Warn("[ws-bridge] re-announce failed: %v", err)
	}
}

func (c *wsClient) runForever() {
	backoff := wsInitialBackoff
	for {
		select {
		case <-c.stop:
			return
		default:
		}
		serverURL := c.cfg.ServerURL()
		if serverURL == "" {
			// Not configured yet — wait + retry.
			c.sleep(wsInitialBackoff)
			continue
		}
		err := c.connectAndServe(serverURL)
		if err == nil {
			// Closed cleanly — reset backoff for the next round.
			backoff = wsInitialBackoff
			continue
		}
		// Don't log the same auth error every backoff — it's
		// normal before the daemon is configured.
		if !errors.Is(err, errWSNotAuthenticated) {
			core.Warn("[ws-bridge] connection failed: %v (backoff %s)", err, backoff)
		}
		c.sleep(backoff)
		backoff = backoff * 2
		if backoff > wsMaxBackoff {
			backoff = wsMaxBackoff
		}
	}
}

func (c *wsClient) sleep(d time.Duration) {
	select {
	case <-c.stop:
	case <-time.After(d):
	}
}

var errWSNotAuthenticated = errors.New("ws: not configured yet (no server URL / API key)")

// connectAndServe opens one WS connection, announces the tool
// catalog, then serves invocations until the connection drops.
// Returns nil on clean shutdown, an error on failure / disconnect.
func (c *wsClient) connectAndServe(serverURL string) error {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return err
	}
	wsURL := *parsed
	switch parsed.Scheme {
	case "http":
		wsURL.Scheme = "ws"
	case "https":
		wsURL.Scheme = "wss"
	default:
		return errors.New("unsupported server URL scheme")
	}
	wsURL.Path = strings.TrimSuffix(wsURL.Path, "/") + wsPath

	// Authenticate with the unified API key. Without one the server
	// returns 401; back off and retry once the daemon is configured.
	apiKey := c.cfg.APIKey()
	// Diagnostic: surface what key the bridge resolved, from where, and
	// which config dir it read — so a 401 traces to "no key / wrong key /
	// dir mismatch" rather than guesswork. Key is masked.
	keySrc := "settings"
	if core.ReadAPIKeySidecar() != "" {
		keySrc = "sidecar"
	}
	masked := "(empty)"
	if len(apiKey) >= 6 {
		masked = apiKey[:6] + "…"
	}
	core.Log("[wsbridge] auth: key_src=%s key=%s key_len=%d configdir=%s", keySrc, masked, len(apiKey), core.ConfigDir())
	if apiKey == "" {
		return errWSNotAuthenticated
	}
	header := http.Header{}
	header.Set("X-API-Key", apiKey)

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = wsHandshakeTimeout
	conn, resp, err := dialer.Dial(wsURL.String(), header)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return errWSNotAuthenticated
		}
		return err
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		conn.Close()
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
	}()

	core.Log("[ws-bridge] connected to %s", wsURL.String())

	// Announce the local catalog right away.
	if err := c.announce(conn); err != nil {
		return err
	}

	// Background ping loop keeps the connection alive through
	// reverse proxies that close idle streams.
	pingStop := make(chan struct{})
	go c.pingLoop(conn, pingStop)
	defer close(pingStop)

	// Read deadline + pong handler — same pattern the server uses.
	conn.SetReadDeadline(time.Now().Add(wsReadDeadline))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(wsReadDeadline))
		return nil
	})

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &head) != nil {
			continue
		}
		switch head.Type {
		case "invoke":
			var inv struct {
				ID   string         `json:"id"`
				Name string         `json:"name"`
				Args map[string]any `json:"args"`
			}
			if json.Unmarshal(data, &inv) != nil {
				continue
			}
			// Run the tool in its own goroutine so a slow one doesn't
			// block delivery of subsequent invocations.
			go c.handleInvoke(conn, inv.ID, inv.Name, inv.Args)
		case "install":
			var m installFrame
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			// Off the read loop — installs spawn subprocesses + prompt.
			go c.handleInstall(m)
		default:
			// Unknown — ignore. Forward-compat for future message types.
		}
	}
}

// installFrame is the server→daemon capability-install message: host the MCP
// servers in Servers and declared commands in Commands; drop the ones named in
// Remove (MCP) / RemoveCommands.
type installFrame struct {
	Servers map[string]struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	} `json:"servers"`
	Commands map[string]struct {
		Desc     string                    `json:"desc"`
		Command  string                    `json:"command"`
		Args     []string                  `json:"args"`
		Params   map[string]core.ToolParam `json:"params"`
		Required []string                  `json:"required"`
	} `json:"commands"`
	Bridges map[string]struct {
		PollSecs int `json:"poll_secs"`
	} `json:"bridges"`
	Remove         []string `json:"remove"`
	RemoveCommands []string `json:"remove_commands"`
	RemoveBridges  []string `json:"remove_bridges"`
}

// handleInstall applies a server-pushed capability install. Removes apply
// directly (tearing down is safe); each new/replaced server is gated behind the
// SAME user-consent prompt as a tool call, so the machine's owner authorizes
// running a new local subprocess. A nil Installer means server-push is off.
func (c *wsClient) handleInstall(m installFrame) {
	if c.installer == nil {
		core.Warn("[ws-bridge] ignoring install frame — server-push not enabled on this daemon")
		return
	}
	// Removes apply directly — tearing down is safe.
	for _, name := range m.Remove {
		if err := c.installer.Remove(name); err != nil {
			core.Warn("[ws-bridge] install remove %q failed: %v", name, err)
		} else {
			core.Log("[ws-bridge] removed pushed capability %q", name)
		}
	}
	for _, name := range m.RemoveCommands {
		if err := c.installer.RemoveCommand(name); err != nil {
			core.Warn("[ws-bridge] install remove command %q failed: %v", name, err)
		} else {
			core.Log("[ws-bridge] removed pushed command %q", name)
		}
	}
	for _, service := range m.RemoveBridges {
		if err := c.installer.RemoveBridge(service); err != nil {
			core.Warn("[ws-bridge] disable bridge %q failed: %v", service, err)
		} else {
			core.Log("[ws-bridge] disabled pushed bridge %q", service)
		}
	}
	// Each new/replaced capability is gated by the SAME user-consent prompt as a
	// tool call — the machine's owner authorizes running new local code.
	for name, s := range m.Servers {
		if !c.consentInstall(name, s.Command, s.Args) {
			continue
		}
		if err := c.installer.Install(name, s.Command, s.Args, s.Env); err != nil {
			core.Warn("[ws-bridge] install of %q failed: %v", name, err)
			continue
		}
		core.Log("[ws-bridge] installed pushed capability %q (%s)", name, s.Command)
	}
	for name, s := range m.Commands {
		if !c.consentInstall(name, s.Command, s.Args) {
			continue
		}
		spec := CommandSpec{Desc: s.Desc, Command: s.Command, Args: s.Args, Params: s.Params, Required: s.Required}
		if err := c.installer.InstallCommand(name, spec); err != nil {
			core.Warn("[ws-bridge] install command %q failed: %v", name, err)
			continue
		}
		core.Log("[ws-bridge] installed pushed command %q (%s)", name, s.Command)
	}
	// Enabling a built-in relay reads the user's messages, so it's gated by the
	// same consent prompt — the machine's owner authorizes turning it on.
	for service, s := range m.Bridges {
		if !c.consentBridge(service) {
			continue
		}
		if err := c.installer.InstallBridge(service, s.PollSecs); err != nil {
			core.Warn("[ws-bridge] enable bridge %q failed: %v", service, err)
			continue
		}
		core.Log("[ws-bridge] enabled pushed bridge %q", service)
	}
}

// consentBridge asks the user to authorize turning on a built-in messaging
// relay. A nil Approver auto-allows (the daemon opted into that).
func (c *wsClient) consentBridge(service string) bool {
	if c.approver == nil {
		return true
	}
	ok := c.approver.RequestApprovalBlocking("bridge-"+service, "enable_bridge:"+service,
		map[string]any{"service": service})
	if !ok {
		core.Log("[ws-bridge] enabling bridge %q denied by user", service)
	}
	return ok
}

// consentInstall asks the user to authorize running a new local capability.
// A nil Approver auto-allows (the daemon's own config opted into that).
func (c *wsClient) consentInstall(name, command string, args []string) bool {
	if c.approver == nil {
		return true
	}
	ok := c.approver.RequestApprovalBlocking("install-"+name, "install_capability:"+name,
		map[string]any{"command": command, "args": args})
	if !ok {
		core.Log("[ws-bridge] install of %q denied by user", name)
	}
	return ok
}

func (c *wsClient) pingLoop(conn *websocket.Conn, stop <-chan struct{}) {
	t := time.NewTicker(wsPingInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			c.writeMu.Lock()
			err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			c.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// announce sends the current tool catalog. Pulled at call time so a
// reconnect after new tool packages registered sees the latest list.
func (c *wsClient) announce(conn *websocket.Conn) error {
	regs := core.RegisteredTools()
	descs := make([]map[string]any, 0, len(regs))
	for _, t := range regs {
		descs = append(descs, map[string]any{
			"name":     t.Name(),
			"desc":     t.Desc(),
			"params":   t.Params(),
			"required": t.Required(),
		})
	}
	frame, _ := json.Marshal(map[string]any{
		"type":  "announce",
		"tools": descs,
	})
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	core.Log("[ws-bridge] announcing %d local tool(s)", len(descs))
	return conn.WriteMessage(websocket.TextMessage, frame)
}

// handleInvoke runs one tool locally and writes the result back.
// Panics inside the tool become error results — never propagate to
// the read loop, which would kill the connection for the wrong reason.
//
// Approval gate: before calling the tool, ask the Approver. A nil
// Approver runs without a prompt; deny returns a clean "denied by
// user" error so the agent sees a failure rather than a hang.
func (c *wsClient) handleInvoke(conn *websocket.Conn, id, name string, args map[string]any) {
	defer func() {
		if rec := recover(); rec != nil {
			core.Err("[ws-bridge] tool %q panicked: %v", name, rec)
			c.sendResult(conn, id, "", "tool panic")
		}
	}()
	if c.approver != nil {
		if !c.approver.RequestApprovalBlocking(id, name, args) {
			core.Log("[ws-bridge] tool %q (id=%s) denied by user", name, id)
			c.sendResult(conn, id, "", "denied by user")
			return
		}
	}
	result, err := core.InvokeTool(name, args)
	if err != nil {
		c.sendResult(conn, id, "", err.Error())
		return
	}
	c.sendResult(conn, id, result, "")
}

// maxResultBytes caps a plain tool result before it goes over the bridge
// WS. A runaway result (read_file on a huge file, a query that returns
// everything) otherwise buffers fully in RAM on both ends and floods the
// agent's context. 256 KB is already far more text than is useful in a
// single tool result.
const maxResultBytes = 256 * 1024

// capResult truncates an oversized plain-text result and appends a notice
// so the agent knows the result was clipped (rather than silently acting on
// a partial blob). data: URIs are exempt: image/file tools return base64
// data URIs through this same path for vision/attachment delivery, and a
// truncated base64 string is a corrupt payload, not shorter text. The
// truncation is snapped to a valid UTF-8 boundary so the JSON frame stays
// clean.
func capResult(result string) string {
	if len(result) <= maxResultBytes || strings.HasPrefix(result, "data:") {
		return result
	}
	head := strings.ToValidUTF8(result[:maxResultBytes], "")
	return head + fmt.Sprintf("\n\n[gohort-bridge: result truncated — was %d bytes, showing the first %d]", len(result), len(head))
}

func (c *wsClient) sendResult(conn *websocket.Conn, id, result, errMsg string) {
	frame, _ := json.Marshal(map[string]any{
		"type":   "result",
		"id":     id,
		"result": capResult(result),
		"error":  errMsg,
	})
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		core.Warn("[ws-bridge] result write failed: %v", err)
	}
}
