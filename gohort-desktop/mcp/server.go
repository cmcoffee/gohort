// Package mcp is a minimal Model Context Protocol host. It launches
// configured MCP servers (subprocesses speaking JSON-RPC 2.0 over
// stdio), enumerates their tools, and re-exposes each as a core.Tool —
// so the bridge serves them to the gohort server over the WS tool
// bridge, gated by the same approval prompt as every other local tool.
//
// Cross-platform (MCP servers are subprocesses; no cgo). The client side
// is deliberately small — initialize / tools/list / tools/call — to keep
// it dependency-free per the project's minimal-deps ethos.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

const (
	protocolVersion  = "2024-11-05"
	handshakeTimeout = 15 * time.Second
	// callTimeout sits just under the server's 60s desktop-invoke
	// deadline so a slow tool fails here with a clean message first.
	callTimeout = 55 * time.Second
)

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"` // omitted for notifications
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp rpc %d: %s", e.Code, e.Message) }

// server is one live MCP server subprocess.
type server struct {
	name  string
	cmd   *exec.Cmd
	stdin io.WriteCloser

	writeMu sync.Mutex // stdin isn't safe for concurrent writes

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcResponse

	dead atomic.Bool
}

// startServer spawns the MCP server process and starts reading stdout.
func startServer(name, command string, args []string, env map[string]string) (*server, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &server{name: name, cmd: cmd, stdin: stdin, pending: map[int64]chan rpcResponse{}}
	go s.readLoop(stdout)
	go func() { cmd.Wait(); s.markDead() }()
	return s, nil
}

func (s *server) alive() bool { return !s.dead.Load() }

func (s *server) markDead() {
	if s.dead.Swap(true) {
		return
	}
	s.mu.Lock()
	for id, ch := range s.pending {
		close(ch)
		delete(s.pending, id)
	}
	s.mu.Unlock()
	core.Warn("[mcp] server %q exited; its tools are now disabled", s.name)
}

func (s *server) close() {
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
}

// readLoop reads newline-delimited JSON-RPC messages and routes
// responses to waiting callers. Notifications (no id) are ignored.
func (s *server) readLoop(stdout io.Reader) {
	r := bufio.NewReader(stdout)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var resp rpcResponse
			if json.Unmarshal(line, &resp) == nil && resp.ID != 0 {
				s.mu.Lock()
				ch := s.pending[resp.ID]
				delete(s.pending, resp.ID)
				s.mu.Unlock()
				if ch != nil {
					ch <- resp
				}
			}
		}
		if err != nil {
			s.markDead()
			return
		}
	}
}

// call sends a request and waits for the matching response.
func (s *server) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	if s.dead.Load() {
		return nil, fmt.Errorf("mcp server %q is not running", s.name)
	}
	s.mu.Lock()
	// Re-check under the lock: markDead() sets the flag before it closes
	// pending channels, so a call() that slips past the unlocked check
	// above could otherwise register its channel AFTER markDead's close
	// loop has run — that channel would never be closed and the caller
	// would block until timeout instead of failing fast. Checking here,
	// while holding the same lock markDead takes, closes that window.
	if s.dead.Load() {
		s.mu.Unlock()
		return nil, fmt.Errorf("mcp server %q is not running", s.name)
	}
	s.nextID++
	id := s.nextID
	ch := make(chan rpcResponse, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	if err := s.write(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, err
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("mcp server %q closed during %s", s.name, method)
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-time.After(timeout):
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("mcp %q %s timed out", s.name, method)
	}
}

func (s *server) notify(method string, params any) error {
	return s.write(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *server) write(req rpcRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.stdin.Write(b)
	return err
}

// --- MCP method wrappers ---

// initialize performs the MCP handshake.
func (s *server) initialize() error {
	if _, err := s.call("initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "gohort-bridge", "version": "0.1"},
	}, handshakeTimeout); err != nil {
		return err
	}
	return s.notify("notifications/initialized", map[string]any{})
}

// toolDef is one tool as returned by tools/list.
type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (s *server) listTools() ([]toolDef, error) {
	raw, err := s.call("tools/list", map[string]any{}, handshakeTimeout)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []toolDef `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

func (s *server) callTool(name string, args map[string]any) (string, error) {
	raw, err := s.call("tools/call", map[string]any{"name": name, "arguments": args}, callTimeout)
	if err != nil {
		return "", err
	}
	return extractContent(raw)
}
