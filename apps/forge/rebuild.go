package forge

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// handleStatus returns the current session + config state for the
// DisplayPanel. restart_display renders the restart command or an
// explicit "(disabled)" so the operator can see at a glance that the
// high-blast dial is off.
func (T *Forge) handleStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := T.requireAdmin(w, r); !ok {
		return
	}
	cfg := T.loadConfig()
	session := "stopped"
	if sessionRunning(cfg.TmuxSession) {
		session = "running (" + cfg.TmuxSession + ")"
	}
	restart := strings.TrimSpace(cfg.RestartCmd)
	if restart == "" {
		restart = "(disabled)"
	}
	writeJSON(w, map[string]any{
		"session":         session,
		"work_dir":        cfg.resolvedWorkDir(),
		"claude_cmd":      cfg.ClaudeCmd,
		"rebuild_cmd":     cfg.RebuildCmd,
		"restart_cmd":     cfg.RestartCmd,
		"restart_display": restart,
	})
}

// handleConfig serves the config record (GET) and persists edits (POST).
func (T *Forge) handleConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := T.requireAdmin(w, r); !ok {
		return
	}
	if r.Method == http.MethodPost {
		var in ForgeConfig
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Trim everything; leave RestartCmd exactly as given (blank = disabled).
		in.WorkDir = strings.TrimSpace(in.WorkDir)
		in.ClaudeCmd = strings.TrimSpace(in.ClaudeCmd)
		in.RebuildCmd = strings.TrimSpace(in.RebuildCmd)
		in.RestartCmd = strings.TrimSpace(in.RestartCmd)
		in.TmuxSession = strings.TrimSpace(in.TmuxSession)
		T.saveConfig(in)
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	cfg := T.loadConfig()
	writeJSON(w, cfg)
}

// handleNewSession kills the current tmux session and starts a fresh
// one. The browser reconnects its terminal afterward.
func (T *Forge) handleNewSession(w http.ResponseWriter, r *http.Request) {
	if _, ok := T.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := T.loadConfig()
	killSession(cfg.TmuxSession)
	if err := ensureSession(cfg); err != nil {
		writeJSON(w, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleRebuild runs the build-gate and, only on a green build, fires
// the restart command. This is the one hard safety invariant that is
// NOT a setting: a red build never restarts the server. The restart is
// launched detached (its own session) after the HTTP response is sent,
// so the reply reaches the browser before systemd bounces the process;
// the terminal's reconnect logic re-attaches to the tmux session once
// the new binary is up.
func (T *Forge) handleRebuild(w http.ResponseWriter, r *http.Request) {
	if _, ok := T.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := T.loadConfig()

	// Build gate. Run the rebuild command in the working dir, capturing
	// combined output so the operator sees compiler errors in the
	// terminal.
	build := exec.Command("bash", "-lc", cfg.RebuildCmd)
	build.Dir = cfg.resolvedWorkDir()
	out, err := build.CombinedOutput()
	log := strings.TrimRight(string(out), "\n")

	if err != nil {
		writeJSON(w, map[string]any{
			"ok":      false,
			"log":     log,
			"message": "build failed: " + err.Error(),
		})
		return
	}

	restart := strings.TrimSpace(cfg.RestartCmd)
	if restart == "" {
		writeJSON(w, map[string]any{
			"ok":      true,
			"log":     log,
			"message": "build ok — no restart command configured",
		})
		return
	}

	// Green build + a restart command: reply first, then bounce.
	writeJSON(w, map[string]any{
		"ok":      true,
		"log":     log,
		"message": "build ok — restarting server",
	})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func(cmdline string) {
		time.Sleep(600 * time.Millisecond)
		c := exec.Command("bash", "-lc", cmdline)
		c.Dir = cfg.resolvedWorkDir()
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		_ = c.Start()
	}(restart)
}

// writeJSON is the small JSON responder shared by forge's endpoints.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
