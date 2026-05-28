// Sandbox hook: a per-dispatch Unix domain socket that lets a script
// running inside the no-network bwrap sandbox call back into gohort
// for narrow, capability-gated operations (HTTP fetch, log, etc.).
//
// Why UDS works inside --unshare-net: a Unix domain socket lives in
// the filesystem namespace, not the network namespace. The sandbox's
// workspace dir is bind-mounted into the sandbox at the same path it
// has on the host, so a socket file gohort listens on at
// <workspace>/.gohort_hook_<token>.sock is reachable from inside via
// the same path. socket.AF_UNIX connections work normally.
//
// Wire format: newline-delimited JSON over a connection-per-call
// socket. Client opens, sends ONE line of JSON, reads ONE line of
// JSON, closes. Keeps the server logic trivially stateless.
//
//	request:  {"method": "...", "params": {...}}\n
//	response: {"result": ...}\n   or   {"error": "..."}\n
//
// Capability gating: only methods listed in the tool's
// HookCapabilities are answered; others return a clear error. Empty
// HookCapabilities ⇒ no hook is started at all (no socket file, no
// env var). This keeps the privacy posture clean — a tool without
// declared capabilities has zero surface area to gohort, same as
// before the hook existed.

package core

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SandboxHook is the per-dispatch UDS server. SocketPath is the
// listener's filesystem path; expose it to the sandbox via the env
// var GOHORT_HOOK_PATH. Capabilities is the allowed-method list —
// only these are dispatched, everything else returns an error.
type SandboxHook struct {
	SocketPath   string
	Capabilities []string

	listener net.Listener
	wg       sync.WaitGroup
	closed   chan struct{}
}

// NewSandboxHook starts a hook listener inside workspaceDir. Returns
// nil (no error) when capabilities is empty — caller treats this as
// "no hook attached, no env var to set."
//
// The socket file is created under workspaceDir so it inherits the
// bind-mount that already exists for the sandbox. The path includes
// a random token so concurrent dispatches sharing the same workspace
// don't collide.
//
// Caller MUST call Close() after the dispatch returns. Close removes
// the socket file and waits for in-flight handlers to drain.
func NewSandboxHook(workspaceDir string, capabilities []string) (*SandboxHook, error) {
	if len(capabilities) == 0 {
		return nil, nil
	}
	if workspaceDir == "" {
		return nil, fmt.Errorf("sandbox hook needs a workspace dir to bind under")
	}
	token, err := randomHookToken()
	if err != nil {
		return nil, fmt.Errorf("hook token: %w", err)
	}
	path := filepath.Join(workspaceDir, ".gohort_hook_"+token+".sock")
	// Pre-cleanup any stale file at this path. With a fresh random
	// token the collision is astronomically unlikely, but a leftover
	// from a hard-killed prior process would otherwise wedge net.Listen.
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("hook listen %s: %w", path, err)
	}
	// Tighten perms — only the gohort process user can connect.
	// Inside the sandbox the script runs as the same UID (bwrap
	// inherits), so it can still open it; an unrelated user on the
	// host can't.
	_ = os.Chmod(path, 0600)
	h := &SandboxHook{
		SocketPath:   path,
		Capabilities: append([]string(nil), capabilities...),
		listener:     l,
		closed:       make(chan struct{}),
	}
	h.wg.Add(1)
	go h.acceptLoop()
	Debug("[hook] listening at %s caps=%v", path, capabilities)
	return h, nil
}

// Close tears down the listener, removes the socket file, and waits
// for in-flight handlers. Safe to call on a nil receiver.
func (h *SandboxHook) Close() {
	if h == nil {
		return
	}
	select {
	case <-h.closed:
		return // already closed
	default:
		close(h.closed)
	}
	_ = h.listener.Close()
	h.wg.Wait()
	_ = os.Remove(h.SocketPath)
	Debug("[hook] closed %s", h.SocketPath)
}

func (h *SandboxHook) acceptLoop() {
	defer h.wg.Done()
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			select {
			case <-h.closed:
				return // expected on Close
			default:
				Debug("[hook] accept error: %v", err)
				return
			}
		}
		h.wg.Add(1)
		go func(c net.Conn) {
			defer h.wg.Done()
			h.handleConn(c)
		}(conn)
	}
}

// handleConn reads one JSON request, dispatches it, writes one JSON
// response, closes the connection. A 60s deadline guards against a
// malformed/stalled script holding the connection open forever.
func (h *SandboxHook) handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	br := bufio.NewReader(conn)
	line, err := br.ReadBytes('\n')
	if err != nil && err != io.EOF {
		Debug("[hook] read error: %v", err)
		return
	}
	var req struct {
		Method string                 `json:"method"`
		Params map[string]interface{} `json:"params"`
	}
	if err := json.Unmarshal(line, &req); err != nil {
		writeHookError(conn, "invalid JSON request: "+err.Error())
		return
	}
	if !h.granted(req.Method) {
		writeHookError(conn, fmt.Sprintf("method %q not granted; capabilities=%v", req.Method, h.Capabilities))
		return
	}
	Debug("[hook] %s(params=%v)", req.Method, paramKeySummary(req.Params))
	switch req.Method {
	case "fetch":
		handleHookFetch(conn, req.Params)
	case "log":
		handleHookLog(conn, req.Params)
	case "secret":
		h.handleSecret(conn, req.Params)
	default:
		writeHookError(conn, "unknown method: "+req.Method)
	}
}

// granted is the first-pass gate: does any capability entry name this
// method? Matches both bare ("fetch") and qualified ("secret:openweather")
// forms — the per-method handler does the qualifier check when needed
// (handleSecret checks the specific name).
func (h *SandboxHook) granted(method string) bool {
	prefix := method + ":"
	for _, c := range h.Capabilities {
		if c == method || strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// grantedSecret enforces per-credential gating. Only entries of the
// exact form "secret:<name>" grant access — bare "secret" is not
// honored even if it slipped past authoring, so a misconfigured tool
// fails closed rather than silently exposing every credential.
func (h *SandboxHook) grantedSecret(name string) bool {
	needle := "secret:" + name
	for _, c := range h.Capabilities {
		if c == needle {
			return true
		}
	}
	return false
}

// --- method handlers ---

// handleHookFetch proxies an HTTP request through gohort's network
// stack. Capped at 10MiB response and 30s default timeout — the
// sandbox can't escalate either. Same-origin / allowlist filtering
// is NOT applied here (yet) — phase 2 if/when an audit log + URL
// allowlist policy lands.
func handleHookFetch(conn net.Conn, params map[string]interface{}) {
	url, _ := params["url"].(string)
	if url == "" {
		writeHookError(conn, "fetch requires url")
		return
	}
	method := "GET"
	if m, ok := params["method"].(string); ok && m != "" {
		method = strings.ToUpper(m)
	}
	var body io.Reader
	if b, ok := params["body"].(string); ok && b != "" {
		body = strings.NewReader(b)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		writeHookError(conn, "build request: "+err.Error())
		return
	}
	if hh, ok := params["headers"].(map[string]interface{}); ok {
		for k, v := range hh {
			if vs, ok := v.(string); ok {
				req.Header.Set(k, vs)
			}
		}
	}
	timeout := 30 * time.Second
	if t, ok := params["timeout"].(float64); ok && t > 0 && t < 300 {
		timeout = time.Duration(t * float64(time.Second))
	}
	client := &http.Client{Timeout: timeout}
	Log("[hook/fetch] %s %s (timeout=%s)", method, url, timeout)
	resp, err := client.Do(req)
	if err != nil {
		writeHookError(conn, "fetch: "+err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		writeHookError(conn, "read response: "+err.Error())
		return
	}
	headers := make(map[string]string, len(resp.Header))
	for k, vs := range resp.Header {
		if len(vs) > 0 {
			headers[k] = vs[0]
		}
	}
	writeHookResult(conn, map[string]interface{}{
		"status":  resp.StatusCode,
		"headers": headers,
		"body":    string(respBody),
	})
}

// handleHookLog routes script-side log messages through gohort's log
// stream. Level maps to Debug / Log / Err so a misbehaving script
// surfaces in the right channel.
func handleHookLog(conn net.Conn, params map[string]interface{}) {
	msg, _ := params["msg"].(string)
	if msg == "" {
		writeHookError(conn, "log requires msg")
		return
	}
	level := strings.ToLower(strings.TrimSpace(stringFromParams(params, "level")))
	switch level {
	case "error", "err":
		Err(fmt.Errorf("[hook/script] %s", msg))
	case "warn", "warning":
		Log("[hook/script WARN] %s", msg)
	case "debug":
		Debug("[hook/script] %s", msg)
	default:
		Log("[hook/script] %s", msg)
	}
	writeHookResult(conn, map[string]bool{"ok": true})
}

// handleSecret looks up a registered credential's decrypted secret
// and returns it as a string. The credential name must be explicitly
// listed in the tool's HookCapabilities as "secret:<name>" — bare
// "secret" without a name doesn't grant access. Audit-logged at Log
// level (the value itself is NEVER logged, only the credential name).
//
// Credentials of type "no_auth" have no stored secret by design;
// they're rejected with a clear message rather than returning an
// empty string (which the script would then silently inject into a
// header, producing a confusing 401 from the real API).
func (h *SandboxHook) handleSecret(conn net.Conn, params map[string]interface{}) {
	name, _ := params["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		writeHookError(conn, "secret requires name")
		return
	}
	if !h.grantedSecret(name) {
		Log("[hook/secret] DENIED %q (not in capabilities=%v)", name, h.Capabilities)
		writeHookError(conn, fmt.Sprintf("secret %q not granted to this tool — add \"secret:%s\" to hook_capabilities", name, name))
		return
	}
	sec := Secure()
	cred, ok := sec.Load(name)
	if !ok {
		writeHookError(conn, fmt.Sprintf("no credential named %q registered — register it via the admin UI first", name))
		return
	}
	if cred.Type == SecureCredNone {
		writeHookError(conn, fmt.Sprintf("credential %q is no_auth — it has no stored secret to return", name))
		return
	}
	secret, ok := sec.loadSecret(name)
	if !ok || secret == "" {
		writeHookError(conn, fmt.Sprintf("credential %q has no stored secret value (was it created without one?)", name))
		return
	}
	Log("[hook/secret] %q (type=%s, %dB) → script", name, cred.Type, len(secret))
	writeHookResult(conn, map[string]string{"secret": secret, "type": cred.Type})
}

// --- wire helpers ---

func writeHookResult(conn net.Conn, result interface{}) {
	b, _ := json.Marshal(map[string]interface{}{"result": result})
	_, _ = conn.Write(append(b, '\n'))
}

func writeHookError(conn net.Conn, msg string) {
	b, _ := json.Marshal(map[string]string{"error": msg})
	_, _ = conn.Write(append(b, '\n'))
}

func stringFromParams(p map[string]interface{}, key string) string {
	if v, ok := p[key].(string); ok {
		return v
	}
	return ""
}

func paramKeySummary(p map[string]interface{}) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	return strings.Join(keys, ",")
}

func randomHookToken() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// --- Python helper ---

// SandboxHookPythonHelper is the source of the `gohort_hook` module
// that gets auto-deployed into a sandboxed workspace whenever the
// tool declares HookCapabilities. Importing it gives the script a
// `gohort` singleton with .fetch(...) and .log(...) methods that
// transparently talk to the per-dispatch UDS. Keeps tool authors
// from having to re-implement the wire protocol every time.
const SandboxHookPythonHelper = `# gohort_hook.py — auto-deployed by gohort.
# Provides a narrow callback channel from this sandboxed script back
# to gohort for capabilities the tool was granted (fetch, log).
#
# Usage:
#     from gohort_hook import gohort
#     data = gohort.fetch("https://api.example.com/x",
#                         headers={"Authorization": "Bearer ..."})
#     print(data["body"])
#
# If GOHORT_HOOK_PATH is not set in env, the tool wasn't granted
# hook capabilities — calls raise HookError with a clear message.

import json
import os
import socket


class HookError(RuntimeError):
    pass


class _Gohort:
    def __init__(self):
        self._path = os.environ.get("GOHORT_HOOK_PATH")

    def _call(self, method, params=None):
        if not self._path:
            raise HookError(
                "GOHORT_HOOK_PATH not set — this tool was not granted hook "
                "capabilities. Re-author the tool with "
                "hook_capabilities=[\"fetch\", ...] to enable."
            )
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            s.settimeout(60)
            s.connect(self._path)
            payload = (json.dumps({"method": method, "params": params or {}})
                       + "\n").encode("utf-8")
            s.sendall(payload)
            buf = b""
            while b"\n" not in buf:
                chunk = s.recv(65536)
                if not chunk:
                    break
                buf += chunk
            line = buf.split(b"\n", 1)[0]
            resp = json.loads(line.decode("utf-8"))
        finally:
            try:
                s.close()
            except Exception:
                pass
        if "error" in resp:
            raise HookError(resp["error"])
        return resp.get("result")

    def fetch(self, url, method="GET", headers=None, body=None, timeout=30):
        """HTTP request via gohort. Returns dict {status, headers, body}.
        body is a string. Capped at 10MiB response."""
        return self._call("fetch", {
            "url": url,
            "method": method,
            "headers": headers or {},
            "body": body or "",
            "timeout": timeout,
        })

    def log(self, msg, level="info"):
        """Route a message into gohort's log stream.
        level: debug, info, warn, error."""
        return self._call("log", {"level": level, "msg": str(msg)})

    def secret(self, name):
        """Return the decrypted secret value for a registered credential.
        The tool must declare "secret:<name>" in hook_capabilities to
        be granted access — otherwise this raises HookError. Use to
        keep API keys / tokens out of script_body: register the secret
        once via the admin UI, then read it here at dispatch time.

        Returns the raw secret string."""
        result = self._call("secret", {"name": name})
        return result["secret"] if isinstance(result, dict) else result


gohort = _Gohort()
`
