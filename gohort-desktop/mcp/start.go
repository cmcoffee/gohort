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

	var mu sync.Mutex
	var servers []*server
	for name, sc := range cfg.MCPServers {
		name, sc := name, sc
		go func() {
			srv, err := bringUp(name, sc)
			if err != nil {
				core.Warn("[mcp] server %q failed to start: %v", name, err)
				return
			}
			mu.Lock()
			servers = append(servers, srv)
			mu.Unlock()
		}()
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range servers {
			s.close()
		}
	}
}

// bringUp starts one server, handshakes, then lists + registers its
// tools into the core registry.
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
	for _, def := range tools {
		safeRegister(newTool(srv, def))
	}
	core.Log("[mcp] server %q online — %d tool(s)", name, len(tools))
	return srv, nil
}

// safeRegister registers a tool, swallowing the duplicate-name panic
// core.RegisterTool raises on a re-registration (the registry has no
// unregister yet — a clean restart/re-register story is a follow-up).
func safeRegister(t core.Tool) {
	defer func() {
		if r := recover(); r != nil {
			core.Warn("[mcp] skip duplicate tool %q", t.Name())
		}
	}()
	core.RegisterTool(t)
}
