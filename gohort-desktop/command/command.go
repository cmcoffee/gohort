// Package command hosts DECLARED-COMMAND tools: a server-pushed capability that
// runs a fixed executable with {placeholder} args filled from the tool call,
// captures stdout, and returns it. The lightweight sibling of the mcp host —
// for a local capability that doesn't warrant a whole MCP server (mirrors how
// gohort's own skills bundle shell scripts).
//
// Registration rides the same runtime path as MCP tools: each command is a
// dynamic registry source ("command:<name>"), so it announces to the server and
// is gated by the same per-invoke approval prompt as every other local tool. No
// shell is involved — exec runs the executable with explicit args, so there is
// no shell-injection surface; placeholders only fill argument VALUES.
package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

// CONFIG_NAME is the declared-command store, in the shared config dir.
const CONFIG_NAME = "commands.json"

const (
	runTimeout = 55 * time.Second // just under the server's desktop-invoke deadline
	maxOutput  = 256 * 1024       // cap a runaway command's output
)

// Spec is one declared command.
type Spec struct {
	Desc     string                    `json:"desc"`
	Command  string                    `json:"command"`
	Args     []string                  `json:"args"` // may contain {placeholder} tokens
	Params   map[string]core.ToolParam `json:"params"`
	Required []string                  `json:"required"`
}

type fileConfig struct {
	Commands map[string]Spec `json:"commands"`
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, core.SETTINGS_DIR_NAME, CONFIG_NAME)
}

// source namespaces a command's dynamic-registry entry.
func source(name string) string { return "command:" + name }

// Start loads persisted declared commands at daemon boot and registers them.
// Safe when no config exists (no-op).
func Start() {
	path := configPath()
	if path == "" {
		return
	}
	cfg := readConfig(path)
	for name, spec := range cfg.Commands {
		register(name, spec)
	}
	if len(cfg.Commands) > 0 {
		core.Log("[command] loaded %d declared command(s)", len(cfg.Commands))
	}
}

// Install registers (or replaces) a declared command and persists it so it
// survives a daemon restart. Registration re-announces the catalog.
func Install(name string, spec Spec) error {
	if strings.TrimSpace(spec.Command) == "" {
		return fmt.Errorf("command is required")
	}
	register(name, spec)
	if err := persist(name, spec); err != nil {
		core.Warn("[command] installed %q but failed to persist: %v", name, err)
	}
	return nil
}

// Remove drops a declared command's tool + persisted entry.
func Remove(name string) error {
	core.ReplaceDynamicTools(source(name), nil)
	return removePersist(name)
}

func register(name string, spec Spec) {
	core.ReplaceDynamicTools(source(name), []core.Tool{newCommandTool(name, spec)})
}

// commandTool adapts a declared command to core.Tool.
type commandTool struct {
	name string
	spec Spec
}

func newCommandTool(name string, spec Spec) *commandTool { return &commandTool{name: name, spec: spec} }

func (t *commandTool) Name() string { return t.name }
func (t *commandTool) Desc() string {
	if strings.TrimSpace(t.spec.Desc) != "" {
		return t.spec.Desc
	}
	return "Runs " + t.spec.Command + " on this machine."
}
func (t *commandTool) Params() map[string]core.ToolParam { return t.spec.Params }
func (t *commandTool) Required() []string                { return t.spec.Required }
func (t *commandTool) Enabled() bool                     { return true }
func (t *commandTool) Handler() core.ToolHandler {
	return func(args map[string]any) (string, error) {
		cmdArgs := make([]string, 0, len(t.spec.Args))
		for _, a := range t.spec.Args {
			cmdArgs = append(cmdArgs, substituteArgs(a, args))
		}
		ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
		defer cancel()
		c := exec.CommandContext(ctx, t.spec.Command, cmdArgs...)
		// Params also arrive as env vars, matching gohort's own shell tools.
		c.Env = append(os.Environ(), argsToEnv(args)...)
		var out bytes.Buffer
		c.Stdout = &out
		c.Stderr = &out
		err := c.Run()
		s := out.String()
		if len(s) > maxOutput {
			s = s[:maxOutput] + "\n[output truncated]"
		}
		if err != nil {
			return "", fmt.Errorf("%s (%v)", strings.TrimSpace(s), err)
		}
		return s, nil
	}
}

// substituteArgs replaces {key} tokens in a command-arg template with the
// tool-call values. No shell is involved, so this only fills VALUES.
func substituteArgs(tmpl string, args map[string]any) string {
	out := tmpl
	for k, v := range args {
		out = strings.ReplaceAll(out, "{"+k+"}", toStr(v))
	}
	return out
}

func argsToEnv(args map[string]any) []string {
	env := make([]string, 0, len(args))
	for k, v := range args {
		env = append(env, k+"="+toStr(v))
	}
	return env
}

func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case float64, int, int64, bool:
		return fmt.Sprintf("%v", t)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// --- commands.json persistence -------------------------------------------

func readConfig(path string) fileConfig {
	var cfg fileConfig
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	if cfg.Commands == nil {
		cfg.Commands = map[string]Spec{}
	}
	return cfg
}

func writeConfig(path string, cfg fileConfig) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func persist(name string, spec Spec) error {
	path := configPath()
	if path == "" {
		return nil
	}
	cfg := readConfig(path)
	cfg.Commands[name] = spec
	return writeConfig(path, cfg)
}

func removePersist(name string) error {
	path := configPath()
	if path == "" {
		return nil
	}
	cfg := readConfig(path)
	delete(cfg.Commands, name)
	return writeConfig(path, cfg)
}
