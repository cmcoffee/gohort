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
	cfg      Config
	approver Approver

	mu      sync.Mutex
	stop    chan struct{}
	conn    *websocket.Conn
	writeMu sync.Mutex // WS connections aren't safe for concurrent writes
}

// StartClient spawns the bridge loop in the background. Caller keeps
// the returned function around to cleanly tear it down on shutdown.
// Safe to call before the daemon is configured; the loop keeps
// retrying with backoff until a server URL + API key are set.
func StartClient(cfg Config, approver Approver) func() {
	c := &wsClient{cfg: cfg, approver: approver, stop: make(chan struct{})}
	go c.runForever()
	return func() {
		close(c.stop)
		c.mu.Lock()
		if c.conn != nil {
			c.conn.Close()
		}
		c.mu.Unlock()
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
		if head.Type != "invoke" {
			continue
		}
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
	}
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

func (c *wsClient) sendResult(conn *websocket.Conn, id, result, errMsg string) {
	frame, _ := json.Marshal(map[string]any{
		"type":   "result",
		"id":     id,
		"result": result,
		"error":  errMsg,
	})
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		core.Warn("[ws-bridge] result write failed: %v", err)
	}
}
