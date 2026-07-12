// Desktop bridge — accepts WebSocket connections from gohort-desktop
// clients and exposes their locally-registered tools to the gohort
// server's agent loop as per-user ChatTools.
//
// The motivating use case: an admin runs gohort serve remotely (e.g.
// on a home server) and gohort-desktop on their Mac. The desktop
// registers local tools (filesystem.read_local_file, eventually
// notify / screenshot / shell). Without this bridge those tools are
// only reachable from the in-window JS bridge — agents running on
// the remote server can't call them. The bridge fills that gap:
//
//   1. Desktop opens GET /api/desktop/ws with the gohort_session cookie.
//   2. Server validates the cookie → user.
//   3. Desktop announces its tool catalog with `{type:"announce",...}`.
//   4. Server registers each tool as a DesktopChatTool under that user
//      and the agent loop picks them up via LocalToolsForUser(user).
//   5. When the LLM calls one, Run() sends `{type:"invoke",id,name,args}`
//      over the same WS and blocks waiting for `{type:"result",id,...}`.
//
// Concurrency model: each desktop connection runs a read pump + a
// write pump. Pending invocations live in a map keyed by request ID;
// Run() registers itself + waits on a channel that the read pump
// signals when the matching result arrives.
//
// Failure modes:
//   - WS disconnect → client removed from registry; in-flight Run()
//     calls receive an error.
//   - WS slow → Run() respects a per-call deadline (default 60s);
//     long-running tools should ack quickly and stream progress
//     separately (not in this MVP).
//   - Multiple connections for the same user → both registered;
//     tool lookup uses the most-recent.

package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// desktopInvokeDeadline caps how long an LLM tool call can wait
// on the desktop side. Most local tools (read file, take
// screenshot, show notification) return in under a second; 60s
// is generous headroom without making the agent loop stall
// forever on a hung client.
func desktopInvokeDeadline() time.Duration { return TuneDuration("tune_desktop_invoke_deadline") }

// desktopPingInterval keeps idle WS connections alive through
// reverse proxies / load balancers (nginx default 60s,
// CloudFlare 100s).
func desktopPingInterval() time.Duration { return TuneDuration("tune_desktop_ping_interval") }

// desktopReadDeadline — drop a connection that hasn't sent any
// frame (data or pong) in this long. Should be > ping interval
// + reasonable RTT.
func desktopReadDeadline() time.Duration { return TuneDuration("tune_desktop_read_deadline") }

func init() {
	RegisterTunable(TunableSpec{Key: "tune_desktop_invoke_deadline", Category: "Timeouts", Label: "Desktop tool invoke deadline", Help: "Max wait for a desktop-bridge tool call before the agent loop gives up on the client.", Kind: KindSeconds, Default: 60, Min: 10, Max: 300})
	RegisterTunable(TunableSpec{Key: "tune_desktop_ping_interval", Category: "Timeouts", Label: "Desktop WS ping interval", Help: "Keepalive ping cadence for idle desktop-bridge WebSocket connections.", Kind: KindSeconds, Default: 25, Min: 5, Max: 120})
	RegisterTunable(TunableSpec{Key: "tune_desktop_read_deadline", Category: "Timeouts", Label: "Desktop WS read deadline", Help: "Drop a desktop-bridge connection with no frame for this long; keep above the ping interval.", Kind: KindSeconds, Default: 70, Min: 15, Max: 360})
}

// DesktopToolDescriptor is the wire shape for one tool the desktop
// has registered locally. Mirrors gohort-desktop/core/tool.go's
// shape — the server doesn't care what implements it on the desktop
// side, only what to surface to the LLM.
type DesktopToolDescriptor struct {
	Name     string               `json:"name"`
	Desc     string               `json:"desc"`
	Params   map[string]ToolParam `json:"params"`
	Required []string             `json:"required"`
}

// announceMsg is sent by the desktop right after connecting. Carries
// the full local-tool catalog. Repeat announces overwrite (a desktop
// that loaded a new tool plugin can re-announce without reconnecting).
type announceMsg struct {
	Type  string                  `json:"type"`
	Tools []DesktopToolDescriptor `json:"tools"`
}

// invokeMsg is sent by the server to the desktop to invoke one of
// the announced tools. ID is correlation; results carry the same.
type invokeMsg struct {
	Type string         `json:"type"`
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// resultMsg is sent by the desktop in response to an invokeMsg.
// Error is set when the tool raised; Result is empty in that case.
type resultMsg struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

// DesktopMCPServer describes a LOCAL MCP server the daemon should host — a
// stdio subprocess on the user's own machine. Same shape as the daemon's
// mcp.json entry (Claude-Desktop-compatible). Pushed by the server via an
// install frame so a connector can add a desktop capability without the user
// hand-editing mcp.json.
type DesktopMCPServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// DesktopCommand describes a DECLARED-COMMAND capability the daemon should host:
// a fixed executable run per tool-call with {placeholder} args filled from the
// call, stdout returned. The lightweight sibling of DesktopMCPServer — for a
// local capability that doesn't warrant a whole MCP server.
type DesktopCommand struct {
	Desc     string               `json:"desc,omitempty"`
	Command  string               `json:"command"`
	Args     []string             `json:"args,omitempty"` // may contain {placeholder} tokens
	Params   map[string]ToolParam `json:"params,omitempty"`
	Required []string             `json:"required,omitempty"`
}

// DesktopBridge describes a BUILT-IN messaging bridge the daemon should turn on
// (e.g. iMessage). Unlike Servers/Commands there is NO command to run — the
// bridge implementation is compiled into the daemon; the server only enables it
// and sets its poll interval. Keyed by service id ("imessage") in the install
// payload — there's one relay per service.
type DesktopBridge struct {
	PollSecs int `json:"poll_secs,omitempty"`
}

// DesktopInstall is the capability-install payload pushed to a user's desktop:
// host the MCP Servers and declared Commands, enable the built-in Bridges, and
// drop the ones named in the Remove* lists. One struct so a connector of any
// desktop kind uses one path.
type DesktopInstall struct {
	Servers        map[string]DesktopMCPServer `json:"servers,omitempty"`
	Commands       map[string]DesktopCommand   `json:"commands,omitempty"`
	Bridges        map[string]DesktopBridge    `json:"bridges,omitempty"`          // keyed by SERVICE id ("imessage")
	Remove         []string                    `json:"remove,omitempty"`          // MCP server names
	RemoveCommands []string                    `json:"remove_commands,omitempty"` // command names
	RemoveBridges  []string                    `json:"remove_bridges,omitempty"`  // SERVICE ids to disable
}

// desktopInstallMsg is sent by the SERVER to a desktop to apply a DesktopInstall.
// The daemon persists the change (mcp.json / commands.json) and brings the
// capability up/down at runtime, then re-announces the fresh catalog. Applying
// is gated by the daemon's own user-consent layer — the server proposes, the
// machine's owner disposes.
type desktopInstallMsg struct {
	Type string `json:"type"` // "install"
	DesktopInstall
}

// desktopClient is one live connection from a gohort-desktop.
type desktopClient struct {
	user string
	conn *websocket.Conn

	mu       sync.Mutex
	tools    []DesktopToolDescriptor
	pending  map[string]chan resultMsg
	nextID   uint64
	closed   bool
	writeMu  sync.Mutex // serialize concurrent writes (WS conns aren't goroutine-safe for writes)
	closedAt time.Time
}

// desktopRegistry holds per-user connected clients. Reads
// (LocalToolsForUser, lookups) are the hot path; writes
// (connect/disconnect) are rare, so a single RWMutex is fine.
type desktopRegistry struct {
	mu     sync.RWMutex
	byUser map[string][]*desktopClient
}

var desktopReg = &desktopRegistry{byUser: map[string][]*desktopClient{}}

var desktopUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     desktopCheckOrigin,
}

// desktopCheckOrigin gates the WebSocket handshake against cross-site
// hijacking (CSWSH). This endpoint is cookie-authenticated, so an allow-all
// origin check would let any page in the viewer's browser open an
// authenticated socket. It rejects ONLY a genuine cross-site http(s) browser
// origin; every other case is allowed so legitimate clients keep working:
//   - no Origin header → native client (the headless bridge daemon, WS
//     libraries); no ambient cookie, so no CSWSH risk.
//   - "null" / file:// / custom-scheme origin → a local webview shell, not a
//     cross-site web page.
//   - loopback origin → local webview (e.g. Wails' wails.localhost).
//   - same-origin (Origin host == the server's Host).
//
// SameSite=Lax on the session cookie is the primary control; this is
// defense-in-depth for the residual gaps.
func desktopCheckOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return true
	}
	if isLoopbackHost(u.Hostname()) {
		return true
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.EqualFold(u.Hostname(), host)
}

// --- API-key authentication hook ---
//
// The desktop bridge normally authenticates via the gohort_session
// cookie (the viewer logs in through its webview). The headless
// gohort-bridge daemon has no cookie — it authenticates with an
// X-API-Key header instead, the same key it uses for its other
// server endpoints (e.g. phantom's /api/hook).
//
// core/ must not import app packages, so an app that issues API keys
// registers a validator here once its key store is live. The desktop
// WS mount (core/webapp.go) resolves the user by trying the cookie
// first, then walking these validators against the X-API-Key header.
// A validator returns the gohort username the key belongs to (the WS
// bridge is per-user) and ok=false when the key is unknown.
var (
	apiKeyValidatorsMu sync.RWMutex
	apiKeyValidators   []func(key string) (user string, ok bool)
)

// RegisterAPIKeyValidator adds a key→user resolver consulted by
// userFromAPIKey. Apps call this once at route-registration time
// (when their key store is live, not at init() — the DB may be nil
// then). Validators are tried in registration order; first match wins.
func RegisterAPIKeyValidator(fn func(key string) (user string, ok bool)) {
	if fn == nil {
		return
	}
	apiKeyValidatorsMu.Lock()
	apiKeyValidators = append(apiKeyValidators, fn)
	apiKeyValidatorsMu.Unlock()
}

// userFromAPIKey resolves the X-API-Key header to a username via the
// registered validators. Returns "" when the header is absent or no
// validator recognizes the key.
func userFromAPIKey(r *http.Request) string {
	key := r.Header.Get("X-API-Key")
	if key == "" {
		return ""
	}
	apiKeyValidatorsMu.RLock()
	defer apiKeyValidatorsMu.RUnlock()
	for _, fn := range apiKeyValidators {
		if u, ok := fn(key); ok && u != "" {
			return u
		}
	}
	return ""
}

// DesktopClientUser resolves the X-Gohort-Desktop-Client-Key header to a
// username, proving the request came from the gohort-desktop VIEWER on the
// same machine as the user's bridge (the viewer's reverse proxy stamps its
// API key into this header). It gates the from_client.* tool surface so the
// local machine's capabilities (filesystem, screenshot, contacts) are
// reachable ONLY from the desktop app — never from a remote browser or phone
// logged into the same account, even with auto-approve on. Returns "" when
// the header is absent or unrecognized.
//
// Deliberately a DISTINCT header from X-API-Key: a normal API request
// shouldn't silently gain local-machine tools; this surface is opt-in by the
// desktop proxy alone.
func DesktopClientUser(r *http.Request) string {
	key := r.Header.Get("X-Gohort-Desktop-Client-Key")
	if key == "" {
		return ""
	}
	apiKeyValidatorsMu.RLock()
	defer apiKeyValidatorsMu.RUnlock()
	for _, fn := range apiKeyValidators {
		if u, ok := fn(key); ok && u != "" {
			return u
		}
	}
	return ""
}

// DesktopBridgeUserOf is the auth resolver the desktop WS mount uses:
// cookie session first (the viewer's logged-in webview), then the
// X-API-Key header (the headless daemon). Returning "" rejects the
// connection.
func DesktopBridgeUserOf(r *http.Request) string {
	if u := AuthCurrentUser(r); u != "" {
		return u
	}
	return userFromAPIKey(r)
}

// HandleDesktopBridge is the WS endpoint handler. Mount at
// /api/desktop/ws via the same auth middleware that protects the
// rest of the app — by the time we get here the user is resolved.
//
// userOf returns the authenticated username for the request; pass
// in whatever helper the surrounding webapp uses (gohort's
// AuthSessionFromRequest, etc.). Returning empty rejects the
// connection.
func HandleDesktopBridge(userOf func(r *http.Request) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := userOf(r)
		if user == "" {
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
		conn, err := desktopUpgrader.Upgrade(w, r, nil)
		if err != nil {
			Warn("[desktop-bridge] upgrade for user=%s failed: %v", user, err)
			return
		}
		client := &desktopClient{
			user:    user,
			conn:    conn,
			pending: map[string]chan resultMsg{},
		}
		desktopReg.add(client)
		Log("[desktop-bridge] connected user=%s remote=%s", user, r.RemoteAddr)

		// Read deadline + pong handler keep the connection alive.
		// Each pong from the client extends the read deadline; if
		// no pong arrives within the window, ReadMessage errors and
		// the loop exits, cleaning up.
		conn.SetReadDeadline(time.Now().Add(desktopReadDeadline()))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(desktopReadDeadline()))
			return nil
		})

		// Background ping loop — fires until conn closes.
		stop := make(chan struct{})
		go client.pingLoop(stop)

		defer func() {
			close(stop)
			desktopReg.remove(client)
			client.markClosed()
			conn.Close()
			Log("[desktop-bridge] disconnected user=%s", user)
		}()

		client.readPump()
	}
}

func (c *desktopClient) pingLoop(stop <-chan struct{}) {
	t := time.NewTicker(desktopPingInterval())
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			c.writeMu.Lock()
			err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			c.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (c *desktopClient) readPump() {
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &head); err != nil {
			continue
		}
		switch head.Type {
		case "announce":
			var m announceMsg
			if err := json.Unmarshal(data, &m); err != nil {
				continue
			}
			c.mu.Lock()
			c.tools = m.Tools
			c.mu.Unlock()
			// Persist the surface so the agent's catalog stays stable
			// across desktop disconnect/reconnect. LocalToolsForUser
			// merges live + persisted; calls to an offline tool return
			// a clean "open your desktop" error.
			persistDesktopKnownTools(c.user, m.Tools)
			names := make([]string, 0, len(m.Tools))
			for _, t := range m.Tools {
				names = append(names, t.Name)
			}
			Log("[desktop-bridge] user=%s announced %d tool(s): %v", c.user, len(m.Tools), names)
		case "result":
			var m resultMsg
			if err := json.Unmarshal(data, &m); err != nil {
				continue
			}
			c.mu.Lock()
			ch, ok := c.pending[m.ID]
			if ok {
				delete(c.pending, m.ID)
			}
			c.mu.Unlock()
			if ok {
				select {
				case ch <- m:
				default:
				}
			}
		default:
			// Unknown — ignore. Forward-compat for future message types.
		}
	}
}

// markClosed signals every in-flight Run() that the connection died
// so they don't block forever waiting for a result that won't come.
func (c *desktopClient) markClosed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.closedAt = time.Now()
	for id, ch := range c.pending {
		select {
		case ch <- resultMsg{ID: id, Error: "desktop client disconnected"}:
		default:
		}
		delete(c.pending, id)
	}
}

// invoke fires a tool call over the WS and waits for the matching
// result. The per-call deadline guards against a hung desktop or
// lost result frame.
func (c *desktopClient) invoke(name string, args map[string]any) (string, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return "", errors.New("desktop disconnected")
	}
	c.nextID++
	id := fmt.Sprintf("%d", c.nextID)
	ch := make(chan resultMsg, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	frame, _ := json.Marshal(invokeMsg{Type: "invoke", ID: id, Name: name, Args: args})
	c.writeMu.Lock()
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := c.conn.WriteMessage(websocket.TextMessage, frame)
	c.writeMu.Unlock()
	if err != nil {
		return "", fmt.Errorf("send to desktop: %w", err)
	}

	select {
	case res := <-ch:
		if res.Error != "" {
			return "", errors.New(res.Error)
		}
		return res.Result, nil
	case <-time.After(desktopInvokeDeadline()):
		return "", fmt.Errorf("desktop tool %q timed out after %s", name, desktopInvokeDeadline())
	}
}

// writeFrame sends a pre-marshaled control frame (e.g. an install) to the
// desktop, serialized against concurrent writes. Unlike invoke it doesn't wait
// for a reply — install is applied asynchronously on the daemon.
func (c *desktopClient) writeFrame(frame []byte) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("desktop disconnected")
	}
	c.mu.Unlock()
	c.writeMu.Lock()
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := c.conn.WriteMessage(websocket.TextMessage, frame)
	c.writeMu.Unlock()
	return err
}

// InstallToDesktop pushes a capability install to a user's connected desktop
// bridge(s). Returns the number of desktop connections the frame reached.
// Errors when the user has no desktop online — the caller (a desktop_* connector)
// surfaces that so an admin retries once the user's app is running. The daemon
// still gates applying the install behind its own consent layer.
func InstallToDesktop(user string, inst DesktopInstall) (int, error) {
	clients := desktopReg.clientsFor(user)
	if len(clients) == 0 {
		return 0, fmt.Errorf("no connected desktop bridge for user %q — the user must have the gohort desktop app running to install a desktop capability", user)
	}
	frame, err := json.Marshal(desktopInstallMsg{Type: "install", DesktopInstall: inst})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range clients {
		if err := c.writeFrame(frame); err != nil {
			Warn("[desktop-bridge] install to user=%s failed on one conn: %v", user, err)
			continue
		}
		n++
	}
	if n == 0 {
		return 0, fmt.Errorf("failed to deliver install to any desktop connection for %q", user)
	}
	return n, nil
}

// add/remove maintain the per-user list. The list shape (not single
// pointer) lets multiple desktops connect for the same user — each
// just adds another callable surface.
func (r *desktopRegistry) add(c *desktopClient) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byUser[c.user] = append(r.byUser[c.user], c)
}

func (r *desktopRegistry) remove(c *desktopClient) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.byUser[c.user]
	out := list[:0]
	for _, k := range list {
		if k != c {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		delete(r.byUser, c.user)
	} else {
		r.byUser[c.user] = out
	}
}

func (r *desktopRegistry) clientsFor(user string) []*desktopClient {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := r.byUser[user]
	out := make([]*desktopClient, len(list))
	copy(out, list)
	return out
}

// desktopKnownToolsTable persists the per-user tool surface across
// disconnects. Written on every announce; read on LocalToolsForUser
// when no live clients are present so the LLM still sees the surface
// (and gets a clean "open your desktop" error if it calls one offline)
// instead of having tools silently appear and disappear from its
// catalog as the user opens / closes the desktop client.
const desktopKnownToolsTable = "desktop_known_tools"

// persistDesktopKnownTools stores the connected desktop's announced
// tool descriptors. Writes to RootDB so the data survives process
// restarts and reaches every chat surface (desktop, browser, phantom)
// uniformly.
//
// The announce is AUTHORITATIVE: it replaces the prior set rather than
// unioning with it. An earlier version merged-to-avoid-shrinking (so a
// partial plugin load wouldn't drop tools), but the desktop announces
// its FULL catalog on connect, and the union meant a tool REMOVED from
// a new build (e.g. a capability ripped out) lingered in the persisted
// surface forever and kept being offered to the LLM. Replace fixes that
// while preserving offline stability: the last announce persists for
// the disconnect window, so the catalog doesn't flicker as the desktop
// comes and goes — it only changes when the desktop says it changed.
func persistDesktopKnownTools(user string, tools []DesktopToolDescriptor) {
	if user == "" || RootDB == nil {
		return
	}
	RootDB.Set(desktopKnownToolsTable, user, tools)
}

// loadDesktopKnownTools returns the persisted tool surface for the
// user. Nil when no record exists.
func loadDesktopKnownTools(user string) []DesktopToolDescriptor {
	if user == "" || RootDB == nil {
		return nil
	}
	var out []DesktopToolDescriptor
	RootDB.Get(desktopKnownToolsTable, user, &out)
	return out
}

// LocalToolsForUser returns ChatTool wrappers for the user's desktop
// tool surface. Always returns the same surface whether the desktop
// is currently connected or not — agents see a stable catalog instead
// of tools appearing / disappearing as the desktop comes and goes.
// At call time, the wrapper resolves a live client; if none is
// connected, the call returns a clean "your gohort-desktop isn't
// connected" error the LLM can relay to the user.
//
// Returns nil only when the user has NEVER registered a desktop
// (no live clients AND nothing persisted).
//
// Tool names are prefixed with "from_client." so they're easy to
// distinguish in the LLM-visible catalog and don't collide with
// server-side tools.
func LocalToolsForUser(user string) []ChatTool {
	clients := desktopReg.clientsFor(user)
	// Walk live tools newest-client-first so the latest connection's
	// schema wins for any name collision.
	seen := map[string]DesktopToolDescriptor{}
	for i := len(clients) - 1; i >= 0; i-- {
		c := clients[i]
		c.mu.Lock()
		for _, t := range c.tools {
			if _, dup := seen[t.Name]; dup {
				continue
			}
			seen[t.Name] = t
		}
		c.mu.Unlock()
	}
	// Layer persisted tools on top — covers the "desktop was here,
	// now offline" case so the agent still sees the catalog.
	for _, t := range loadDesktopKnownTools(user) {
		if _, dup := seen[t.Name]; dup {
			continue
		}
		seen[t.Name] = t
	}
	if len(seen) == 0 {
		return nil
	}
	// Emit in NAME-SORTED order. `seen` is a map, so a raw range would
	// order the from_client.* tools randomly every call — and tool ORDER
	// (not just per-tool schema) is part of the prompt the worker caches,
	// so a shuffling catalog breaks the prompt-cache prefix and forces a
	// full re-prefill every turn (the Chat-only cache thrash: only the
	// desktop client surfaces these tools). Sorting keeps the catalog
	// byte-stable across turns.
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ChatTool, 0, len(seen))
	for _, name := range names {
		out = append(out, &desktopChatTool{user: user, desc: seen[name]})
	}
	return out
}

// desktopChatTool implements core.ChatTool by proxying Run() over
// whichever live WS connection the user has at call time. Name is
// rewritten with the "from_client." prefix so the LLM sees a
// clearly-distinct surface.
//
// Resolves the client at CALL time (not registration time) so the
// catalog stays stable even as the desktop disconnects/reconnects.
// An offline tool call returns a clean error the LLM can relay; the
// catalog doesn't churn.
type desktopChatTool struct {
	user string
	desc DesktopToolDescriptor
}

func (t *desktopChatTool) Name() string {
	return "from_client." + t.desc.Name
}

func (t *desktopChatTool) Desc() string {
	return t.desc.Desc + " (runs on the user's gohort-desktop client — works when the desktop is connected; returns a clean error otherwise)"
}

func (t *desktopChatTool) Params() map[string]ToolParam {
	return t.desc.Params
}

func (t *desktopChatTool) Run(args map[string]any) (string, error) {
	clients := desktopReg.clientsFor(t.user)
	if len(clients) == 0 {
		return "", fmt.Errorf("your gohort-desktop client isn't connected — open it (it's the gohort app on your Mac / Windows) and try again. The %q tool runs there, not on the server", t.desc.Name)
	}
	// Newest-connected wins (matches LocalToolsForUser's dedup order
	// for live announces).
	return clients[len(clients)-1].invoke(t.desc.Name, args)
}
