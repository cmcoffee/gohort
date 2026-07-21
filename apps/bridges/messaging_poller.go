// Server-side messaging poller — the bridges half of the rest_messaging connector
// kind (core/connector_restmessaging.go). For each approved rest_messaging
// connector it runs one goroutine that, every interval:
//
//  1. polls the service's REST list endpoint through the connector's SecureAPI
//     credential (Microsoft Graph channel-message delta, Slack conversations, …),
//  2. maps each new message onto the existing Bridges inbound contract via the
//     connector's declarative dot-paths and feeds it IN-PROCESS through
//     ingestInbound — reusing dedup, channel binding, agent dispatch, and outbox,
//  3. advances a persisted cursor (Graph deltaLink / a query-param cursor),
//  4. drains that service's outbox and delivers each bound agent's reply back out
//     the service's send endpoint.
//
// No public endpoint, no per-service Go code — a Builder authors the spec (or
// picks a preset) and this loop makes it a two-sided bridge. Registered with core
// at route time via RegisterMessagingPoller / RegisterMessagingProbe (web.go).
package bridges

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const pollCursorTable = "bridges_poll_cursor" // connector name → pollCursor

// maxPollPages bounds within-tick pagination (Graph @odata.nextLink) so a channel
// with a huge history can't spin a poll forever.
const maxPollPages = 50

// pollCursor is the persisted per-connector pagination state.
type pollCursor struct {
	NextURL string `json:"next_url,omitempty"` // Graph deltaLink — replaces PollURL next tick
	Cursor  string `json:"cursor,omitempty"`   // opaque cursor injected as CursorParam next tick
}

// pollerReg tracks the running poll loops so a re-materialize (admin re-approve,
// startup reload) or teardown can stop the prior loop. Process-scoped.
var (
	pollerMu     sync.Mutex
	pollerCancel = map[string]context.CancelFunc{}
)

// startPoller launches (or restarts) the poll loop for a connector. Idempotent —
// stops any prior loop of the same name first.
func (T *Bridges) startPoller(c Connector) error {
	var spec RestMessagingSpec
	if err := json.Unmarshal(c.Spec, &spec); err != nil {
		return fmt.Errorf("bad rest_messaging spec for %q: %w", c.Name, err)
	}
	pollerMu.Lock()
	if cancel, ok := pollerCancel[c.Name]; ok {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	pollerCancel[c.Name] = cancel
	pollerMu.Unlock()

	// The synthetic key carries the owner + service so ingestInbound routes to the
	// owner's bound channel and namespaces the outbox — same fields the iMessage
	// daemon's key supplies. Enabled=true; unapprove stops the loop entirely.
	key := BridgeKey{Owner: c.Owner, Service: spec.Service, Enabled: true, Name: c.Name}

	// Webhook mode: inbound arrives on the public /api/webhook/<name> route (push),
	// not by polling. Generate + store the provider's secret if it self-provisions
	// one (Graph clientState), then run an outbound-only loop to deliver replies —
	// there's no poll tick to drain the outbox otherwise.
	if spec.WebhookProvider != "" {
		if p, ok := webhookProviders[spec.WebhookProvider]; ok {
			if sec, gen := p.autoSecret(); gen && T.getWebhookSecret(c.Name) == "" {
				T.setWebhookSecret(c.Name, sec)
				Log("[bridges] webhook %q generated %s clientState secret", c.Name, spec.WebhookProvider)
			}
		}
		// Graph pushes only while a live subscription points at our route. Register
		// it with the core push-sub primitive, which creates it NOW (synchronously,
		// so a failure — e.g. our URL isn't publicly reachable — surfaces to the
		// approving admin) and then owns renewal + restart recovery via one sweeper.
		if spec.WebhookProvider == "graph" {
			if err := EnsurePushSubscription(graphPushKind, c.Name, graphRenewBefore); err != nil {
				return fmt.Errorf("graph subscription: %w", err)
			}
		}
		go T.outboundLoop(ctx, c, spec)
		return nil
	}

	go func() {
		// Cold-start seed: on the very FIRST start (no cursor persisted yet), advance
		// the cursor past current history WITHOUT ingesting, so we don't replay the
		// whole channel and wake the agent on old messages. A restart finds the
		// persisted cursor and skips this, resuming with no message loss.
		if cur := T.getPollCursor(c.Name); cur.NextURL == "" && cur.Cursor == "" {
			if err := T.pollOnce(ctx, c, spec, key, false); err != nil && ctx.Err() == nil {
				Warn("[bridges] messaging poller %q seed failed (first tick will ingest from scratch): %v", c.Name, err)
			}
		}
		T.pollLoop(ctx, c, spec, key)
	}()
	return nil
}

// stopPoller cancels a connector's poll loop, if running.
func (T *Bridges) stopPoller(name string) {
	pollerMu.Lock()
	if cancel, ok := pollerCancel[name]; ok {
		cancel()
		delete(pollerCancel, name)
	}
	pollerMu.Unlock()
}

func (T *Bridges) pollLoop(ctx context.Context, c Connector, spec RestMessagingSpec, key BridgeKey) {
	interval := time.Duration(spec.IntervalSecs) * time.Second
	if interval < 5*time.Second {
		interval = 30 * time.Second
	}
	Log("[bridges] messaging poller %q started (svc=%s, every %s)", c.Name, spec.Service, interval)

	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if err := T.pollOnce(ctx, c, spec, key, true); err != nil && ctx.Err() == nil {
			Warn("[bridges] messaging poller %q: %v", c.Name, err)
		}
		select {
		case <-ctx.Done():
			Log("[bridges] messaging poller %q stopped", c.Name)
			return
		case <-tick.C:
		}
	}
}

// outboundLoop drives reply delivery for a WEBHOOK connector, whose inbound is
// push (no poll tick to drain the outbox). It just runs deliverOutbound on a short
// timer so a bound agent's reply reaches the service within a few seconds.
func (T *Bridges) outboundLoop(ctx context.Context, c Connector, spec RestMessagingSpec) {
	Log("[bridges] webhook %q outbound loop started (svc=%s, provider=%s)", c.Name, spec.Service, spec.WebhookProvider)
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		T.deliverOutbound(ctx, spec)
		select {
		case <-ctx.Done():
			Log("[bridges] webhook %q outbound loop stopped", c.Name)
			return
		case <-tick.C:
		}
	}
}

// pollOnce does one poll → map → ingest → advance-cursor → deliver-outbound cycle.
// It follows MoreURLPath pagination WITHIN the tick until the page carrying the
// cross-tick cursor (Graph deltaLink). With ingest=false it advances the cursor
// without delivering messages — the cold-start seed.
func (T *Bridges) pollOnce(ctx context.Context, c Connector, spec RestMessagingSpec, key BridgeKey, ingest bool) error {
	method := firstNonEmpty(spec.Method, "GET")
	cur := T.getPollCursor(c.Name)
	newCur := cur
	pollURL := spec.PollURL
	if cur.NextURL != "" {
		pollURL = cur.NextURL // Graph deltaLink / nextLink is a complete URL
	} else if cur.Cursor != "" && spec.CursorParam != "" {
		pollURL = addQueryParam(pollURL, spec.CursorParam, cur.Cursor)
	}

	for pages := 0; ; pages++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		body, status, err := authedRequest(spec.Credential, method, pollURL, spec.Body)
		if err != nil {
			return err
		}
		if status == 0 || status >= 300 {
			return fmt.Errorf("poll %s → HTTP %d: %s", pollURL, status, snippet(body))
		}
		var root any
		if err := json.Unmarshal([]byte(body), &root); err != nil {
			return fmt.Errorf("poll response not JSON: %w", err)
		}
		if ingest {
			for _, req := range messagesFromResponse(root, spec) {
				T.ingestInbound(key, req)
			}
		}
		if spec.NextURLPath != "" {
			if v := jsonPathString(root, spec.NextURLPath); v != "" {
				newCur.NextURL = v
			}
		}
		if spec.CursorPath != "" {
			if v := jsonPathString(root, spec.CursorPath); v != "" {
				newCur.Cursor = v
			}
		}
		// Follow next-page URL within this tick (Graph nextLink); stop at the page
		// that had none (it carried the deltaLink above).
		if spec.MoreURLPath == "" {
			break
		}
		more := jsonPathString(root, spec.MoreURLPath)
		if more == "" {
			break
		}
		if pages+1 >= maxPollPages {
			Warn("[bridges] messaging poller %q hit the %d-page cap for svc=%s — cursor may lag; widen the interval or narrow the query", c.Name, maxPollPages, spec.Service)
			break
		}
		pollURL = more
	}

	if newCur != cur {
		T.setPollCursor(c.Name, newCur)
	}
	if ingest {
		T.deliverOutbound(ctx, spec)
	}
	return nil
}

// deliverOutbound drains the service's outbox and posts each reply back out. On a
// send failure the item AND every not-yet-sent item are re-queued so nothing is
// lost, and delivery retries next tick.
func (T *Bridges) deliverOutbound(ctx context.Context, spec RestMessagingSpec) {
	if spec.SendURL == "" {
		// Inbound-only connector (no outbound leg). If a bidirectional channel is
		// bound to this service, its agent's replies enqueue with nothing to drain
		// them — they'd pile up in the uncapped outbox silently. Surface it (guards
		// leave breadcrumbs) so the misconfiguration is visible, not invisible.
		if n := T.pendingOutboxCount(spec.Service); n > 0 {
			Warn("[bridges] %d queued reply(ies) for svc=%q but its rest_messaging connector has no send_url — set the bound channel to inbound-only, or add send_url to deliver them", n, spec.Service)
		}
		return
	}
	method := firstNonEmpty(spec.SendMethod, "POST")
	items := T.drainOutbox(spec.Service)
	for i, it := range items {
		if ctx.Err() != nil {
			// Shutting down mid-drain: re-queue the rest so nothing is lost.
			for _, r := range items[i:] {
				T.enqueueOutbox(r)
			}
			return
		}
		if strings.TrimSpace(it.Text) == "" {
			continue
		}
		sendURL := strings.ReplaceAll(spec.SendURL, "{chat_id}", it.ChatID)
		reqBody := renderSendBody(spec.SendBody, it.ChatID, it.Text)
		_, status, err := authedRequest(spec.Credential, method, sendURL, reqBody)
		if err != nil || status >= 300 {
			Warn("[bridges] messaging send failed (svc=%s chat=%s): err=%v status=%d — re-queued %d item(s)",
				spec.Service, it.ChatID, err, status, len(items)-i)
			for _, r := range items[i:] {
				T.enqueueOutbox(r)
			}
			return
		}
	}
}

// probeMessaging does ONE poll + maps the first message and returns a preview,
// without ingesting — the connector `test` action so a Builder can verify a
// mapping before going live. Needs the credential enabled (admin), else the
// SecureAPI authorize step reports it's still pending.
func (T *Bridges) probeMessaging(c Connector) (string, error) {
	var spec RestMessagingSpec
	if err := json.Unmarshal(c.Spec, &spec); err != nil {
		return "", err
	}
	pollURL := spec.PollURL
	if cur := T.getPollCursor(c.Name); cur.NextURL != "" {
		pollURL = cur.NextURL
	}
	body, status, err := authedRequest(spec.Credential, firstNonEmpty(spec.Method, "GET"), pollURL, spec.Body)
	if err != nil {
		return "", err
	}
	if status == 0 || status >= 300 {
		return "", fmt.Errorf("poll %s → HTTP %d: %s", pollURL, status, snippet(body))
	}
	var root any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return "", fmt.Errorf("response not JSON: %w", err)
	}
	msgs := messagesFromResponse(root, spec)
	var b strings.Builder
	fmt.Fprintf(&b, "Polled %s (HTTP %d) — mapped %d message(s).", pollURL, status, len(msgs))
	if len(msgs) > 0 {
		m := msgs[0]
		fmt.Fprintf(&b, "\nFirst message → chat_id=%q sender=%q name=%q text=%q",
			m.ChatID, m.Handle, m.DisplayName, truncateText(m.Text, 140))
	} else {
		b.WriteString("\n(no messages matched — check list_path and map.chat_id/map.text against the raw response shape)")
	}
	return b.String(), nil
}

// --- mapping ------------------------------------------------------------------

// messagesFromResponse resolves the list of messages from a parsed response and
// maps each into a hookRequest via the spec's dot-paths, dropping any element
// missing a chat id or text.
func messagesFromResponse(root any, spec RestMessagingSpec) []hookRequest {
	var listNode any = root
	if spec.ListPath != "" {
		listNode = resolveJSONPath(root, spec.ListPath)
	}
	arr, ok := listNode.([]any)
	if !ok {
		return nil
	}
	m := spec.Map
	var out []hookRequest
	for _, e := range arr {
		chatID := spec.ChatIDConst
		if chatID == "" {
			chatID = jsonPathString(e, m.ChatID)
		}
		req := hookRequest{
			ChatID:           chatID,
			MsgID:            jsonPathString(e, m.MsgID),
			Handle:           jsonPathString(e, m.Sender),
			DisplayName:      jsonPathString(e, m.SenderName),
			Text:             jsonPathString(e, m.Text),
			ConversationName: jsonPathString(e, m.ConvName),
		}
		if spec.StripHTML {
			req.Text = stripHTML(req.Text)
		}
		if strings.TrimSpace(req.ChatID) == "" || strings.TrimSpace(req.Text) == "" {
			continue
		}
		out = append(out, req)
	}
	return out
}

// resolveJSONPath walks a decoded JSON value by a dot-path. At each level, on a
// MAP it first tries the WHOLE remaining path as a literal key — so a key that
// itself contains dots (Graph's "@odata.deltaLink") resolves — then splits on the
// next dot; on an ARRAY a numeric segment indexes into it (e.g. "messages.0.ts"
// for Slack's newest message). Returns nil on any miss.
func resolveJSONPath(node any, path string) any {
	path = strings.TrimSpace(path)
	for {
		if path == "" {
			return node
		}
		if m, ok := node.(map[string]any); ok {
			if v, ok := m[path]; ok {
				return v
			}
		}
		seg, rest := path, ""
		if i := strings.IndexByte(path, '.'); i >= 0 {
			seg, rest = path[:i], path[i+1:]
		}
		switch n := node.(type) {
		case map[string]any:
			v, ok := n[seg]
			if !ok {
				return nil
			}
			node = v
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(n) {
				return nil
			}
			node = n[idx]
		default:
			return nil
		}
		path = rest
	}
}

// jsonPathString resolves a path to its string form (numbers/bools coerced).
func jsonPathString(node any, path string) string {
	if path == "" {
		return ""
	}
	switch t := resolveJSONPath(node, path).(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		return t.String()
	}
	return ""
}

// --- helpers ------------------------------------------------------------------

// pendingOutboxCount reports how many items are queued for a service (used to
// surface replies stranded by an inbound-only connector).
func (T *Bridges) pendingOutboxCount(service string) int {
	n := 0
	for _, id := range T.DB.Keys(outboxTable) {
		var it OutboxItem
		if T.DB.Get(outboxTable, id, &it) && it.Service == service {
			n++
		}
	}
	return n
}

func (T *Bridges) getPollCursor(name string) pollCursor {
	var c pollCursor
	T.DB.Get(pollCursorTable, name, &c)
	return c
}

func (T *Bridges) setPollCursor(name string, c pollCursor) {
	T.DB.Set(pollCursorTable, name, c)
}

// authedRequest issues one credential-authorized request through SecureAPI's
// central dispatch — so EVERY credential type works (bearer for Slack, oauth2 for
// Graph, header/basic/…), and the credential's URL allow-list, rate limit, audit,
// and secret redaction all apply. dispatch returns an LLM-formatted string ("HTTP
// <status> <text>\n<pretty-json>"); parseDispatchResult splits it back to a status
// + raw body for the poller to parse.
func authedRequest(cred, method, rawURL, body string) (string, int, error) {
	out, err := Secure().DispatchToolCall(nil, cred, rawURL, method, body)
	if err != nil {
		return "", 0, err
	}
	status, respBody := parseDispatchResult(out)
	return respBody, status, nil
}

// parseDispatchResult peels the "HTTP <status> <text>" header line that dispatch
// prepends and drops a trailing truncation marker, leaving the (JSON) body.
func parseDispatchResult(s string) (int, string) {
	nl := strings.IndexByte(s, '\n')
	if nl < 0 {
		return 0, s
	}
	var status int
	fmt.Sscanf(s[:nl], "HTTP %d", &status)
	body := s[nl+1:]
	if i := strings.Index(body, "\n... [TRUNCATED"); i >= 0 {
		body = body[:i]
	}
	return status, body
}

// renderSendBody fills {text}/{chat_id} in the send-body template, JSON-escaping
// each so a quote or newline in a reply can't break the JSON. Empty template = the
// raw text.
func renderSendBody(tmpl, chatID, text string) string {
	if tmpl == "" {
		return text
	}
	tmpl = strings.ReplaceAll(tmpl, "{text}", jsonEscape(text))
	tmpl = strings.ReplaceAll(tmpl, "{chat_id}", jsonEscape(chatID))
	return tmpl
}

// jsonEscape returns s escaped for embedding inside a JSON string literal (no
// surrounding quotes).
func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return s
	}
	return string(b[1 : len(b)-1])
}

func addQueryParam(rawURL, k, v string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set(k, v)
	u.RawQuery = q.Encode()
	return u.String()
}

var htmlTagRE = regexp.MustCompile(`<[^>]+>`)

// stripHTML removes tags and unescapes entities — Graph message bodies are HTML.
func stripHTML(s string) string {
	if s == "" {
		return s
	}
	return strings.TrimSpace(html.UnescapeString(htmlTagRE.ReplaceAllString(s, "")))
}

func snippet(b string) string {
	s := strings.TrimSpace(b)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
