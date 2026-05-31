// WebSocket client that connects to the gohort server and exposes
// the desktop's locally-registered tools (see core/tool.go +
// tools/<category>/) to the server's agent loop. This is the bridge
// that turns "tool registered in the desktop" into "tool callable
// by an agent on the server."
//
// Protocol mirror of core/desktop_bridge.go on the server:
//
//   - On connect: send {type:"announce", tools:[…catalog…]}.
//   - Server sends {type:"invoke", id, name, args} for each tool call.
//   - Reply with {type:"result", id, result, error}.
//
// Auth: uses the gohort_session cookie from the proxy's PersistentCookieJar
// — same cookie the webview's pages use after the user logs in.
// Without a saved cookie the server returns 401 and we back off,
// retrying once login finishes (the cookie jar update happens
// asynchronously when the user signs in via /login).
//
// Reconnect: exponential backoff, capped at 30s. The bridge is
// non-essential — agents work fine without local tools — so failures
// are warn-logged and never block startup.

package main

import (
	"context"
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
	wsPath              = "/api/desktop/ws"
	wsInitialBackoff    = 2 * time.Second
	wsMaxBackoff        = 30 * time.Second
	wsHandshakeTimeout  = 8 * time.Second
	wsPingInterval      = 25 * time.Second
	wsReadDeadline      = 70 * time.Second
)

// wsClient runs the long-lived bridge. One instance per app; the
// reconnect loop is the only goroutine, plus per-connection read/
// ping pumps spawned when a connection is live.
type wsClient struct {
	cfg     *core.Config
	cookies *core.PersistentCookieJar
	// app gives ws_client a way to ask the user for approval
	// before running a server-initiated tool invocation. See
	// handleInvoke + approvals.go.
	app *App

	mu     sync.Mutex
	stop   chan struct{}
	conn   *websocket.Conn
	writeMu sync.Mutex // WS connections aren't safe for concurrent writes
}

// startWSClient spawns the bridge loop in the background. Caller
// keeps the returned function around to cleanly tear it down on
// shutdown. Safe to call before the user has logged in; the loop
// will keep retrying with backoff until the cookie jar has a
// valid session.
func startWSClient(cfg *core.Config, cookies *core.PersistentCookieJar, app *App) func() {
	c := &wsClient{cfg: cfg, cookies: cookies, app: app, stop: make(chan struct{})}
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
			// User hasn't configured yet — wait + retry.
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
		// normal during pre-login startup.
		if !errors.Is(err, errWSNotAuthenticated) {
			core.Warn("[ws-client] connection failed: %v (backoff %s)", err, backoff)
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

var errWSNotAuthenticated = errors.New("ws: not authenticated yet")

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

	// Pull cookies from the proxy's jar for this URL — same cookie
	// the webview uses after login. Without a gohort_session the
	// server returns 401.
	header := http.Header{}
	if c.cookies != nil {
		for _, ck := range c.cookies.Cookies(parsed) {
			header.Add("Cookie", ck.Name+"="+ck.Value)
		}
	}
	if header.Get("Cookie") == "" {
		return errWSNotAuthenticated
	}

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

	core.Log("[ws-client] connected to %s", wsURL.String())

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
// reconnect after a hot-reload (where new tool packages registered
// post-startup) sees the latest list.
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
	core.Log("[ws-client] announcing %d local tool(s)", len(descs))
	return conn.WriteMessage(websocket.TextMessage, frame)
}

// handleInvoke runs one tool locally and writes the result back.
// Crashes inside the tool become error results — never panic up to
// the read loop, which would kill the connection for the wrong
// reason.
//
// Approval gate: before calling the tool, we ask the user via the
// in-window modal (see approvals.go + popup_shim_script's
// approval handler). Auto-approve mode bypasses the prompt; deny /
// timeout returns a "denied by user" error to the server so the
// agent sees a clean failure rather than a hang.
func (c *wsClient) handleInvoke(conn *websocket.Conn, id, name string, args map[string]any) {
	defer func() {
		if rec := recover(); rec != nil {
			core.Err("[ws-client] tool %q panicked: %v", name, rec)
			c.sendResult(conn, id, "", "tool panic")
		}
	}()
	if c.app != nil {
		if !c.app.RequestApprovalBlocking(id, name, args) {
			core.Log("[ws-client] tool %q (id=%s) denied by user", name, id)
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
		core.Warn("[ws-client] result write failed: %v", err)
	}
}

// silence unused-import lint when context is referenced only via doc.
var _ = context.Background
