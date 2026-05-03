// phantom-bridge bridges the local macOS Messages app to a remote gohort
// Phantom server. Successor to the older `phantom-agent` name; existing
// installs migrate via tools/migrate-phantom-agent-to-bridge.sh.
//
// It watches ~/Library/Messages/chat.db for new incoming messages and POSTs
// them to the server. It polls the server for queued replies and delivers them
// via AppleScript (osascript).
//
// Build on macOS:
//
//	cd apps/phantom/_bridge && go build -o phantom-bridge .
//
// First run: phantom-bridge --setup
// Normal run: phantom-bridge (or install as a launchd service)
package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"encoding/json"

	"github.com/cmcoffee/snugforge/cfg"
	"github.com/cmcoffee/snugforge/eflag"
	"github.com/cmcoffee/snugforge/nfo"
	_ "modernc.org/sqlite"
)

const defaultPollInterval = 5 * time.Second
const defaultDB = "~/Library/Messages/chat.db"

// Config is loaded from ~/.phantom-bridge.cfg (INI format).
type Config struct {
	ServerURL string // e.g. "https://gohort.example.com/phantom"
	APIKey    string
	PollSecs  int    // default 10
	DBPath    string // path to chat.db (for resolving "any;-;..." chat IDs)
}

// HookPayload is what we POST to /api/hook.
type HookPayload struct {
	RowID       int64    `json:"row_id,omitempty"` // DB ROWID for server-side deduplication
	ChatID      string   `json:"chat_id"`
	Handle      string   `json:"handle"`
	DisplayName string   `json:"display_name"`
	Text        string   `json:"text"`
	Timestamp   string   `json:"timestamp"`
	Images      []string `json:"images,omitempty"` // base64-encoded image data
	Videos      []string `json:"videos,omitempty"` // base64-encoded video data; phantom samples frames + extracts metadata server-side
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
	sentROWIDMu   sync.Mutex
	sentROWIDs    = map[int64]struct{}{} // ROWIDs of is_from_me=1 rows we sent; skip these in the processing loop
)

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func main() {
	home, _ := os.UserHomeDir()
	plistFile := filepath.Join(home, "Library", "LaunchAgents", "com.gohort.phantom-bridge.plist")
	logFile := filepath.Join(home, "phantom-bridge.log")

	eflag.Header(fmt.Sprintf(`phantom-bridge — bridges macOS Messages to a gohort Phantom server.

USAGE
  phantom-bridge [flags]

FIRST-TIME SETUP
  1. make install               # build and copy to ~/bin/phantom-bridge
  2. phantom-bridge --setup     # enter server URL and API key
  3. phantom-bridge --install   # register as a login item (launchd)
  4. phantom-bridge --test      # verify chat.db access

LAUNCHD SERVICE
  Logs:  %s
  Plist: %s

PERMISSIONS
  Terminal (or the binary) must have Full Disk Access:
  System Settings → Privacy & Security → Full Disk Access

FLAGS`, logFile, plistFile))

	setup     := eflag.Bool("setup",      "interactive setup wizard (run this first)")
	install   := eflag.Bool("install",    "install as a launchd service (starts at login)")
	uninstall := eflag.Bool("uninstall",  "remove the launchd service")
	test      := eflag.Bool("test",       "print recent messages from chat.db and exit")
	fromStart := eflag.Bool("from-start", "process all existing messages instead of only new ones")
	verbose   := eflag.Bool("verbose",    "log every poll cycle even when no new messages are found")
	cfgPath   := eflag.String("config",   configPath(), "path to config file")

	eflag.Parse()

	if *install {
		runInstall()
		return
	}
	if *uninstall {
		runUninstall()
		return
	}

	dbPath := expandHome(defaultDB)

	if *test {
		runTest(dbPath)
		return
	}

	if *setup {
		runSetup(*cfgPath)
		return
	}

	agentCfg, err := loadConfig(*cfgPath)
	if err != nil {
		nfo.Fatal("config error: %v\n(run phantom-bridge --setup to configure)", err)
	}
	agentCfg.DBPath = dbPath

	// Set up file logging — writes to ~/phantom-bridge.log alongside stdout.
	if _, err := nfo.LogFile(logFile, 10, 1); err != nil {
		nfo.Fatal("cannot open log file %s: %v", logFile, err)
	}

	interval := time.Duration(agentCfg.PollSecs) * time.Second
	if interval < 5*time.Second {
		interval = defaultPollInterval
	}

	// Wait until chat.db is accessible (Full Disk Access may not be granted yet).
	var db *sql.DB
	for {
		if _, err := os.Stat(dbPath); err != nil {
			nfo.Log("chat.db not accessible (%v) — retrying in %s\n"+
				"Make sure Terminal has Full Disk Access:\n"+
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
				"Make sure Terminal has Full Disk Access:\n"+
				"System Settings → Privacy & Security → Full Disk Access", err, interval)
			time.Sleep(interval)
			continue
		}
		db = d
		break
	}
	defer db.Close()

	var lastRowID int64
	if !*fromStart {
		lastRowID = latestMessageRowID(dbPath)
	}

	hasBody := hasColumn(db, "message", "attributedBody")
	nfo.Log("phantom-bridge started. db=%s server=%s poll=%s last_rowid=%d attributedBody=%v",
		dbPath, agentCfg.ServerURL, interval, lastRowID, hasBody)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		prev := lastRowID
		lastRowID = processNewMessages(agentCfg, db, hasBody, lastRowID, *verbose)
		if *verbose && lastRowID == prev {
			nfo.Log("poll: no new messages (watermark=%d)", lastRowID)
		}
		deliverOutbox(agentCfg, db)
	}
}

// hasColumn returns true if the named column exists in the named table.
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

		// Skip is_from_me=1 rows that we know we sent (ROWID recorded at delivery time).
		// User self-messages are NOT in this set and pass through normally.
		if isFromMe == 1 {
			sentROWIDMu.Lock()
			_, skip := sentROWIDs[rowID]
			sentROWIDMu.Unlock()
			if skip {
				nfo.Log("rowid=%d: skipping our own outgoing reply", rowID)
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
		if text == "" && len(images) == 0 && len(videos) == 0 {
			var attachCount int
			db.QueryRow(`SELECT COUNT(*) FROM message_attachment_join WHERE message_id = ?`, rowID).Scan(&attachCount)
			if attachCount > 0 {
				// Has attachments but couldn't decode them — send a placeholder.
				nfo.Log("rowid=%d: %d attachment(s), none processable — using [Image] placeholder", rowID, attachCount)
				text = "[Image]"
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
				nfo.Log("rowid=%d handle=%q: still empty after %d polls, skipping", rowID, handle, polls)
				emptyRowMu.Lock()
				delete(emptyRowSeen, rowID)
				emptyRowMu.Unlock()
				newMax = rowID
				continue
			}
		}

		// Resolve display name: prefer group chat name, then Contacts lookup, then handle.
		displayName := chatDisplayName
		if displayName == "" {
			displayName = lookupContact(handle)
		}

		payload := HookPayload{
			RowID:       rowID,
			ChatID:      chatID,
			Handle:      handle,
			DisplayName: displayName,
			Text:        text,
			Timestamp:   ts,
			Images:      images,
			Videos:      videos,
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
		case len(images) > 0 && len(videos) > 0:
			nfo.Log("relayed [%d] from %s: %q [%d image(s), %d video(s)]", rowID, handle, truncate(text, 60), len(images), len(videos))
		case len(videos) > 0:
			nfo.Log("relayed [%d] from %s: %q [%d video(s)]", rowID, handle, truncate(text, 60), len(videos))
		case len(images) > 0:
			nfo.Log("relayed [%d] from %s: %q [%d image(s)]", rowID, handle, truncate(text, 60), len(images))
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
	return extractTextHeuristic(body)
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

// extractTextHeuristic is the fallback: find all printable UTF-8 runs
// in the blob and join them. No filtering — gohort cleans the output.
func extractTextHeuristic(body []byte) string {
	var parts []string
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
		if len(s) >= 2 {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

// readImageAttachments returns base64-encoded image data for images attached to messageID.
// Converts HEIC to JPEG. Reads up to 3 images to keep payloads manageable.
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

		// Convert all non-JPEG images to JPEG — many vision models (including
		// Ollama-hosted Gemma) only accept JPEG in their image projection layer.
		isJPEG := mimeType == "image/jpeg" ||
			strings.HasSuffix(strings.ToLower(filename), ".jpg") ||
			strings.HasSuffix(strings.ToLower(filename), ".jpeg")
		if !isJPEG {
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

	req, _ := http.NewRequest(http.MethodGet, cfg.ServerURL+"/api/poll", nil)
	req.Header.Set("X-API-Key", cfg.APIKey)
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
		// WebP and GIF (animated aside) are unreliable in iMessage cross-device
		// delivery. Convert to JPEG using macOS sips before sending.
		sendPath := tmpPath
		if ext == ".webp" || ext == ".gif" {
			if converted, err := convertToJPEG(tmpPath); err == nil {
				sendPath = converted
			} else {
				nfo.Log("image %d convert warning: %v — sending original", i, err)
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
			go func(p string) {
				time.Sleep(15 * time.Second) // longer than images — videos take Messages.app a moment to ingest
				os.Remove(p)
			}(tmpPath)
		}
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

	// Find the is_from_me=1 row(s) that appeared after we sent and record their
	// ROWIDs so the processing loop won't treat them as inbound messages.
	if db != nil {
		recordSentROWIDs(db, preROWID)
	}
}

// recordSentROWIDs polls briefly for new is_from_me=1 rows above preROWID and
// adds them to sentROWIDs. Called right after a successful osascript delivery.
func recordSentROWIDs(db *sql.DB, preROWID int64) {
	deadline := time.Now().Add(2 * time.Second)
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
	req, err := http.NewRequest(http.MethodPost, cfg.ServerURL+"/api/hook", bytes.NewReader(body))
	if err != nil {
		return &hookErr{msg: err.Error(), skip: false}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", cfg.APIKey)
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

var (
	contactsMu    sync.RWMutex
	contactsCache map[string]string // normalised phone/email → display name
	contactsBuilt bool
)

var nonDigit = regexp.MustCompile(`\D`)

// normalisePhone strips all non-digit characters and returns the last 10 digits,
// which is sufficient to match US numbers stored in various formats.
func normalisePhone(s string) string {
	digits := nonDigit.ReplaceAllString(s, "")
	if len(digits) > 10 {
		digits = digits[len(digits)-10:]
	}
	return digits
}

// lookupContact returns the display name for a phone number or email address
// by querying the macOS AddressBook SQLite databases. Results are cached.
func lookupContact(handle string) string {
	if handle == "" {
		return ""
	}

	contactsMu.RLock()
	if contactsBuilt {
		name := contactsCache[strings.ToLower(handle)]
		contactsMu.RUnlock()
		return name
	}
	contactsMu.RUnlock()

	buildContactsCache()

	contactsMu.RLock()
	defer contactsMu.RUnlock()
	return contactsCache[strings.ToLower(handle)]
}

// buildContactsCache loads all contacts from all AddressBook sources into memory.
func buildContactsCache() {
	contactsMu.Lock()
	defer contactsMu.Unlock()
	if contactsBuilt {
		return
	}
	contactsCache = map[string]string{}
	contactsBuilt = true

	home, _ := os.UserHomeDir()
	// AddressBook databases live under ~/Library/Application Support/AddressBook/
	// either directly or under per-source subdirectories.
	pattern := filepath.Join(home, "Library", "Application Support", "AddressBook", "**", "AddressBook-v22.abcddb")
	matches, _ := filepath.Glob(filepath.Join(home, "Library", "Application Support", "AddressBook", "AddressBook-v22.abcddb"))
	// Also search one level of Sources subdirs.
	sub, _ := filepath.Glob(filepath.Join(home, "Library", "Application Support", "AddressBook", "Sources", "*", "AddressBook-v22.abcddb"))
	matches = append(matches, sub...)
	_ = pattern

	for _, dbPath := range matches {
		loadAddressBook(dbPath)
	}
	nfo.Log("contacts: loaded %d entries", len(contactsCache))
}

// loadAddressBook reads one AddressBook database and merges contacts into the cache.
func loadAddressBook(dbPath string) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_busy_timeout=2000")
	if err != nil {
		return
	}
	defer db.Close()

	// Build name map: Z_PK → display name
	names := map[int]string{}
	rows, err := db.Query(`SELECT Z_PK, ZFIRSTNAME, ZLASTNAME, ZNICKNAME, ZORGANIZATION FROM ZABCDRECORD`)
	if err != nil {
		return
	}
	for rows.Next() {
		var pk int
		var first, last, nick, org sql.NullString
		if rows.Scan(&pk, &first, &last, &nick, &org) != nil {
			continue
		}
		var name string
		switch {
		case first.Valid && last.Valid:
			name = strings.TrimSpace(first.String + " " + last.String)
		case first.Valid:
			name = first.String
		case last.Valid:
			name = last.String
		case nick.Valid:
			name = nick.String
		case org.Valid:
			name = org.String
		}
		if name != "" {
			names[pk] = name
		}
	}
	rows.Close()

	// Map phone numbers → name
	rows, err = db.Query(`SELECT ZOWNER, ZFULLNUMBER, ZVALUE FROM ZABCDPHONENUMBER`)
	if err == nil {
		for rows.Next() {
			var owner int
			var full, val sql.NullString
			if rows.Scan(&owner, &full, &val) != nil {
				continue
			}
			name := names[owner]
			if name == "" {
				continue
			}
			for _, raw := range []string{full.String, val.String} {
				if raw == "" {
					continue
				}
				// Store both E.164 full form and normalised 10-digit form.
				contactsCache[strings.ToLower(raw)] = name
				norm := normalisePhone(raw)
				if norm != "" {
					contactsCache[norm] = name
				}
			}
		}
		rows.Close()
	}

	// Map email addresses → name
	rows, err = db.Query(`SELECT ZOWNER, ZADDRESS FROM ZABCDEMAILADDRESS`)
	if err == nil {
		for rows.Next() {
			var owner int
			var addr sql.NullString
			if rows.Scan(&owner, &addr) != nil {
				continue
			}
			if addr.Valid && addr.String != "" {
				name := names[owner]
				if name != "" {
					contactsCache[strings.ToLower(addr.String)] = name
				}
			}
		}
		rows.Close()
	}
}

const launchdLabel = "com.gohort.phantom-bridge"

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

func runInstall() {
	exe, err := os.Executable()
	if err != nil {
		nfo.Fatal("cannot determine executable path: %v", err)
	}
	// Resolve symlinks so launchd gets the real binary path.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	home, _ := os.UserHomeDir()
	logFile := filepath.Join(home, "phantom-bridge.log")

	// Verify the binary actually exists at that path before baking it into the plist.
	if _, err := os.Stat(exe); err != nil {
		nfo.Fatal("binary not found at %s: %v", exe, err)
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>KeepAlive</key>
	<true/>
	<key>RunAtLoad</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, launchdLabel, exe, logFile, logFile)

	path := plistPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		nfo.Fatal("cannot create LaunchAgents dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(plist), 0644); err != nil {
		nfo.Fatal("cannot write plist: %v", err)
	}
	nfo.Log("binary:  %s", exe)
	nfo.Log("plist:   %s", path)

	// Validate the plist before attempting to load it.
	if out, err := exec.Command("plutil", "-lint", path).CombinedOutput(); err != nil {
		nfo.Log("plist validation failed: %s", strings.TrimSpace(string(out)))
		nfo.Log("plist content:\n%s", plist)
		nfo.Fatal("aborting install — fix the plist and retry")
	}

	uid := fmt.Sprintf("gui/%d", os.Getuid())
	svcID := uid + "/" + launchdLabel

	// Bootout first in case a previous version is already registered.
	exec.Command("launchctl", "bootout", uid, path).Run()
	// Re-enable in case bootout (or a prior unload) left the service marked disabled
	// in launchd's internal database — bootstrap silently fails (error 5) if disabled.
	exec.Command("launchctl", "enable", svcID).Run()

	out, err := exec.Command("launchctl", "bootstrap", uid, path).CombinedOutput()
	if err != nil {
		nfo.Log("auto-load failed: %s", strings.TrimSpace(string(out)))
		nfo.Log("")
		nfo.Log("Load manually:")
		nfo.Log("  launchctl enable %s", svcID)
		nfo.Log("  launchctl bootstrap %s %s", uid, path)
	} else {
		nfo.Log("service loaded — phantom-bridge will start at login")
	}

	nfo.Log("")
	nfo.Log("Manage the service with:")
	nfo.Log("  Load:    launchctl bootstrap %s %s", uid, path)
	nfo.Log("  Unload:  launchctl bootout %s %s", uid, path)
	nfo.Log("  Status:  launchctl list | grep phantom")
	nfo.Log("  Logs:    tail -f %s", logFile)
}

func runUninstall() {
	path := plistPath()
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	exec.Command("launchctl", "bootout", uid, path).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		nfo.Fatal("cannot remove plist: %v", err)
	}
	nfo.Log("service removed: %s", path)
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".phantom-bridge.cfg")
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func loadConfig(path string) (Config, error) {
	var store cfg.Store
	if err := store.File(path); err != nil {
		return Config{}, fmt.Errorf("cannot read %s: %w", path, err)
	}
	serverURL := store.SGet("phantom", "server_url")
	apiKey := store.SGet("phantom", "api_key")
	if serverURL == "" || apiKey == "" {
		return Config{}, fmt.Errorf("server_url and api_key are required in %s", path)
	}
	pollSecs := int(store.GetInt("phantom", "poll_seconds"))
	if pollSecs == 0 {
		pollSecs = 10
	}
	return Config{ServerURL: serverURL, APIKey: apiKey, PollSecs: pollSecs}, nil
}

func runSetup(path string) {
	// Pre-populate with existing values so the menu shows current config.
	existing, _ := loadConfig(path)
	if existing.PollSecs == 0 {
		existing.PollSecs = 10
	}

	serverURL := existing.ServerURL
	apiKey := existing.APIKey
	pollSecs := existing.PollSecs

	menu := nfo.NewOptions("--- Phantom Bridge Configuration ---", "(selection or 'q' to save & exit)", 'q')
	menu.StringVar(&serverURL, "Gohort Server URL", serverURL, "Base URL of the phantom server (e.g. https://gohort.example.com).")
	menu.SecretVar(&apiKey, "API Key", apiKey, "Generate one in the Phantom web UI under API Keys.")
	menu.IntVar(&pollSecs, "Poll Interval (seconds)", pollSecs, "How often to check for new messages.", 5, 300)
	menu.Select(false)

	serverURL = strings.TrimRight(serverURL, "/")
	content := fmt.Sprintf("[phantom]\nserver_url = %s\napi_key = %s\npoll_seconds = %d\n", serverURL, apiKey, pollSecs)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		nfo.Fatal("cannot write config: %v", err)
	}
	nfo.Log("Config saved to %s", path)
	nfo.Log("Next: run phantom-bridge --install to register the launchd service.")
}
