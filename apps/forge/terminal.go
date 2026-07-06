package forge

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"

	"github.com/gorilla/websocket"
	"golang.org/x/sys/unix"

	. "github.com/cmcoffee/gohort/core"
)

// forgeUpgrader is the WebSocket upgrader for the interactive terminal.
// Same-origin only: the socket is cookie-authenticated and streams a
// live shell, so a permissive origin check would allow cross-site
// WebSocket hijacking. SameOriginRequest returns true when Origin is
// absent (non-browser client), which still needs a valid session
// cookie + admin to get here.
var forgeUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     SameOriginRequest,
}

// sessionRunning reports whether the tmux session exists.
func sessionRunning(session string) bool {
	return exec.Command("tmux", "has-session", "-t", session).Run() == nil
}

// ensureSession creates the detached tmux session running the configured
// launch command if it isn't already up. Idempotent. The launch command
// is wrapped so that if claude exits, the session drops to a login shell
// and stays alive — which keeps the tmux session (and this terminal's
// reconnect target) around across a gohort restart and across claude
// quitting.
func ensureSession(cfg ForgeConfig) error {
	if sessionRunning(cfg.TmuxSession) {
		return nil
	}
	launch := cfg.ClaudeCmd + "; exec bash -l"
	args := []string{"new-session", "-d", "-s", cfg.TmuxSession, "-c", cfg.resolvedWorkDir(), "bash", "-lc", launch}
	return exec.Command("tmux", args...).Run()
}

// killSession tears the tmux session down. Used by "New claude session"
// so the next attach re-creates it fresh.
func killSession(session string) {
	_ = exec.Command("tmux", "kill-session", "-t", session).Run()
}

// handleTerminal upgrades to a WebSocket and bridges it to a local PTY
// running `tmux attach` against the claude session. Keystrokes flow
// browser → PTY; output flows PTY → browser. A resize control message
// ({"type":"resize",cols,rows}) sets the PTY window size. Closing the
// socket kills only the attach client, never the session.
func (T *Forge) handleTerminal(w http.ResponseWriter, r *http.Request) {
	if _, ok := T.requireAdmin(w, r); !ok {
		return
	}
	cfg := T.loadConfig()
	if err := ensureSession(cfg); err != nil {
		http.Error(w, "could not start session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	ws, err := forgeUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	master, slave, err := openPTY()
	if err != nil {
		ws.WriteMessage(websocket.BinaryMessage, []byte("\r\n\x1b[31mforge: could not allocate PTY: "+err.Error()+"\x1b[0m\r\n"))
		return
	}
	defer master.Close()
	setWinsize(master, 80, 24)

	cmd := exec.Command("tmux", "attach-session", "-t", cfg.TmuxSession)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		slave.Close()
		ws.WriteMessage(websocket.BinaryMessage, []byte("\r\n\x1b[31mforge: could not attach: "+err.Error()+"\x1b[0m\r\n"))
		return
	}
	slave.Close() // parent keeps only the master side
	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
	}()

	var wsMu sync.Mutex
	wsWrite := func(data []byte) {
		wsMu.Lock()
		defer wsMu.Unlock()
		ws.WriteMessage(websocket.BinaryMessage, data)
	}

	// PTY → browser.
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				wsWrite(buf[:n])
			}
			if err != nil {
				ws.Close()
				return
			}
		}
	}()

	// browser → PTY (with resize control messages).
	type ctrl struct {
		Type string `json:"type"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	for {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if mt == websocket.TextMessage {
			var c ctrl
			if json.Unmarshal(data, &c) == nil && c.Type == "resize" {
				setWinsize(master, c.Cols, c.Rows)
				continue
			}
		}
		master.Write(data)
	}
}

// openPTY allocates a Linux pseudo-terminal pair with no third-party
// dependency: open /dev/ptmx, unlock the slave (TIOCSPTLCK 0), read the
// pts index (TIOCGPTN), and open the matching /dev/pts/N. The master is
// used for read/write; the slave becomes the child's controlling tty.
func openPTY() (master *os.File, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		return nil, nil, err
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	s, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	return m, s, nil
}

// setWinsize applies a terminal window size to the PTY master. No-op on
// non-positive dimensions.
func setWinsize(f *os.File, cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	_ = unix.IoctlSetWinsize(int(f.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
		Row: uint16(rows),
		Col: uint16(cols),
	})
}
