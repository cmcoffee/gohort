package imsg

// The relay's log, sent to the gohort server as well as written here.
//
// What the relay saw (an attachment not yet on disk, a row skipped as our own,
// a chat id that lagged) was in this machine's log and nowhere else, so a
// photo that never arrived could not be traced from the server. Every line the
// relay logs is kept here too, and the ones not yet sent go to the server's
// /bridges/api/relay-log on each poll, where the bridge's owner sees them on
// its row and the server's log keeps them.
//
// No build tag: nothing here is macOS-specific, so its test runs anywhere.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/cmcoffee/snugforge/nfo"
)

// relayLogCap bounds the lines waiting to be sent. Past it the oldest go: a
// server that cannot be reached for a long while gets the recent tail, which
// is the part worth reading.
const relayLogCap = 300

type relayLogLine struct {
	At   string `json:"at"`
	Text string `json:"text"`
	seq  uint64
}

var relayLog = struct {
	sync.Mutex
	pending []relayLogLine
	next    uint64
	paused  time.Time // an older server with nowhere to send them: try again after this
}{}

// logf writes a relay line here, as nfo.Log did, and keeps it to send.
func logf(format string, args ...any) {
	nfo.Log(append([]any{format}, args...)...)
	relayLog.Lock()
	defer relayLog.Unlock()
	relayLog.next++
	relayLog.pending = append(relayLog.pending, relayLogLine{
		At:   time.Now().UTC().Format(time.RFC3339),
		Text: fmt.Sprintf(format, args...),
		seq:  relayLog.next,
	})
	if n := len(relayLog.pending); n > relayLogCap {
		relayLog.pending = relayLog.pending[n-relayLogCap:]
	}
}

var relayLogClient = &http.Client{Timeout: 10 * time.Second}

// shipRelayLog sends the lines logged since the last send. A failed send
// keeps them for the next; a server without the endpoint is left alone for a
// while rather than asked on every poll.
func shipRelayLog(serverURL string, apiKey func() string) {
	relayLog.Lock()
	if len(relayLog.pending) == 0 || time.Now().Before(relayLog.paused) {
		relayLog.Unlock()
		return
	}
	batch := append([]relayLogLine(nil), relayLog.pending...)
	relayLog.Unlock()

	body, _ := json.Marshal(map[string]any{"lines": batch})
	req, err := http.NewRequest(http.MethodPost, serverURL+"/bridges/api/relay-log", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != nil {
		req.Header.Set("X-API-Key", apiKey())
	}
	resp, err := relayLogClient.Do(req)
	if err != nil {
		return // unreachable: kept for the next poll
	}
	resp.Body.Close()

	relayLog.Lock()
	defer relayLog.Unlock()
	switch {
	case resp.StatusCode/100 == 2:
		// Sent: drop what went, by sequence, since more may have been logged
		// (and the oldest trimmed) while the request was out.
		last := batch[len(batch)-1].seq
		keep := relayLog.pending[:0]
		for _, l := range relayLog.pending {
			if l.seq > last {
				keep = append(keep, l)
			}
		}
		relayLog.pending = keep
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		// A server from before this endpoint. nfo, not logf: a line about
		// not sending lines must not become one more to send.
		nfo.Log("relay log: the server has no /bridges/api/relay-log (HTTP %d); not sending the log for 10 minutes", resp.StatusCode)
		relayLog.pending = nil
		relayLog.paused = time.Now().Add(10 * time.Minute)
	default:
		relayLog.paused = time.Now().Add(time.Minute)
	}
}
