//go:build darwin

// Package imsg is the macOS iMessage relay service: it watches the
// local Messages chat.db, relays inbound messages to the gohort
// Bridges server's /bridges/api/hook, polls /bridges/api/poll for
// outbound items, and sends them through Messages.app via AppleScript.
// (This daemon IS the iMessage bridge — one connector in the Bridges app.)
//
// Migrated verbatim from apps/phantom/_bridge/main.go during the
// bridge consolidation; the daemon (cmd/gohort-bridge) drives it via
// Run(). Process lifecycle, flag parsing, launchd install, and the
// setup wizard moved to the daemon; contacts lookup moved to the
// sibling macos/contacts package.
package imsg

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cmcoffee/gohort/gohort-desktop/macos/contacts"
	"github.com/cmcoffee/snugforge/nfo"
	_ "modernc.org/sqlite"
)

const defaultPollInterval = 5 * time.Second
const defaultDB = "~/Library/Messages/chat.db"

// Config drives the relay service. The daemon (cmd/gohort-bridge)
// builds it from the kvlite settings store and passes it to Run.
// ServerURL is the BARE origin (e.g. "https://gohort.example.com") —
// the /phantom prefix is appended by the service, unlike the old INI
// which baked it in.
type Config struct {
	ServerURL string        // bare gohort origin, e.g. "https://gohort.example.com"
	APIKey    func() string // live getter — read per request so a rotated / auto-provisioned (sidecar) key is picked up without restarting the relay
	PollSecs  int           // chat.db poll interval, seconds
	DBPath    string        // path to chat.db (for resolving "any;-;..." chat IDs)
}

// expandHome expands a leading ~/ to the user's home directory.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

// Run opens the Messages chat.db and runs the relay loop forever:
// each tick it relays new inbound messages to the server and delivers
// any queued outbound items. The daemon (cmd/gohort-bridge) calls
// this in a goroutine. fromStart processes all existing messages
// instead of only new ones; verbose logs every poll cycle. Blocks
// until the process exits.
func Run(cfg Config, fromStart, verbose bool) {
	dbPath := expandHome(defaultDB)
	if cfg.DBPath != "" {
		dbPath = expandHome(cfg.DBPath)
	}
	cfg.DBPath = dbPath

	interval := time.Duration(cfg.PollSecs) * time.Second
	if interval < 5*time.Second {
		interval = defaultPollInterval
	}

	db := openChatDB(dbPath, interval)
	defer db.Close()

	var lastRowID int64
	if !fromStart {
		lastRowID = latestMessageRowID(dbPath)
	}
	hasBody := hasColumn(db, "message", "attributedBody")
	nfo.Log("imsg relay started. db=%s server=%s poll=%s last_rowid=%d attributedBody=%v",
		dbPath, cfg.ServerURL, interval, lastRowID, hasBody)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		prev := lastRowID
		lastRowID = processNewMessages(cfg, db, hasBody, lastRowID, verbose)
		if verbose && lastRowID == prev {
			nfo.Log("poll: no new messages (watermark=%d)", lastRowID)
		}
		deliverOutbox(cfg, db)
	}
}

// openChatDB blocks until Messages' chat.db is readable, then returns
// a read-only handle. The retry loop exists because Full Disk Access
// may not be granted yet at first launch — the daemon keeps trying and
// logs the grant instructions until access lands.
func openChatDB(dbPath string, interval time.Duration) *sql.DB {
	for {
		if _, err := os.Stat(dbPath); err != nil {
			nfo.Log("chat.db not accessible (%v) — retrying in %s\n"+
				"Make sure the bridge has Full Disk Access:\n"+
				"System Settings → Privacy & Security → Full Disk Access", err, interval)
			time.Sleep(interval)
			continue
		}
		d, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_busy_timeout=5000")
		if err != nil {
			nfo.Log("open chat.db: %v — retrying in %s", err, interval)
			time.Sleep(interval)
			continue
		}
		d.SetMaxOpenConns(3)
		if err := d.Ping(); err != nil {
			d.Close()
			nfo.Log("chat.db ping failed (%v) — retrying in %s\n"+
				"Make sure the bridge has Full Disk Access:\n"+
				"System Settings → Privacy & Security → Full Disk Access", err, interval)
			time.Sleep(interval)
			continue
		}
		return d
	}
}

// RunTest prints recent messages from chat.db and returns — the daemon's
// --test flag. Verifies Full Disk Access without contacting the server.
func RunTest(dbPath string) { runTest(expandHome(dbPath)) }

// ProbeChatDB attempts a tiny read of Messages' chat.db purely so macOS
// registers this app in System Settings → Privacy & Security → Full Disk
// Access — letting the user just flip the toggle instead of adding the
// app by hand with the "+" button. Call it at startup, BEFORE access is
// granted: the denied read is the whole point, so errors are ignored.
func ProbeChatDB() {
	f, err := os.Open(expandHome(defaultDB))
	if err != nil {
		return
	}
	var b [1]byte
	_, _ = f.Read(b[:])
	_ = f.Close()
}

// HookPayload is what we POST to /api/hook.
type HookPayload struct {
	RowID            int64    `json:"row_id,omitempty"` // DB ROWID for server-side deduplication
	ChatID           string   `json:"chat_id"`
	Handle           string   `json:"handle"`
	DisplayName      string   `json:"display_name"`                // the SENDER's name (the person at Handle), not the room name
	ConversationName string   `json:"conversation_name,omitempty"` // the group/chat title, when the chat has one — names the thread, distinct from the sender
	Text             string   `json:"text"`
	Timestamp        string   `json:"timestamp"`
	Images           []string `json:"images,omitempty"` // base64-encoded image data
	Videos           []string `json:"videos,omitempty"` // base64-encoded video data; server samples frames + extracts metadata
	Audios           []string `json:"audios,omitempty"` // base64-encoded audio (voice memo / m4a); server transcribes it
}

// OutboxItem is what /api/poll returns.
type OutboxItem struct {
	ID     string   `json:"id"`
	ChatID string   `json:"chat_id"`
	Handle string   `json:"handle"`
	Text   string   `json:"text"`
	Images []string `json:"images,omitempty"` // base64-encoded images to send as attachments
	Videos []string `json:"videos,omitempty"` // base64-encoded videos to send as attachments
	Type   string   `json:"type"`
}

// retryEntry holds a failed outbox item waiting for a retry attempt.
type retryEntry struct {
	item      OutboxItem
	attempts  int
	nextRetry time.Time
}

const maxDeliveryAttempts = 5

var httpClient = &http.Client{Timeout: 20 * time.Second}

var (
	retryMu    sync.Mutex
	retryQueue []*retryEntry

	// emptyRowMu guards emptyRowSeen, which tracks how many consecutive polls
	// a row has had no content. After maxEmptyPolls we give up and skip it.
	emptyRowMu   sync.Mutex
	emptyRowSeen = map[int64]int{}
)

const maxEmptyPolls = 6 // ~30s at 5s interval before giving up on an empty row

var (
	sentROWIDMu sync.Mutex
	sentROWIDs  = map[int64]struct{}{} // ROWIDs of is_from_me=1 rows we sent; skip these in the processing loop
	// sentText is a TTL-bounded set of recently-sent message texts. It's a
	// fallback skip key for is_from_me=1 rows whose ROWID never landed in
	// sentROWIDs (iMessage occasionally writes the row > 10s after our
	// osascript send returns, and a bridge restart wipes the ROWID set
	// entirely). Without this, our own outbound messages loop back through
	// the hook as if the user typed them.
	sentTextMu sync.Mutex
	sentText   = map[string]time.Time{}
)

const sentTextTTL = 10 * time.Minute

// rememberSentText records s with the current time so subsequent polls can
// match against it as a fallback to the ROWID set. Empty / very short
// strings are skipped to avoid false matches on common short replies.
func rememberSentText(s string) {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return
	}
	sentTextMu.Lock()
	defer sentTextMu.Unlock()
	sentText[s] = time.Now()
	// Lazy GC: walk the map and drop expired entries when it grows large.
	if len(sentText) > 200 {
		cutoff := time.Now().Add(-sentTextTTL)
		for k, t := range sentText {
			if t.Before(cutoff) {
				delete(sentText, k)
			}
		}
	}
}

// matchesRecentSentText returns true if s was recently sent by us (within
// sentTextTTL). Used as a secondary skip key for is_from_me=1 rows.
func matchesRecentSentText(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return false
	}
	sentTextMu.Lock()
	defer sentTextMu.Unlock()
	t, ok := sentText[s]
	if !ok {
		return false
	}
	if time.Since(t) > sentTextTTL {
		delete(sentText, s)
		return false
	}
	return true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func hasColumn(db *sql.DB, table, column string) bool {
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&n)
	return n > 0
}

// messageTextQuery returns the SELECT query for incoming messages.
// No content filter is applied in SQL — iMessage fills text/attributedBody asynchronously,
// so filtering in SQL races against the write. The Go side handles empty content.
func messageTextQuery(hasBody bool) string {
	bodyCol := "NULL"
	if hasBody {
		bodyCol = "m.attributedBody"
	}
	return fmt.Sprintf(`
		SELECT
			m.ROWID,
			m.is_from_me,
			IFNULL(m.text,'') AS text,
			m.date,
			IFNULL(h.id,'')      AS handle,
			IFNULL(h.service,'') AS service,
			IFNULL((SELECT c.guid         FROM chat_message_join cmj JOIN chat c ON c.ROWID = cmj.chat_id WHERE cmj.message_id = m.ROWID LIMIT 1),'') AS chat_id,
			IFNULL((SELECT c.display_name FROM chat_message_join cmj JOIN chat c ON c.ROWID = cmj.chat_id WHERE cmj.message_id = m.ROWID LIMIT 1),'') AS chat_display_name,
			%s AS body
		FROM message m
		LEFT JOIN handle h ON m.handle_id = h.ROWID
		WHERE m.ROWID > ?
		  AND (m.associated_message_type IS NULL OR m.associated_message_type = 0)
		  AND m.item_type = 0
		ORDER BY m.ROWID ASC
	`, bodyCol)
}

// runTest prints the last 10 messages (sent and received) to verify DB access and schema.
func runTest(dbPath string) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		nfo.Fatal("open: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		nfo.Fatal("ping: %v", err)
	}

	hasBody := hasColumn(db, "message", "attributedBody")
	fmt.Printf("Schema: attributedBody column present = %v\n", hasBody)

	bodyCol := "NULL"
	if hasBody {
		bodyCol = "m.attributedBody"
	}
	q := fmt.Sprintf(`
		SELECT
			m.ROWID,
			CASE m.is_from_me WHEN 1 THEN 'me' ELSE IFNULL(h.id,'?') END AS sender,
			IFNULL(m.text,'') AS text,
			m.date,
			%s AS body,
			(SELECT COUNT(*) FROM message_attachment_join maj JOIN attachment a ON a.ROWID=maj.attachment_id WHERE maj.message_id=m.ROWID AND a.mime_type LIKE 'image/%%') AS img_count
		FROM message m
		LEFT JOIN handle h ON m.handle_id = h.ROWID
		ORDER BY m.ROWID DESC
		LIMIT 20
	`, bodyCol)

	rows, err := db.Query(q)
	if err != nil {
		nfo.Fatal("query: %v", err)
	}
	defer rows.Close()

	fmt.Println("--- last 20 messages (newest first) ---")
	count := 0
	for rows.Next() {
		var rowID, appleDate, imgCount int64
		var sender, text string
		var body []byte
		if err := rows.Scan(&rowID, &sender, &text, &appleDate, &body, &imgCount); err != nil {
			nfo.Log("scan: %v", err)
			continue
		}
		if text == "" && len(body) > 0 {
			text = extractTextFromBody(body)
			if text != "" {
				text = "[body] " + text
			}
		}
		if text == "" {
			text = "(no text)"
		}
		ts := appleTimeToRFC3339(appleDate)
		if len(text) > 80 {
			text = text[:80] + "…"
		}
		imgNote := ""
		if imgCount > 0 {
			imgNote = fmt.Sprintf(" [%d image(s)]", imgCount)
		}
		fmt.Printf("[%d] %s %s: %s%s\n", rowID, ts, sender, text, imgNote)
		count++
	}
	if count == 0 {
		fmt.Println("(no messages found — check that the query joins are correct for your macOS version)")
	}
	fmt.Printf("--- max ROWID in db: %d ---\n", latestMessageRowID(dbPath))
}

// processNewMessages queries chat.db for messages newer than lastRowID,
// POSTs them to the server, and returns the new high-water mark.
// Only advances the watermark past messages that were successfully sent.
func processNewMessages(cfg Config, db *sql.DB, hasBody bool, lastRowID int64, verbose bool) int64 {
	var dbMax int64
	db.QueryRow(`SELECT IFNULL(MAX(ROWID),0) FROM message`).Scan(&dbMax)
	if verbose {
		nfo.Log("poll: watermark=%d db_max=%d", lastRowID, dbMax)
	}

	rows, err := db.Query(messageTextQuery(hasBody), lastRowID)
	if err != nil {
		nfo.Log("query: %v", err)
		return lastRowID
	}
	defer rows.Close()

	newMax := lastRowID
	rowCount := 0
	for rows.Next() {
		var rowID int64
		var isFromMe int
		var text, handle, service, chatID, chatDisplayName string
		var appleDate int64
		var body []byte
		if err := rows.Scan(&rowID, &isFromMe, &text, &appleDate, &handle, &service, &chatID, &chatDisplayName, &body); err != nil {
			nfo.Log("scan error rowid=%d: %v", rowID, err)
			continue
		}
		rowCount++

		// macOS Ventura+: text column is often NULL; extract from attributedBody blob.
		if text == "" && len(body) > 0 {
			text = extractTextFromBody(body)
		}
		if verbose {
			nfo.Log("found rowid=%d is_from_me=%d handle=%q chatID=%q text=%q body_bytes=%d", rowID, isFromMe, handle, chatID, truncate(text, 40), len(body))
		}

		// Skip is_from_me=1 rows that we know we sent. Two skip paths:
		//   - Primary: ROWID recorded at delivery time (recordSentROWIDs).
		//   - Fallback: text matches a recently-sent message. Catches the
		//     race where chat.db wrote the row after the recordSent
		//     window closed, or where a bridge restart wiped the ROWID set.
		// User self-messages from another device are NOT in either set
		// and pass through normally.
		if isFromMe == 1 {
			sentROWIDMu.Lock()
			_, skipByID := sentROWIDs[rowID]
			sentROWIDMu.Unlock()
			if skipByID {
				nfo.Log("rowid=%d: skipping our own outgoing reply (matched by ROWID)", rowID)
				newMax = rowID
				continue
			}
			if matchesRecentSentText(text) {
				nfo.Log("rowid=%d: skipping our own outgoing reply (matched by text)", rowID)
				// Record the ROWID so subsequent passes use the fast path
				// even if the same row reappears.
				sentROWIDMu.Lock()
				sentROWIDs[rowID] = struct{}{}
				sentROWIDMu.Unlock()
				newMax = rowID
				continue
			}
			// User typed this themselves — clear handle so the server treats it as from_me.
			handle = ""
		}

		// chatID comes from chat_message_join which may lag behind the message row.
		// Group chats can take longer — retry up to maxEmptyPolls before falling back.
		if chatID == "" {
			if handle == "" {
				// No handle either — skip entirely.
				newMax = rowID
				continue
			}
			emptyRowMu.Lock()
			emptyRowSeen[rowID]++
			polls := emptyRowSeen[rowID]
			emptyRowMu.Unlock()
			if polls < maxEmptyPolls {
				nfo.Log("rowid=%d: no chat_id yet (poll %d/%d), rechecking", rowID, polls, maxEmptyPolls)
				break
			}
			nfo.Log("rowid=%d: no chat_id after %d polls, falling back to handle %q", rowID, maxEmptyPolls, handle)
			chatID = handle
			emptyRowMu.Lock()
			delete(emptyRowSeen, rowID)
			emptyRowMu.Unlock()
		}

		ts := appleTimeToRFC3339(appleDate)
		images := readImageAttachments(db, rowID)
		videos := readVideoAttachments(db, rowID)
		audios := readAudioAttachments(db, rowID)
		if text == "" && len(images) == 0 && len(videos) == 0 && len(audios) == 0 {
			var attachCount int
			db.QueryRow(`SELECT COUNT(*) FROM message_attachment_join WHERE message_id = ?`, rowID).Scan(&attachCount)
			if attachCount > 0 {
				// Has attachments but none we could read (not an image/video/audio —
				// e.g. a pdf or vcard). Send a NEUTRAL placeholder, never "[Image]":
				// claiming a picture when none was sent makes the agent hallucinate
				// one. The server-side note tells the agent it can't inspect it.
				nfo.Log("rowid=%d: %d attachment(s), none processable — using [Attachment] placeholder", rowID, attachCount)
				text = "[Attachment]"
			} else {
				// Row exists but content not yet written — iMessage fills text
				// asynchronously. Re-check for up to maxEmptyPolls, then give up.
				emptyRowMu.Lock()
				emptyRowSeen[rowID]++
				polls := emptyRowSeen[rowID]
				emptyRowMu.Unlock()
				if polls < maxEmptyPolls {
					nfo.Log("rowid=%d handle=%q: no content yet (poll %d/%d), rechecking", rowID, handle, polls, maxEmptyPolls)
					break
				}
				// Log the raw attributedBody (hex, capped) so a message that
				// extracts to empty — e.g. a very short "P." — can be decoded
				// after the fact and the extractor fixed for that exact layout.
				hexBody := body
				if len(hexBody) > 256 {
					hexBody = hexBody[:256]
				}
				nfo.Log("rowid=%d handle=%q: still empty after %d polls, skipping — body[%d]=%x", rowID, handle, polls, len(body), hexBody)
				emptyRowMu.Lock()
				delete(emptyRowSeen, rowID)
				emptyRowMu.Unlock()
				newMax = rowID
				continue
			}
		}

		// Sender name = the PERSON at this handle (Contacts lookup), falling back
		// to the raw handle. Never the group's name — that's the conversation, not
		// the sender. Conflating them mis-attributes every message in a named group
		// to the group itself. The empty-handle (is_from_me) case stays blank so the
		// server labels it as the owner's own message.
		senderName := ""
		if handle != "" {
			if senderName = contacts.Lookup(handle); senderName == "" {
				senderName = handle
			}
		}

		payload := HookPayload{
			RowID:            rowID,
			ChatID:           chatID,
			Handle:           handle,
			DisplayName:      senderName,
			ConversationName: chatDisplayName, // group/chat title (empty for 1:1 / unnamed), names the thread
			Text:             text,
			Timestamp:        ts,
			Images:           images,
			Videos:           videos,
			Audios:           audios,
		}
		if err := postHook(cfg, payload); err != nil {
			if he, ok := err.(*hookErr); ok && he.skip {
				nfo.Log("hook rejected rowid=%d handle=%s: %v — skipping row", rowID, handle, err)
				newMax = rowID
				continue
			}
			nfo.Log("hook FAILED rowid=%d handle=%s: %v — will retry next poll", rowID, handle, err)
			break
		}
		newMax = rowID
		emptyRowMu.Lock()
		delete(emptyRowSeen, rowID)
		emptyRowMu.Unlock()
		switch {
		case len(images) > 0 || len(videos) > 0 || len(audios) > 0:
			nfo.Log("relayed [%d] from %s: %q [%d image(s), %d video(s), %d audio(s)]", rowID, handle, truncate(text, 60), len(images), len(videos), len(audios))
		default:
			nfo.Log("relayed [%d] from %s: %q", rowID, handle, truncate(text, 60))
		}
	}
	if rowCount > 0 {
		nfo.Log("poll done: scanned %d row(s), watermark now %d", rowCount, newMax)
	}
	return newMax
}

// extractTextFromBody extracts the message text from an NSAttributedString binary plist blob.
// On macOS Ventura+ the text column is NULL and the body contains the actual content.
// It first tries a proper binary plist parse to follow the NS.string key through the
// object table; if that fails it falls back to a printable-run heuristic.
func extractTextFromBody(body []byte) string {
	if len(body) < 10 {
		return ""
	}
	if s := bplistNSString(body); s != "" {
		return stripStreamtypedFraming(s)
	}
	if s := streamtypedNSString(body); s != "" {
		return s
	}
	return extractTextHeuristic(body)
}

// streamtypedNSString extracts the message text from a legacy NSArchiver
// ("streamtyped") attributedBody blob by reading the first NSString
// value's length-prefixed byte payload directly, instead of scavenging
// printable runs. iMessage stores the message text as the backing string
// of an NSMutableAttributedString; in the typedstream format that string
// is a '+' (0x2B) byte-array marker followed by a length integer and the
// UTF-8 bytes. Reading EXACTLY length bytes is what makes this correct:
//   - it stops at the true end, so the attribute-run metadata that
//     follows (NSNumber run lengths, attribute dicts, a second class
//     chain) never leaks in as trailing garbage; and
//   - it consumes the length byte rather than treating it as text, so a
//     length that happens to be a printable ASCII byte (e.g. 51 == 0x33
//     == '3', 49 == 0x31 == '1') does not bleed into the output and
//     truncate the message — the failure mode the printable-run
//     heuristic exhibited on any text whose length landed in 0x20..0x7E.
//
// typedstream integer encoding: a single byte for 0..0x80; a 0x81 prefix
// for a following little-endian int16; a 0x82 prefix for int32.
func streamtypedNSString(body []byte) string {
	head := 64
	if len(body) < head {
		head = len(body)
	}
	if !bytes.Contains(body[:head], []byte("streamtyped")) {
		return ""
	}
	anchor := bytes.Index(body, []byte("NSString"))
	if anchor < 0 {
		anchor = bytes.Index(body, []byte("NSMutableString"))
	}
	if anchor < 0 {
		return ""
	}
	// The '+' byte-array value marker introduces the text payload.
	rel := bytes.IndexByte(body[anchor:], '+')
	if rel < 0 {
		return ""
	}
	p := anchor + rel + 1 // first byte after '+'
	if p >= len(body) {
		return ""
	}
	var length, dataOff int
	switch b := body[p]; {
	case b == 0x81:
		if p+3 > len(body) {
			return ""
		}
		length = int(binary.LittleEndian.Uint16(body[p+1 : p+3]))
		dataOff = p + 3
	case b == 0x82:
		if p+5 > len(body) {
			return ""
		}
		length = int(binary.LittleEndian.Uint32(body[p+1 : p+5]))
		dataOff = p + 5
	case b < 0x80:
		length = int(b)
		dataOff = p + 1
	default:
		return ""
	}
	if length <= 0 || dataOff+length > len(body) {
		return ""
	}
	s := string(body[dataOff : dataOff+length])
	if !utf8.ValidString(s) {
		return ""
	}
	return s
}

// stripStreamtypedFraming removes the NSStreamTyped '+' + length-byte header that
// iMessage sometimes embeds at the start of extracted NS.string values.
// Format: '+' (0x2B) + one byte encoding the payload length + payload.
func stripStreamtypedFraming(s string) string {
	if len(s) >= 4 && s[0] == '+' {
		expectedLen := int(s[1])
		actualLen := len(s) - 2
		diff := expectedLen - actualLen
		if diff < 0 {
			diff = -diff
		}
		if expectedLen >= 4 && diff <= 2 {
			return s[2:]
		}
	}
	return s
}

// bplistNSString parses a bplist00 binary plist and returns the value of the
// first "NS.string" key found in any dict — which is the plain-text content of
// an NSAttributedString / NSKeyedArchiver archive.
func bplistNSString(body []byte) string {
	if len(body) < 40 || string(body[:8]) != "bplist00" {
		return ""
	}
	// Trailer occupies the last 32 bytes.
	t := body[len(body)-32:]
	offsetIntSize := int(t[6])
	objRefSize := int(t[7])
	numObjects := int(binary.BigEndian.Uint64(t[8:16]))
	offsetTableOff := int(binary.BigEndian.Uint64(t[24:32]))

	if offsetIntSize < 1 || offsetIntSize > 8 || objRefSize < 1 || objRefSize > 4 ||
		numObjects <= 0 || offsetTableOff <= 0 || offsetTableOff >= len(body) {
		return ""
	}

	readUintN := func(b []byte, n int) int {
		v := 0
		for i := 0; i < n && i < len(b); i++ {
			v = v<<8 | int(b[i])
		}
		return v
	}

	objOff := func(idx int) int {
		pos := offsetTableOff + idx*offsetIntSize
		if pos+offsetIntSize > len(body) {
			return -1
		}
		return readUintN(body[pos:], offsetIntSize)
	}

	// readBplistString reads a bplist ASCII or UTF-16BE string object at byte offset off.
	readBplistString := func(off int) (string, bool) {
		if off < 0 || off >= len(body) {
			return "", false
		}
		marker := body[off]
		typ := marker >> 4
		count := int(marker & 0x0F)
		dataOff := off + 1

		if count == 0x0F {
			// Extended length: next object is an integer.
			if dataOff >= len(body) {
				return "", false
			}
			intMarker := body[dataOff]
			if intMarker>>4 != 0x1 {
				return "", false
			}
			intBytes := 1 << (intMarker & 0x0F)
			dataOff++
			if dataOff+intBytes > len(body) {
				return "", false
			}
			count = readUintN(body[dataOff:], intBytes)
			dataOff += intBytes
		}

		switch typ {
		case 0x5: // ASCII string
			if dataOff+count > len(body) {
				return "", false
			}
			return string(body[dataOff : dataOff+count]), true
		case 0x6: // UTF-16BE string
			if dataOff+count*2 > len(body) {
				return "", false
			}
			runes := make([]rune, count)
			for i := range runes {
				runes[i] = rune(binary.BigEndian.Uint16(body[dataOff+i*2:]))
			}
			return string(runes), true
		}
		return "", false
	}

	// Scan all objects looking for a dict that contains the key "NS.string".
	for i := 0; i < numObjects; i++ {
		off := objOff(i)
		if off < 0 || off >= len(body) {
			continue
		}
		marker := body[off]
		if marker>>4 != 0xD {
			continue // not a dict
		}
		count := int(marker & 0x0F)
		refsOff := off + 1
		if count == 0x0F {
			// Extended count: next byte is an integer marker 0x1n.
			if refsOff >= len(body) {
				continue
			}
			intMarker := body[refsOff]
			if intMarker>>4 != 0x1 {
				continue
			}
			intBytes := 1 << (intMarker & 0x0F)
			refsOff++
			if refsOff+intBytes > len(body) {
				continue
			}
			count = readUintN(body[refsOff:], intBytes)
			refsOff += intBytes
		}
		needed := count * 2 * objRefSize
		if refsOff+needed > len(body) {
			continue
		}
		for j := 0; j < count; j++ {
			keyRef := readUintN(body[refsOff+j*objRefSize:], objRefSize)
			keyStr, ok := readBplistString(objOff(keyRef))
			if !ok || keyStr != "NS.string" {
				continue
			}
			valRef := readUintN(body[refsOff+(count+j)*objRefSize:], objRefSize)
			if s, ok := readBplistString(objOff(valRef)); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// typedstreamClassNames is the registry of class / key names that the
// NSKeyedArchiver / streamtyped format emits as printable ASCII runs at
// the start of a serialized NSAttributedString blob. The heuristic
// extractor sees them as printable UTF-8 and would happily prepend them
// to the user's actual text — so we filter explicit matches out before
// joining. The list is the union of class names observed in macOS
// chat.db blobs across Sonoma+ and the iMessage-internal attribute keys
// that ride alongside data-detector / link runs.
var typedstreamClassNames = map[string]bool{
	"streamtyped":                            true,
	"NSObject":                               true,
	"NSString":                               true,
	"NSMutableString":                        true,
	"NSAttributedString":                     true,
	"NSMutableAttributedString":              true,
	"NSDictionary":                           true,
	"NSMutableDictionary":                    true,
	"NSArray":                                true,
	"NSMutableArray":                         true,
	"NSData":                                 true,
	"NSMutableData":                          true,
	"NSNumber":                               true,
	"NSValue":                                true,
	"NSDate":                                 true,
	"NSURL":                                  true,
	"NSColor":                                true,
	"NSFont":                                 true,
	"NSKeyedArchiver":                        true,
	"DDScannerResult":                        true,
	"NSDictionary0":                          true,
	"__kIMMessagePartAttributeName":          true,
	"__kIMBaseWritingDirectionAttributeName": true,
	"__kIMFileTransferGUIDAttributeName":     true,
	"__kIMLinkAttributeName":                 true,
	"__kIMDataDetectedAttributeName":         true,
	"__kIMMentionConfirmedMention":           true,
	"__kIMTextEffectAttributeName":           true,
	"NS.string":                              true,
	"NS.keys":                                true,
	"NS.objects":                             true,
}

// extractTextHeuristic is the fallback: find printable UTF-8 runs in
// the blob, drop typedstream class-name preamble tokens, and join the
// rest. The class-name filter prevents the leading
// "streamtyped NSAttributedString NSObject NSString …" preamble that
// every macOS-encoded NSAttributedString carries from being prepended
// to every empty-handle (is_from_me=1) message the server sees.
//
// Short-message handling: previous versions dropped any run < 2 chars
// as binary noise, which silently filtered legitimate one-character
// messages (a lone digit, a single emoji-less character, "y"/"n"
// replies). Instead, collect all runs first; if the class-name filter
// leaves exactly one survivor, return it unconditionally — it's the
// whole message. Only fall back to the < 2 noise drop when there are
// multiple runs to disambiguate.
func extractTextHeuristic(body []byte) string {
	var raw []string
	i := 0
	for i < len(body) {
		r, size := utf8.DecodeRune(body[i:])
		if r == utf8.RuneError || size == 0 || (r < 0x20 && r != '\n' && r != '\r' && r != '\t') {
			i++
			continue
		}
		start := i
		for i < len(body) {
			r, size = utf8.DecodeRune(body[i:])
			if r == utf8.RuneError || size == 0 || (r < 0x20 && r != '\n' && r != '\r' && r != '\t') {
				break
			}
			i += size
		}
		s := strings.TrimSpace(string(body[start:i]))
		s = stripStreamtypedFraming(s)
		if s == "" {
			continue
		}
		if typedstreamClassNames[s] {
			continue
		}
		raw = append(raw, s)
	}
	if len(raw) == 0 {
		return ""
	}
	if len(raw) == 1 {
		// Single surviving run is the entire message — return it even
		// if it's one character.
		return raw[0]
	}
	parts := raw[:0]
	for _, s := range raw {
		if len(s) >= 2 {
			parts = append(parts, s)
			continue
		}
		// Single-char run: keep it only if it's alphanumeric — a real
		// one-letter/digit message like "P" or "5". Lone punctuation /
		// symbol runs at this length are typedstream framing artifacts
		// (e.g. the "+" length header), not content, so drop those. This
		// stops one-character texts from being filtered out entirely.
		if c := s[0]; (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

// maxInboundGifBytes caps the raw-GIF passthrough. Animated GIFs ship raw
// (the server frame-samples them); anything larger is flattened to a single
// still instead so an outsized GIF can't balloon the hook POST. iMessage GIFs
// are almost always well under this.
const maxInboundGifBytes = 25 * 1024 * 1024

// readImageAttachments returns base64-encoded image data for images attached to messageID.
// Converts HEIC/PNG/WebP to JPEG; GIFs pass through raw for server-side frame
// sampling. Reads up to 3 images to keep payloads manageable.
func readImageAttachments(db *sql.DB, messageID int64) []string {
	// No transfer_state filter: received images use state=5, sent use state=2, and
	// the values vary by macOS version. File existence + size checks are the reliable gate.
	rows, err := db.Query(`
		SELECT a.filename, IFNULL(a.mime_type,''), a.transfer_state
		FROM message_attachment_join maj
		JOIN attachment a ON a.ROWID = maj.attachment_id
		WHERE maj.message_id = ?
		LIMIT 10
	`, messageID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var filename, mimeType string
		var transferState int
		if rows.Scan(&filename, &mimeType, &transferState) != nil {
			continue
		}
		nfo.Log("attachment candidate: state=%d mime=%q path=%s", transferState, mimeType, filename)

		// Only process image MIME types, or files with image extensions.
		lower := strings.ToLower(filename)
		isImage := strings.HasPrefix(mimeType, "image/") ||
			strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") ||
			strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".gif") ||
			strings.HasSuffix(lower, ".heic") || strings.HasSuffix(lower, ".heif") ||
			strings.HasSuffix(lower, ".webp")
		if !isImage {
			continue
		}

		path := expandHome(filename)
		info, err := os.Stat(path)
		if err != nil {
			nfo.Log("attachment not found: %s: %v", filepath.Base(path), err)
			continue
		}
		if info.Size() < 512 {
			nfo.Log("attachment too small (%d bytes), placeholder: %s", info.Size(), filepath.Base(path))
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			nfo.Log("read attachment %s: %v", filepath.Base(path), err)
			continue
		}

		// Convert non-JPEG stills to JPEG — many vision models (including
		// Ollama-hosted Gemma) only accept JPEG in their image projection layer.
		// GIFs are the exception: ship them RAW so the server can frame-sample
		// animated GIFs into a temporal still sequence (sips would flatten them
		// to a single frame, losing the motion). Cap the raw passthrough so a
		// pathological GIF can't balloon the hook POST; over-cap GIFs fall back
		// to the flattened still.
		isJPEG := mimeType == "image/jpeg" ||
			strings.HasSuffix(lower, ".jpg") ||
			strings.HasSuffix(lower, ".jpeg")
		isGIF := mimeType == "image/gif" || strings.HasSuffix(lower, ".gif")
		switch {
		case isJPEG:
			// already vision-ready — send raw
		case isGIF && info.Size() <= maxInboundGifBytes:
			nfo.Log("gif passthrough (raw, %d bytes): %s — server frame-samples", len(data), filepath.Base(path))
		default:
			converted := toJPEG(path)
			if converted != nil {
				data = converted
				nfo.Log("converted→JPEG for %s (%d bytes)", filepath.Base(path), len(data))
			} else {
				nfo.Log("skipping non-JPEG (sips conversion failed): %s", filepath.Base(path))
				continue
			}
		}

		out = append(out, base64.StdEncoding.EncodeToString(data))
		nfo.Log("attached image %s (%d bytes, mime=%s)", filepath.Base(path), len(data), mimeType)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

// readVideoAttachments returns base64-encoded raw video bytes for video
// attachments on messageID. Phantom samples frames + extracts metadata
// server-side via ffmpeg, so we just need to deliver the file as-is. Caps
// at 1 video per message to keep payload size manageable — multi-video
// iMessage is rare, and we'd rather drop excess than blow up payloads
// (a 30-sec 4K clip is ~80 MB; even one is a heavy POST).
func readVideoAttachments(db *sql.DB, messageID int64) []string {
	rows, err := db.Query(`
		SELECT a.filename, IFNULL(a.mime_type,''), a.transfer_state
		FROM message_attachment_join maj
		JOIN attachment a ON a.ROWID = maj.attachment_id
		WHERE maj.message_id = ?
		LIMIT 10
	`, messageID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	const maxVideoBytes = 200 * 1024 * 1024 // 200 MB hard cap per video

	var out []string
	for rows.Next() {
		var filename, mimeType string
		var transferState int
		if rows.Scan(&filename, &mimeType, &transferState) != nil {
			continue
		}

		lower := strings.ToLower(filename)
		isVideo := strings.HasPrefix(mimeType, "video/") ||
			strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".mov") ||
			strings.HasSuffix(lower, ".m4v") || strings.HasSuffix(lower, ".webm") ||
			strings.HasSuffix(lower, ".mkv") || strings.HasSuffix(lower, ".avi") ||
			strings.HasSuffix(lower, ".3gp") || strings.HasSuffix(lower, ".3g2")
		if !isVideo {
			continue
		}

		path := expandHome(filename)
		info, err := os.Stat(path)
		if err != nil {
			nfo.Log("video attachment not found: %s: %v", filepath.Base(path), err)
			continue
		}
		if info.Size() > maxVideoBytes {
			nfo.Log("video too large (%d bytes), skipping: %s", info.Size(), filepath.Base(path))
			continue
		}
		if info.Size() < 512 {
			nfo.Log("video too small (%d bytes), placeholder: %s", info.Size(), filepath.Base(path))
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			nfo.Log("read video %s: %v", filepath.Base(path), err)
			continue
		}

		out = append(out, base64.StdEncoding.EncodeToString(data))
		nfo.Log("attached video %s (%d bytes, mime=%s)", filepath.Base(path), len(data), mimeType)
		if len(out) >= 1 {
			break
		}
	}
	return out
}

// readAudioAttachments returns base64 audio attachments (a voice memo / m4a) on
// a message. Mirrors readVideoAttachments; the server transcribes these instead
// of trying to "see" them. Without this an audio-only message looks empty to the
// relay and gets sent as an "[Image]" placeholder — the agent then reports a
// picture that was never sent.
func readAudioAttachments(db *sql.DB, messageID int64) []string {
	rows, err := db.Query(`
		SELECT a.filename, IFNULL(a.mime_type,''), a.transfer_state
		FROM message_attachment_join maj
		JOIN attachment a ON a.ROWID = maj.attachment_id
		WHERE maj.message_id = ?
		LIMIT 10
	`, messageID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	const maxAudioBytes = 50 * 1024 * 1024 // 50 MB hard cap per clip

	var out []string
	for rows.Next() {
		var filename, mimeType string
		var transferState int
		if rows.Scan(&filename, &mimeType, &transferState) != nil {
			continue
		}

		lower := strings.ToLower(filename)
		isAudio := strings.HasPrefix(mimeType, "audio/") ||
			strings.HasSuffix(lower, ".m4a") || strings.HasSuffix(lower, ".mp3") ||
			strings.HasSuffix(lower, ".wav") || strings.HasSuffix(lower, ".aac") ||
			strings.HasSuffix(lower, ".caf") || strings.HasSuffix(lower, ".amr") ||
			strings.HasSuffix(lower, ".aiff") || strings.HasSuffix(lower, ".aif") ||
			strings.HasSuffix(lower, ".ogg") || strings.HasSuffix(lower, ".opus") ||
			strings.HasSuffix(lower, ".flac")
		if !isAudio {
			continue
		}

		path := expandHome(filename)
		info, err := os.Stat(path)
		if err != nil {
			nfo.Log("audio attachment not found: %s: %v", filepath.Base(path), err)
			continue
		}
		if info.Size() > maxAudioBytes {
			nfo.Log("audio too large (%d bytes), skipping: %s", info.Size(), filepath.Base(path))
			continue
		}
		if info.Size() < 256 {
			nfo.Log("audio too small (%d bytes), skipping: %s", info.Size(), filepath.Base(path))
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			nfo.Log("read audio %s: %v", filepath.Base(path), err)
			continue
		}

		out = append(out, base64.StdEncoding.EncodeToString(data))
		nfo.Log("attached audio %s (%d bytes, mime=%s)", filepath.Base(path), len(data), mimeType)
		if len(out) >= 1 {
			break
		}
	}
	return out
}

// toJPEG converts any image to JPEG using macOS sips.
func toJPEG(srcPath string) []byte {
	tmp, err := os.CreateTemp("", "relay-img-*.jpg")
	if err != nil {
		return nil
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	sipsOut, err := exec.Command("sips", "-s", "format", "jpeg", srcPath, "--out", tmp.Name()).CombinedOutput()
	if err != nil {
		nfo.Log("sips failed for %s: %v: %s", filepath.Base(srcPath), err, strings.TrimSpace(string(sipsOut)))
		return nil
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil || len(data) < 1024 {
		return nil
	}
	return data
}

// deliverOutbox polls the server for queued messages and delivers them via osascript.
// The server deletes items on poll; failed deliveries are held in the local retry queue.
func deliverOutbox(cfg Config, db *sql.DB) {
	// Retry any previously-failed items that are due before polling for new ones.
	flushRetryQueue(cfg, db)

	req, _ := http.NewRequest(http.MethodGet, cfg.ServerURL+"/bridges/api/poll", nil)
	req.Header.Set("X-API-Key", cfg.APIKey())
	resp, err := httpClient.Do(req)
	if err != nil {
		nfo.Log("poll: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var items []OutboxItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return
	}
	if len(items) > 0 {
		nfo.Log("poll: %d item(s)", len(items))
	}
	for _, item := range items {
		tryDeliver(cfg, db, item, 1)
	}
}

// tryDeliver attempts to send one outbox item via osascript.
// On failure it queues the item for retry with exponential backoff.
// On success it records the newly-created is_from_me=1 ROWID so the polling
// loop skips it and doesn't treat our own reply as a new inbound message.
func tryDeliver(cfg Config, db *sql.DB, item OutboxItem, attempt int) {
	// Resolve "any;-;..." chat IDs to their actual service-specific ones from
	// the Messages database so the Messages app can resolve them.
	item.ChatID = resolveChatID(cfg.DBPath, item.ChatID)

	nfo.Log("delivering [attempt %d] %s to %s: %q", attempt, item.Type, item.Handle, truncate(item.Text, 60))

	// Snapshot max ROWID before sending so we can find the new row afterwards.
	var preROWID int64
	if db != nil {
		db.QueryRow(`SELECT IFNULL(MAX(ROWID),0) FROM message`).Scan(&preROWID)
	}

	// Send image attachments first so the text reply follows them naturally.
	for i, b64 := range item.Images {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			nfo.Log("image %d decode error: %v", i, err)
			continue
		}
		ext := imageExt(data)
		tmp, err := os.CreateTemp("", "phantom-img-*"+ext)
		if err != nil {
			nfo.Log("image %d temp file error: %v", i, err)
			continue
		}
		tmpPath := tmp.Name()
		_, err = tmp.Write(data)
		tmp.Close()
		if err != nil {
			nfo.Log("image %d write error: %v", i, err)
			os.Remove(tmpPath)
			continue
		}
		// Per-format delivery decisions:
		//
		//   - PNG / JPG: pass through as-is — first-class on iMessage.
		//   - WebP: still flatten to JPEG via sips. Mostly used for
		//     static images by the sources we ingest from; cross-
		//     device reliability concerns about animated WebP and
		//     mixed-fallback recipients haven't changed.
		//   - GIF: preserve animation when it'll fit. iMessage handles
		//     animated GIF natively on iOS/macOS; only the >~20MB cap
		//     forces our hand. Under-cap files pass through. Over-cap
		//     files transcode to MP4 (iMessage previews inline, much
		//     smaller bytes-per-second of motion, still animated).
		//     Both paths fail → fall back to the prior JPEG flatten
		//     so we never DROP a delivery — better to send a still
		//     than nothing.
		sendPath := tmpPath
		switch ext {
		case ".webp":
			if converted, err := convertToJPEG(tmpPath); err == nil {
				sendPath = converted
			} else {
				nfo.Log("image %d webp convert warning: %v — sending original", i, err)
			}
		case ".gif":
			fi, _ := os.Stat(tmpPath)
			size := int64(0)
			if fi != nil {
				size = fi.Size()
			}
			if size > 0 && size <= iMessageImageMaxBytes {
				nfo.Log("image %d: gif under cap (%d bytes), sending animated", i, size)
				// sendPath stays tmpPath — animated GIF passes through.
			} else if mp4Path, err := convertGIFToMP4(tmpPath); err == nil {
				sendPath = mp4Path
				nfo.Log("image %d: gif %d bytes over cap, transcoded to mp4: %s", i, size, mp4Path)
			} else {
				nfo.Log("image %d: gif transcode failed (%v), falling back to JPEG flatten", i, err)
				if converted, jerr := convertToJPEG(tmpPath); jerr == nil {
					sendPath = converted
				} else {
					nfo.Log("image %d: jpeg fallback also failed (%v) — sending original gif", i, jerr)
				}
			}
		}
		if fi, err := os.Stat(sendPath); err == nil {
			nfo.Log("image %d sending: %s (%d bytes)", i, filepath.Ext(sendPath), fi.Size())
		}
		if err := sendFileViaMessages(item.Handle, item.ChatID, sendPath); err != nil {
			nfo.Log("image %d send error to %s: %v", i, item.Handle, err)
			os.Remove(tmpPath)
			if sendPath != tmpPath {
				os.Remove(sendPath)
			}
		} else {
			nfo.Log("image %d sent to %s (%s)", i, item.Handle, filepath.Ext(sendPath))
			// Delay removal — Messages.app reads the file asynchronously after
			// osascript returns, so removing immediately can cause a broken attachment.
			go func(orig, sent string) {
				time.Sleep(10 * time.Second)
				os.Remove(orig)
				if sent != orig {
					os.Remove(sent)
				}
			}(tmpPath, sendPath)
		}
	}

	// Video attachments — send after images so order matches the LLM's
	// reply text. Each clip is written to a temp .mp4 (most-compatible
	// container for iMessage cross-device delivery) and handed to the
	// same sendFileViaMessages path images use. No transcoding —
	// gohort's downloader prefers mp4 already, and re-encoding here
	// would burn CPU on the relay machine for no clear win.
	var videoBytesSent int64
	for i, b64 := range item.Videos {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			nfo.Log("video %d decode error: %v", i, err)
			continue
		}
		ext := videoExt(data)
		tmp, err := os.CreateTemp("", "phantom-vid-*"+ext)
		if err != nil {
			nfo.Log("video %d temp file error: %v", i, err)
			continue
		}
		tmpPath := tmp.Name()
		_, err = tmp.Write(data)
		tmp.Close()
		if err != nil {
			nfo.Log("video %d write error: %v", i, err)
			os.Remove(tmpPath)
			continue
		}
		if fi, err := os.Stat(tmpPath); err == nil {
			nfo.Log("video %d sending: %s (%d bytes)", i, filepath.Ext(tmpPath), fi.Size())
		}
		if err := sendFileViaMessages(item.Handle, item.ChatID, tmpPath); err != nil {
			nfo.Log("video %d send error to %s: %v", i, item.Handle, err)
			os.Remove(tmpPath)
		} else {
			nfo.Log("video %d sent to %s (%s)", i, item.Handle, filepath.Ext(tmpPath))
			videoBytesSent += int64(len(data))
			go func(p string) {
				time.Sleep(15 * time.Second) // longer than images — videos take Messages.app a moment to ingest
				os.Remove(p)
			}(tmpPath)
		}
	}

	// Give iMessage a head start uploading the video before the text
	// reply lands. Without this, the bytes-tiny text often arrives on
	// the recipient device first because it finishes uploading while
	// the video is still in flight — making the conversation read
	// "[reply text] [video]" instead of "[video] [reply text]". Scale
	// with total video size: 2s base + 1s per MB, capped at 15s.
	if videoBytesSent > 0 && item.Text != "" {
		mb := videoBytesSent / (1024 * 1024)
		delay := 2*time.Second + time.Duration(mb)*time.Second
		if delay > 15*time.Second {
			delay = 15 * time.Second
		}
		nfo.Log("sleeping %s before text reply so video lands first (%d bytes sent)", delay, videoBytesSent)
		time.Sleep(delay)
	}

	if item.Text == "" {
		if db != nil {
			recordSentROWIDs(db, preROWID)
		}
		return
	}

	// Empty handle alone is not a skip signal — sendViaMessages routes by chat
	// GUID first (which works for owner-into-real-thread and Note-to-Self both)
	// and falls back to the secondScreen buddy "Me" service if that fails.
	// Only bail out when there is nothing at all to route on.
	if item.Handle == "" && item.ChatID == "" {
		nfo.Log("no handle or chat id, skipping: %q", truncate(item.Text, 80))
		if db != nil {
			recordSentROWIDs(db, preROWID)
		}
		return
	}

	if err := sendViaMessages(item.Handle, item.ChatID, item.Text); err != nil {
		nfo.Log("send FAILED to %s: %v", item.Handle, err)
		if attempt < maxDeliveryAttempts {
			backoff := time.Duration(1<<uint(attempt)) * 15 * time.Second
			nfo.Log("will retry in %s (attempt %d/%d)", backoff, attempt+1, maxDeliveryAttempts)
			retryMu.Lock()
			retryQueue = append(retryQueue, &retryEntry{
				item:      item,
				attempts:  attempt,
				nextRetry: time.Now().Add(backoff),
			})
			retryMu.Unlock()
		} else {
			nfo.Log("giving up on %s after %d attempts", item.ID, attempt)
		}
		return
	}
	nfo.Log("send OK to %s", item.Handle)

	// Remember the sent text so a fallback skip path can identify our own
	// outbound even if recordSentROWIDs misses the row (slow chat.db
	// commit, bridge restart, etc.).
	rememberSentText(item.Text)

	// Find the is_from_me=1 row(s) that appeared after we sent and record their
	// ROWIDs so the processing loop won't treat them as inbound messages.
	if db != nil {
		recordSentROWIDs(db, preROWID)
	}
}

// recordSentROWIDs polls for new is_from_me=1 rows above preROWID and
// adds them to sentROWIDs. Called right after a successful osascript
// delivery. The poll runs in the background so a slow chat.db commit
// doesn't stall the deliver loop, and the window is generous (15s) —
// observed in the wild that iMessage occasionally takes 5-10s to write
// the row, especially under load.
func recordSentROWIDs(db *sql.DB, preROWID int64) {
	go recordSentROWIDsBlocking(db, preROWID)
}

// recordSentROWIDsBlocking is the actual polling loop. Spawned as a
// goroutine by recordSentROWIDs.
func recordSentROWIDsBlocking(db *sql.DB, preROWID int64) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := db.Query(`SELECT ROWID FROM message WHERE is_from_me = 1 AND ROWID > ?`, preROWID)
		if err != nil {
			return
		}
		var found []int64
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				found = append(found, id)
			}
		}
		rows.Close()
		if len(found) > 0 {
			sentROWIDMu.Lock()
			for _, id := range found {
				sentROWIDs[id] = struct{}{}
				nfo.Log("recorded sent ROWID %d (will skip on next poll)", id)
			}
			sentROWIDMu.Unlock()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	nfo.Log("warning: could not find sent ROWID after delivery (pre=%d)", preROWID)
}

// flushRetryQueue retries any queued items whose backoff has elapsed.
func flushRetryQueue(cfg Config, db *sql.DB) {
	retryMu.Lock()
	now := time.Now()
	var remaining, due []*retryEntry
	for _, e := range retryQueue {
		if now.After(e.nextRetry) {
			due = append(due, e)
		} else {
			remaining = append(remaining, e)
		}
	}
	retryQueue = remaining
	retryMu.Unlock()

	for _, e := range due {
		tryDeliver(cfg, db, e.item, e.attempts+1)
	}
}

// hookErr wraps a postHook failure and signals whether the row should be
// skipped (true) or retried next poll (false).
type hookErr struct {
	msg  string
	skip bool // true = 4xx, bad data; false = network/5xx, retry
}

func (e *hookErr) Error() string { return e.msg }

// postHook sends one message event to the server.
// Returns *hookErr so callers can distinguish skip vs retry.
func postHook(cfg Config, payload HookPayload) error {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, cfg.ServerURL+"/bridges/api/hook", bytes.NewReader(body))
	if err != nil {
		return &hookErr{msg: err.Error(), skip: false}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", cfg.APIKey())
	resp, err := httpClient.Do(req)
	if err != nil {
		return &hookErr{msg: err.Error(), skip: false}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusOK {
		return nil
	}
	var rbody [256]byte
	n, _ := resp.Body.Read(rbody[:])
	msg := fmt.Sprintf("server %d: %s", resp.StatusCode, strings.TrimSpace(string(rbody[:n])))
	// 4xx = our data was bad (skip); 5xx or other = server trouble (retry)
	return &hookErr{msg: msg, skip: resp.StatusCode >= 400 && resp.StatusCode < 500}
}

// resolveChatID finds the actual service-specific chat ID from the Messages
// database for an "any;-;..." chat ID. Returns the original if already
// resolvable or not an "any" service.
func resolveChatID(dbPath, chatID string) string {
	nfo.Log("resolveChatID: called with chatID=%q dbPath=%q", chatID, dbPath)
	// The DB stores "any;-;..." GUIDs the same as the incoming chat ID,
	// so there's nothing to resolve — just return the original.
	return chatID
}

// sendViaMessages uses osascript to send a message via Messages.app.
// Text is passed as an osascript argv argument to avoid any AppleScript string escaping.
// It tries the chat GUID first (preserves group chats and existing threads),
// then falls back to an iMessage buddy lookup using either the supplied
// handle or the address parsed out of the chat GUID, then secondScreen "Me"
// as a last resort for self-conversations.
func sendViaMessages(handle, chatGUID, text string) error {
	// Try by chat GUID first (preserves the existing thread).
	if chatGUID != "" {
		script := `
on run argv
	tell application "Messages"
		set targetChat to chat id (item 1 of argv)
		send (item 2 of argv) to targetChat
	end tell
end run`
		cmd := exec.Command("osascript", "-e", script, "--", chatGUID, text)
		if err := cmd.Run(); err == nil {
			return nil
		} else {
			nfo.Log("sendViaMessages: chat GUID %q failed: %v — trying fallbacks", chatGUID, err)
		}
	}

	// If the chat GUID was something Messages couldn't resolve (e.g. the
	// "any;-;..." prefix used for cross-service or self threads), try the
	// address embedded in the GUID as an iMessage buddy.
	target := handle
	if target == "" && chatGUID != "" {
		if i := strings.LastIndex(chatGUID, ";-;"); i >= 0 {
			target = chatGUID[i+3:]
		}
	}

	if target != "" {
		script := `
on run argv
	tell application "Messages"
		set targetService to 1st service whose service type = iMessage
		set targetBuddy to buddy (item 1 of argv) of targetService
		send (item 2 of argv) to targetBuddy
	end tell
end run`
		cmd := exec.Command("osascript", "-e", script, "--", target, text)
		if err := cmd.Run(); err == nil {
			return nil
		} else {
			nfo.Log("sendViaMessages: iMessage buddy %q failed: %v", target, err)
		}
	}

	// Last resort for self-conversations: the secondScreen "Me" buddy. Only
	// works on Macs with iCloud Messages relay enabled.
	if handle == "" {
		script := `
on run argv
	tell application "Messages"
		set targetService to 1st service whose service type = secondScreen
		send (item 1 of argv) to buddy "Me" of targetService
	end tell
end run`
		cmd := exec.Command("osascript", "-e", script, "--", text)
		if err := cmd.Run(); err == nil {
			return nil
		} else {
			nfo.Log("sendViaMessages: secondScreen fallback failed: %v — giving up", err)
			return err
		}
	}

	return fmt.Errorf("no working delivery path for handle=%q chatGUID=%q", handle, chatGUID)
}

// iMessageImageMaxBytes is the cap an image attachment must fit under
// to pass through the bridge without re-encoding. iMessage refuses
// attachments somewhere around ~20MB; 18MB gives headroom for the
// container overhead added by Messages.app when it wraps the file.
// Used by the GIF-passthrough decision in the image-send loop.
const iMessageImageMaxBytes = 18 * 1024 * 1024

// convertGIFToMP4 transcodes an animated GIF to MP4 using ffmpeg.
// Used when a GIF is over the iMessage image cap — MP4 preserves the
// animation, previews inline in Messages, and is dramatically smaller
// than the equivalent GIF (typically 5-10x). Requires ffmpeg on PATH
// of the bridge machine; an unavailable ffmpeg surfaces as the caller's
// fallback path (JPEG flatten) so delivery isn't blocked.
//
// Encoder choices match what messaging clients reliably play: H.264
// with yuv420p pixel format, even-dimension scaling (libx264 refuses
// odd dimensions on some inputs), faststart so the moov atom is at
// the front (Messages and recipients can preview before the whole
// file transfers).
func convertGIFToMP4(src string) (string, error) {
	out, err := os.CreateTemp("", "phantom-gif-*.mp4")
	if err != nil {
		return "", err
	}
	outPath := out.Name()
	out.Close()
	cmd := exec.Command("ffmpeg",
		"-y", "-i", src,
		"-movflags", "+faststart",
		"-pix_fmt", "yuv420p",
		"-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2",
		"-c:v", "libx264", "-preset", "fast",
		outPath,
	)
	if b, err := cmd.CombinedOutput(); err != nil {
		os.Remove(outPath)
		return "", fmt.Errorf("ffmpeg: %s", strings.TrimSpace(string(b)))
	}
	return outPath, nil
}

// convertToJPEG uses macOS sips to convert an image file to JPEG in-place,
// writing the result to a new temp file. Returns the path of the converted file.
func convertToJPEG(src string) (string, error) {
	out, err := os.CreateTemp("", "phantom-img-*.jpg")
	if err != nil {
		return "", err
	}
	outPath := out.Name()
	out.Close()
	cmd := exec.Command("sips", "-s", "format", "jpeg", src, "--out", outPath)
	if b, err := cmd.CombinedOutput(); err != nil {
		os.Remove(outPath)
		return "", fmt.Errorf("sips: %s", strings.TrimSpace(string(b)))
	}
	return outPath, nil
}

// imageExt detects the image format from magic bytes and returns the correct
// file extension (including the dot). Defaults to ".jpg" when unknown.
func imageExt(data []byte) string {
	if len(data) >= 4 {
		switch {
		case data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G':
			return ".png"
		case data[0] == 'G' && data[1] == 'I' && data[2] == 'F':
			return ".gif"
		case data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' &&
			len(data) >= 12 && data[8] == 'W' && data[9] == 'E' && data[10] == 'B' && data[11] == 'P':
			return ".webp"
		case data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
			return ".jpg"
		}
	}
	return ".jpg"
}

// videoExt sniffs the container type from the first few bytes of a video
// blob. ISO Base Media (mp4/mov/m4v) all carry an `ftyp` box at offset 4;
// Matroska/WebM start with EBML header 0x1A45DFA3. Defaults to .mp4 since
// that's what gohort's downloader prefers and what iMessage handles best.
func videoExt(data []byte) string {
	if len(data) >= 8 && data[4] == 'f' && data[5] == 't' && data[6] == 'y' && data[7] == 'p' {
		// ISO base media — distinguish .mov from .mp4 via brand at offset 8.
		if len(data) >= 12 {
			brand := string(data[8:12])
			if brand == "qt  " {
				return ".mov"
			}
		}
		return ".mp4"
	}
	if len(data) >= 4 && data[0] == 0x1A && data[1] == 0x45 && data[2] == 0xDF && data[3] == 0xA3 {
		return ".webm"
	}
	return ".mp4"
}

// sendFileViaMessages sends a local file as an iMessage attachment via osascript.
// The real path is resolved first so /tmp (a symlink to /private/tmp on macOS)
// doesn't confuse AppleScript's alias resolution.
func sendFileViaMessages(handle, chatGUID, filePath string) error {
	// Resolve any symlinks so AppleScript gets a canonical path.
	realPath, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		realPath = filePath
	}

	if chatGUID != "" {
		script := `
on run argv
	tell application "Messages"
		set theFile to (POSIX file (item 2 of argv)) as alias
		set targetChat to chat id (item 1 of argv)
		send theFile to targetChat
	end tell
end run`
		cmd := exec.Command("osascript", "-e", script, "--", chatGUID, realPath)
		if out, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			nfo.Log("sendFile chatGUID failed (%v): %s — trying handle", err, strings.TrimSpace(string(out)))
		}
	}
	// If handle is empty, try the address parsed from the chat GUID (e.g. email).
	target := handle
	if target == "" && chatGUID != "" {
		if i := strings.LastIndex(chatGUID, ";-;"); i >= 0 {
			target = chatGUID[i+3:]
		} else if i := strings.LastIndex(chatGUID, ";+;"); i >= 0 {
			target = chatGUID[i+3:]
		}
	}
	script := `
on run argv
	tell application "Messages"
		set theFile to (POSIX file (item 2 of argv)) as alias
		set targetService to 1st service whose service type = iMessage
		set targetBuddy to buddy (item 1 of argv) of targetService
		send theFile to targetBuddy
	end tell
end run`
	if out, err := exec.Command("osascript", "-e", script, "--", target, realPath).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// latestMessageRowID returns the current max ROWID across all messages (sent + received).
func latestMessageRowID(dbPath string) int64 {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return 0
	}
	defer db.Close()
	var id int64
	db.QueryRow(`SELECT IFNULL(MAX(ROWID),0) FROM message`).Scan(&id)
	return id
}

// appleTimeToRFC3339 converts Apple's epoch (nanoseconds since 2001-01-01) to RFC3339.
// Older macOS versions used seconds; newer use nanoseconds. We detect by magnitude.
func appleTimeToRFC3339(appleTime int64) string {
	appleEpoch := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	var t time.Time
	if appleTime > 1e15 {
		// nanoseconds
		t = appleEpoch.Add(time.Duration(appleTime))
	} else {
		// seconds
		t = appleEpoch.Add(time.Duration(appleTime) * time.Second)
	}
	return t.UTC().Format(time.RFC3339)
}

// --- Config ---

// --- Contacts lookup ---
