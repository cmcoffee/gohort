package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

// CONFIG_NAME is the MCP config file, in the shared config dir. Its
// schema matches Claude Desktop's claude_desktop_config.json so existing
// configs paste straight in.
const CONFIG_NAME = "mcp.json"

type config struct {
	MCPServers map[string]serverConfig `json:"mcpServers"`
}

type serverConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, core.SETTINGS_DIR_NAME, CONFIG_NAME)
}

// Start reads mcp.json and brings up every configured server in the
// background, registering each server's tools as it comes online.
// Non-blocking and safe when no config exists (no-op). Returns a
// teardown that kills the subprocesses; call it on bridge shutdown.
func Start() func() {
	path := configPath()
	if path == "" {
		return func() {}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return func() {} // no config → nothing to host
	}
	var cfg config
	if json.Unmarshal(b, &cfg) != nil {
		core.Warn("[mcp] %s is not valid JSON — ignoring", path)
		return func() {}
	}
	if len(cfg.MCPServers) == 0 {
		return func() {}
	}

	for name, sc := range cfg.MCPServers {
		name, sc := name, sc
		go func() {
			srv, err := bringUp(name, sc)
			if err != nil {
				core.Warn("[mcp] server %q failed to start: %v", name, err)
				return
			}
			trackServer(name, srv)
		}()
	}
	return func() {
		running_mu.Lock()
		defer running_mu.Unlock()
		for name, s := range running {
			core.ReplaceDynamicTools(mcpSource(name), nil) // drop its tools from the catalog
			s.close()
			delete(running, name)
		}
	}
}

// running tracks live servers by name so a runtime Install can replace one and
// Remove can tear one down (kill the subprocess + drop its tools).
var (
	running_mu sync.Mutex
	running    = map[string]*server{}
)

// trackServer records a live server, closing any prior one under the same name.
func trackServer(name string, srv *server) {
	running_mu.Lock()
	if old := running[name]; old != nil {
		old.close()
	}
	running[name] = srv
	running_mu.Unlock()
}

// Install brings up (or replaces) one MCP server at runtime and persists it to
// mcp.json so it survives a daemon restart. Its tools register via
// ReplaceDynamicTools, which re-announces the catalog to the server. This is
// the server-push path: a desktop_mcp connector, once approved + user-consented,
// lands here. Applying it means SPAWNING the command — callers must gate on user
// consent first.
func Install(name, command string, args []string, env map[string]string) error {
	sc := serverConfig{Command: command, Args: args, Env: env}
	srv, err := bringUp(name, sc)
	if err != nil {
		return err
	}
	trackServer(name, srv)
	if err := persistServer(name, sc); err != nil {
		core.Warn("[mcp] installed %q but failed to persist mcp.json: %v", name, err)
	}
	return nil
}

// Remove tears down a server (kills the subprocess, drops its tools) and drops
// it from mcp.json. Safe when the server isn't running.
func Remove(name string) error {
	core.ReplaceDynamicTools(mcpSource(name), nil)
	running_mu.Lock()
	if srv := running[name]; srv != nil {
		srv.close()
		delete(running, name)
	}
	running_mu.Unlock()
	return removeServer(name)
}

// --- mcp.json persistence -------------------------------------------------

// readConfig loads mcp.json, tolerating a missing/invalid file (empty result).
func readConfig(path string) config {
	var cfg config
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]serverConfig{}
	}
	return cfg
}

// writeConfig writes mcp.json (pretty-printed, 0600) creating the dir if needed.
func writeConfig(path string, cfg config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func persistServer(name string, sc serverConfig) error {
	path := configPath()
	if path == "" {
		return nil
	}
	cfg := readConfig(path)
	cfg.MCPServers[name] = sc
	return writeConfig(path, cfg)
}

func removeServer(name string) error {
	path := configPath()
	if path == "" {
		return nil
	}
	cfg := readConfig(path)
	delete(cfg.MCPServers, name)
	return writeConfig(path, cfg)
}

// mcpSource namespaces a server's dynamic-registry entry so re-bringing-up the
// same server REPLACES its tools rather than duplicating them (the reload path),
// and so teardown can drop exactly that server's tools.
func mcpSource(name string) string { return "mcp:" + name }

// bringUp starts one server, handshakes, then lists + registers its tools into
// the core registry as a dynamic source (replaceable, so a re-init swaps the
// set cleanly instead of colliding on duplicate names).
func bringUp(name string, sc serverConfig) (*server, error) {
	srv, err := startServer(name, sc.Command, sc.Args, sc.Env)
	if err != nil {
		return nil, err
	}
	if err := srv.initialize(); err != nil {
		srv.close()
		return nil, err
	}
	tools, err := srv.listTools()
	if err != nil {
		srv.close()
		return nil, err
	}
	registered := make([]core.Tool, 0, len(tools))
	for _, def := range tools {
		registered = append(registered, newTool(srv, def))
	}
	core.ReplaceDynamicTools(mcpSource(name), registered)
	core.Log("[mcp] server %q online — %d tool(s)", name, len(tools))
	return srv, nil
}
