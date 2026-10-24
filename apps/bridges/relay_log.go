package bridges

// The relay's own log, sent here.
//
// What only the machine running a connector knows (an attachment not yet on
// disk, a row skipped as our own, a chat id that lagged) was in a log on that
// machine and nowhere else, so "did both photos reach the relay" had no answer
// from this side. The relay sends the lines it logged since its last send on
// each poll; they are kept here, a few hundred per bridge, shown to the
// bridge's owner on its row, and written to this server's log.
//
// In memory: a short tail for looking at what just happened, not a record. A
// restart starts it empty, and the relay only sends what is new.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// relayLogKeep is how many lines are kept per bridge.
const relayLogKeep = 300

// relayLogLineMax bounds one line, in runes.
const relayLogLineMax = 2000

type relayLogLine struct {
	At   string `json:"at"`
	Text string `json:"text"`
}

var relayLogs = struct {
	sync.Mutex
	m map[string][]relayLogLine
}{m: map[string][]relayLogLine{}}

// handleRelayLogPost takes a batch of lines from a relay, authenticated by its
// bridge key.
func (T *Bridges) handleRelayLogPost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key, ok := T.validateBridgeKey(r.Header.Get("X-API-Key"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		Lines []relayLogLine `json:"lines"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	lines := body.Lines
	if len(lines) > relayLogKeep {
		lines = lines[len(lines)-relayLogKeep:]
	}
	name := firstNonEmpty(key.Name, key.ID)
	for i := range lines {
		lines[i].Text = clipRelayLine(lines[i].Text)
		Log("[bridges.relay] %s: %s", name, lines[i].Text)
	}
	relayLogs.Lock()
	kept := append(relayLogs.m[key.ID], lines...)
	if len(kept) > relayLogKeep {
		kept = kept[len(kept)-relayLogKeep:]
	}
	relayLogs.m[key.ID] = kept
	relayLogs.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// handleRelayLogView lists a bridge's kept lines, newest first, to its owner.
func (T *Bridges) handleRelayLogView(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	owned := false
	for _, k := range T.listBridgeKeys(user) {
		if k.ID == id {
			owned = true
			break
		}
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	relayLogs.Lock()
	lines := append([]relayLogLine(nil), relayLogs.m[id]...)
	relayLogs.Unlock()
	type row struct {
		ID   string `json:"id"`
		At   string `json:"at"`
		Line string `json:"line"`
	}
	loc := UserLocation(user)
	out := make([]row, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		at := lines[i].At
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			at = t.In(loc).Format("Jan 2 15:04:05")
		}
		out = append(out, row{ID: fmt.Sprint(i), At: at, Line: lines[i].Text})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// clipRelayLine keeps a line on one line and within relayLogLineMax.
func clipRelayLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > relayLogLineMax {
		s = string(r[:relayLogLineMax]) + "…"
	}
	return s
}
