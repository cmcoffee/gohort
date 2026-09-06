package servitor

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/gorilla/websocket"
)

// ansiEscape matches ANSI/VT100 terminal escape sequences so they can be
// stripped from PTY output before handing text to the LLM.
var ansiEscape = regexp.MustCompile(`\x1b(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])`)

func stripANSI(s string) string {
	s = ansiEscape.ReplaceAllString(s, "")
	// Normalise Windows-style line endings left by PTY output.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}

// termBuffer is the per-(user+appliance) persistent terminal log.
// Every command + output mirrored from a worker session lands here
// regardless of whether anyone is watching the terminal WebSocket;
// when a viewer connects, it gets the buffer's contents replayed
// before live streaming starts. Keeps the terminal pane truthful:
// commands the user missed because they hadn't opened the pane yet
// still show up when they finally do.
//
// Buffer is a byte slice trimmed to termBufferCap bytes — keeps long
// sessions from growing memory unbounded while preserving recent
// activity (256KB ≈ a few thousand lines of normal command output,
// far more than what's interesting to scroll back through).
type termBuffer struct {
	mu      sync.Mutex
	bytes   []byte
	writers map[*termWriterEntry]struct{}
}

// termWriterEntry is one live WebSocket subscribed to a termBuffer.
// Held as a pointer so we can address-match in subscribe/unsubscribe
// without worrying about map-key equality on closure values.
type termWriterEntry struct {
	write func([]byte)
}

func termBufferCap() int { return TuneInt("tune_term_buffer_cap") }

func init() {
	RegisterTunable(TunableSpec{
		Key:      "tune_term_buffer_cap",
		App:      "/servitor",
		Category: "Limits",
		Label:    "Terminal buffer cap (bytes)",
		Help:     "Max bytes retained per live terminal buffer before old output is trimmed.",
		Kind:     KindInt,
		Default:  262144,
		Min:      32768,
		Max:      4194304,
	})
}

// termBufferFor returns (or creates) the buffer for a user+appliance
// pair. Buffers persist for the process lifetime; that's fine — they
// trim themselves, and an idle buffer holding 256KB is negligible.
func termBufferFor(userID, applianceID string) *termBuffer {
	key := userID + ":" + applianceID
	if v, ok := termBuffers.Load(key); ok {
		return v.(*termBuffer)
	}
	tb := &termBuffer{writers: map[*termWriterEntry]struct{}{}}
	if actual, loaded := termBuffers.LoadOrStore(key, tb); loaded {
		return actual.(*termBuffer)
	}
	return tb
}

// append records data in the buffer and returns a snapshot of the
// current writer set. Caller broadcasts to each writer OUTSIDE the
// lock so a slow client can't stall other writers or future appends.
func (tb *termBuffer) append(data []byte) []*termWriterEntry {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.bytes = append(tb.bytes, data...)
	if cap := termBufferCap(); len(tb.bytes) > cap {
		// Trim from the start. Copy into a fresh slice so the dropped
		// prefix's underlying memory is reclaimable.
		fresh := make([]byte, cap)
		copy(fresh, tb.bytes[len(tb.bytes)-cap:])
		tb.bytes = fresh
	}
	writers := make([]*termWriterEntry, 0, len(tb.writers))
	for w := range tb.writers {
		writers = append(writers, w)
	}
	return writers
}

// subscribe registers a writer and returns a copy of the current
// buffer so the caller can replay it to the new subscriber before
// any future appends arrive. Locked so a concurrent append can't
// interleave (subscriber would either miss it or see it twice).
func (tb *termBuffer) subscribe(w *termWriterEntry) []byte {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.writers[w] = struct{}{}
	snap := make([]byte, len(tb.bytes))
	copy(snap, tb.bytes)
	return snap
}

func (tb *termBuffer) unsubscribe(w *termWriterEntry) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	delete(tb.writers, w)
}

// mirrorToTerm appends terminal bytes to the persistent buffer and
// fans out to every connected viewer. Worker code keeps calling this
// unchanged — the only behavior change is that writes no longer drop
// when no terminal is connected.
func mirrorToTerm(userID, applianceID string, data []byte) {
	if len(data) == 0 {
		return
	}
	tb := termBufferFor(userID, applianceID)
	for _, w := range tb.append(data) {
		w.write(data)
	}
}

// terminalPrompt returns a short colored prompt string for the given appliance.
// SSH: green hostname (first label only) followed by $. Command: plain $.
func terminalPrompt(appliance Appliance) string {
	user := appliance.User
	if user == "" {
		user = "root"
	}
	if user == "root" {
		return "\x1b[31m#$\x1b[0m "
	}
	return "\x1b[32m#$\x1b[0m "
}

var wsUpgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	ReadBufferSize:   4096,
	WriteBufferSize:  4096,
	// Same-origin only. This terminal WS is cookie-authenticated (behind
	// AuthMiddleware) and streams a live shell, so a permissive CheckOrigin would
	// allow cross-site WebSocket hijacking — a malicious page opening the socket
	// with the victim's session cookie. It's a browser-only surface with no
	// legitimate cross-origin client, so require the handshake Origin to match the
	// host (SameOriginRequest returns true when Origin is absent, i.e. a
	// non-browser client, which still needs a valid session cookie to get here).
	CheckOrigin: SameOriginRequest,
}

// handleTerminal upgrades to a WebSocket and registers it as the passive agent
// viewer for this appliance. The terminal receives command output mirrored from
// active agent sessions via mirrorToTerm — it is read-only and requires no SSH
// connection of its own.
func (T *Servitor) handleTerminal(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" || udb == nil {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	appliance, _, _, found := T.resolveAppliance(userID, udb, id)
	if !found {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}

	ws, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	var wsMu sync.Mutex
	wsWrite := func(data []byte) {
		wsMu.Lock()
		defer wsMu.Unlock()
		ws.WriteMessage(websocket.BinaryMessage, data)
	}

	// Subscribe to the appliance's persistent terminal buffer. The
	// snapshot returned is everything mirrored so far (capped to
	// termBufferCap bytes); replay it first so the viewer sees prior
	// activity, then live writes pick up where the snapshot ended.
	// Subscribe is synchronized with append, so there's no gap where
	// a write could land between snapshot and live-stream.
	tb := termBufferFor(userID, id)
	entry := &termWriterEntry{write: wsWrite}
	history := tb.subscribe(entry)
	defer tb.unsubscribe(entry)

	// Force-reset the terminal to US ASCII before anything else. Some
	// SSH programs emit DEC SCS (Select Character Set) escapes — e.g.
	// "\x1b(K" for German NRCS or "\x1b(H" for Swedish — that remap
	// ASCII bracket bytes to localized letters ('[' → 'Ä', ']' → 'Å'
	// under Swedish) and never reset them. Once the G0 set is stuck in
	// an NRCS, every subsequent ASCII bracket renders wrong. Three
	// bytes restore sanity: ESC ( B (G0 = US ASCII), ESC ) B (G1 = US
	// ASCII), SI (invoke G0). Idempotent and harmless if the terminal
	// is already in US ASCII mode.
	const charsetReset = "\x1b(B\x1b)B\x0f"

	// Send a brief header so the user can tell at a glance whether
	// they're looking at backfilled history or live streaming. When
	// the buffer is empty it doubles as the "no activity yet" placeholder.
	header := charsetReset + "\x1b[2m── " + appliance.Name + " — "
	if len(history) == 0 {
		header += "waiting for agent activity"
	} else {
		header += "showing prior activity (" + fmt.Sprintf("%d bytes", len(history)) + ")"
	}
	header += " ──\x1b[0m\r\n"
	wsWrite([]byte(header))
	if len(history) > 0 {
		wsWrite(history)
	} else {
		wsWrite([]byte(terminalPrompt(appliance)))
	}

	// Keep the connection open until the client disconnects.
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			return
		}
	}
}
