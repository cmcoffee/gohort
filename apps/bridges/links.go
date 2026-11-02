package bridges

// Linked services: a messaging API reached through a service connector that
// gives message.fetch and message.send. A person links one under a service
// name (slack, discord); its messages come in through the same door every
// transport's do (ingestInbound), so their channels bound to that service
// see them, and their agents' replies go back out through the connector.
//
// One link per service per person: the outbox is kept by service and
// owner, and two transports draining it would race for the same replies.
// The connector is called as its owner, so a connection limited to named
// users (its Access) is reached only by one of them; its key is the
// connection's own, set up by an administrator.
//
// The first fetch of each chat only marks what is already there as seen,
// so history does not wake anyone; after that each poll passes on what is
// new. A service that gives a cursor is asked from it; one that gives none
// is asked for its latest every time, and what was seen is passed over.
//
// A link a webhook feeds (core/webhooks, channel:<service>) is pushed its
// messages: it fetches nothing, and sends its agents' replies every few
// seconds instead.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/messaging"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/teamsbots"
	"github.com/cmcoffee/gohort/core/webhooks"
)

const (
	linksTable      = "bridges_links"       // owner \x00 service -> serviceLink
	linkCursorTable = "bridges_link_cursor" // owner \x00 service \x00 chat -> cursor ("" = seeded, no cursor)

	linkDefaultSecs = 30
	linkMinSecs     = 15
	// linkMaxChats bounds the chats one link polls, each a call a tick.
	linkMaxChats = 20
	// linkPushedSecs is how often a link a webhook feeds sends replies.
	linkPushedSecs = 3
)

var serviceIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// serviceLink is one linked service.
type serviceLink struct {
	Owner        string    `json:"owner"`
	Service      string    `json:"service"`
	Connector    string    `json:"connector"`
	Chats        []string  `json:"chats,omitempty"`
	IntervalSecs int       `json:"interval_secs,omitempty"`
	Created      time.Time `json:"created"`
}

func linkKey(owner, svc string) string { return owner + "\x00" + svc }

func (l serviceLink) key() string { return linkKey(l.Owner, l.Service) }

func (l serviceLink) every() time.Duration {
	if l.IntervalSecs < linkMinSecs {
		return linkDefaultSecs * time.Second
	}
	return time.Duration(l.IntervalSecs) * time.Second
}

// chats are what each poll fetches: the ones named, else the whole service.
func (l serviceLink) chats() []string {
	if len(l.Chats) == 0 {
		return []string{""}
	}
	return l.Chats
}

// session is who the connector is called as: the link's owner.
func (l serviceLink) session() *ToolSession { return &ToolSession{Username: l.Owner} }

// linkState is how a link's last poll went, for its row.
type linkState struct {
	At      time.Time
	Outcome string
	OK      bool
}

var (
	linkMu      sync.Mutex
	linkCancel  = map[string]context.CancelFunc{}
	linkLastRun = map[string]linkState{}
)

func noteLink(key string, ok bool, format string, args ...any) {
	linkMu.Lock()
	linkLastRun[key] = linkState{At: time.Now(), OK: ok, Outcome: fmt.Sprintf(format, args...)}
	linkMu.Unlock()
}

func lastLink(key string) (linkState, bool) {
	linkMu.Lock()
	defer linkMu.Unlock()
	s, ok := linkLastRun[key]
	return s, ok
}

// allLinks is every link, by owner then service.
func (T *Bridges) allLinks() []serviceLink {
	var out []serviceLink
	for _, k := range T.DB.Keys(linksTable) {
		var l serviceLink
		if T.DB.Get(linksTable, k, &l) {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

// linksOf is owner's links.
func (T *Bridges) linksOf(owner string) []serviceLink {
	var out []serviceLink
	for _, l := range T.allLinks() {
		if l.Owner == owner {
			out = append(out, l)
		}
	}
	return out
}

// linkFor is owner's link for service svc.
func (T *Bridges) linkFor(owner, svc string) (serviceLink, bool) {
	var l serviceLink
	return l, T.DB.Get(linksTable, linkKey(owner, svc), &l)
}

// linkable reports why connector name cannot be linked by owner, or nil:
// it must give both halves, and be one owner may reach.
func linkable(owner, name string) error {
	c, ok := GetConnector(RootDB, strings.TrimSpace(name))
	if !ok || c.Kind != service.Kind {
		return fmt.Errorf("there is no connector %q here", name)
	}
	if !service.Provides(c, "message.fetch") || !service.Provides(c, "message.send") {
		return fmt.Errorf("%s does not both fetch and send messages", c.Title())
	}
	return (&ToolSession{Username: owner}).ConnectionBarred(c.Name)
}

// servedElsewhere is what already carries service svc for owner, or "": a
// bridge key, a Teams bot, or a connector of another kind.
func (T *Bridges) servedElsewhere(owner, svc string) string {
	if b, ok := teamsbots.Load(owner, svc); ok {
		return "the Teams bot " + b.Name
	}
	for _, k := range T.listBridgeKeys(owner) {
		if k.Service == svc {
			return "the bridge " + k.Name
		}
	}
	for _, c := range ListConnectors(RootDB) {
		if c.Owner != owner {
			continue
		}
		switch c.Kind {
		case RestMessagingConnectorKind, MessagingBridgeConnectorKind:
			if connectorService(c) == svc {
				return "the connector " + c.Title()
			}
		}
	}
	return ""
}

// connectorService is the service a messaging connector of another kind
// carries, or "".
func connectorService(c Connector) string {
	var spec struct {
		Service string `json:"service"`
	}
	if json.Unmarshal(c.Spec, &spec) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(spec.Service))
}

// saveLink adds or replaces owner's link, and starts it.
func (T *Bridges) saveLink(l serviceLink) error {
	l, err := T.storeLink(l)
	if err != nil {
		return err
	}
	T.registerLinkService(l)
	T.startLink(l)
	return nil
}

// storeLink checks l and keeps it, as it is to run.
func (T *Bridges) storeLink(l serviceLink) (serviceLink, error) {
	l.Service = strings.ToLower(strings.TrimSpace(l.Service))
	l.Connector = strings.TrimSpace(l.Connector)
	if !serviceIDRE.MatchString(l.Service) {
		return l, fmt.Errorf("a service name is lowercase letters, digits, dashes or underscores, such as slack")
	}
	if l.Service == "imessage" {
		return l, fmt.Errorf("iMessage comes through the desktop relay, not a connector")
	}
	if by := T.servedElsewhere(l.Owner, l.Service); by != "" {
		return l, fmt.Errorf("%s already carries %s for you: one transport a service, or the two would race for its replies", by, l.Service)
	}
	if err := linkable(l.Owner, l.Connector); err != nil {
		return l, err
	}
	var chats []string
	for _, ch := range l.Chats {
		if ch = strings.TrimSpace(ch); ch != "" && !contains(chats, ch) {
			chats = append(chats, ch)
		}
	}
	if len(chats) > linkMaxChats {
		return l, fmt.Errorf("a link polls at most %d chats", linkMaxChats)
	}
	l.Chats = chats
	if l.IntervalSecs != 0 && l.IntervalSecs < linkMinSecs {
		return l, fmt.Errorf("poll at most every %d seconds", linkMinSecs)
	}
	if prev, ok := T.linkFor(l.Owner, l.Service); ok {
		l.Created = prev.Created
	} else {
		l.Created = time.Now()
	}
	T.DB.Set(linksTable, l.key(), l)
	return l, nil
}

// removeLink stops and deletes owner's link for svc, with its cursors.
func (T *Bridges) removeLink(owner, svc string) error {
	l, ok := T.linkFor(owner, svc)
	if !ok {
		return fmt.Errorf("you have no link for %s", svc)
	}
	T.stopLink(l.key())
	T.DB.Unset(linksTable, l.key())
	prefix := l.key() + "\x00"
	for _, k := range T.DB.Keys(linkCursorTable) {
		if strings.HasPrefix(k, prefix) {
			T.DB.Unset(linkCursorTable, k)
		}
	}
	return nil
}

// registerLinkService names a service no transport has named before, so
// its channels show it: as the connector is shown, in plain text.
func (T *Bridges) registerLinkService(l serviceLink) {
	if c, ok := GetConnector(RootDB, l.Connector); ok {
		messaging.RegisterBridgeService(l.Service, messaging.BridgeService{DisplayName: c.Title()})
	}
}

// startLinks starts every link, at start.
func (T *Bridges) startLinks() {
	if T.DB == nil {
		return
	}
	for _, l := range T.allLinks() {
		T.registerLinkService(l)
		T.startLink(l)
	}
}

// startLink (re)starts l's poll loop.
func (T *Bridges) startLink(l serviceLink) {
	ctx, cancel := context.WithCancel(context.Background())
	linkMu.Lock()
	if prev, ok := linkCancel[l.key()]; ok {
		prev()
	}
	linkCancel[l.key()] = cancel
	linkMu.Unlock()
	go T.linkLoop(ctx, l)
}

func (T *Bridges) stopLink(key string) {
	linkMu.Lock()
	if cancel, ok := linkCancel[key]; ok {
		cancel()
		delete(linkCancel, key)
	}
	delete(linkLastRun, key)
	linkMu.Unlock()
}

func (T *Bridges) linkLoop(ctx context.Context, l serviceLink) {
	for {
		T.pollLink(ctx, l)
		wait := l.every()
		if pushedBy(l) != "" {
			wait = linkPushedSecs * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// pushedBy names the webhook feeding l, or "".
func pushedBy(l serviceLink) string {
	if hs := webhooks.FeedsChannel(l.Owner, l.Service); len(hs) > 0 {
		return hs[0].Label
	}
	return ""
}

// wireWebhooks lets a person's webhooks feed their linked services.
func (T *Bridges) wireWebhooks() {
	webhooks.LinkedServices = func(owner string) []string {
		var out []string
		for _, l := range T.linksOf(owner) {
			out = append(out, l.Service)
		}
		for _, b := range teamsbots.List(owner) {
			out = append(out, b.Service)
		}
		return out
	}
	webhooks.RegisterChannelCheck(teamsbots.VerifyName, T.botframeworkCheck)
	webhooks.ToChannel = func(owner, svc string, m webhooks.ChannelMessage) {
		T.ingestInbound(BridgeKey{Owner: owner, Service: svc, Enabled: true, Name: "webhook"},
			hookRequest{ChatID: m.Chat, Handle: m.From, Text: m.Text, MsgID: m.ID})
	}
}

// cursorKey is where the cursor of chat in l is kept.
func (l serviceLink) cursorKey(chat string) string { return l.key() + "\x00" + chat }

// pollLink fetches each of l's chats once, passes on what is new, then
// sends what its agents wrote back.
func (T *Bridges) pollLink(ctx context.Context, l serviceLink) {
	if by := pushedBy(l); by != "" {
		sent, err := T.sendLinked(ctx, l)
		if err != nil {
			noteLink(l.key(), false, "messages come by the webhook %s; could not send a reply, kept for the next try: %v", by, err)
		} else if sent > 0 {
			noteLink(l.key(), true, "messages come by the webhook %s; %d sent", by, sent)
		} else if _, ok := lastLink(l.key()); !ok {
			noteLink(l.key(), true, "messages come by the webhook %s", by)
		}
		return
	}
	key := BridgeKey{Owner: l.Owner, Service: l.Service, Enabled: true, Name: l.Connector}
	passed, seeded, chatless := 0, 0, 0
	for _, chat := range l.chats() {
		if ctx.Err() != nil {
			return
		}
		var cursor string
		seen := T.DB.Get(linkCursorTable, l.cursorKey(chat), &cursor)
		in := map[string]any{}
		if cursor != "" {
			in["since"] = cursor
		}
		if chat != "" {
			in["chat"] = chat
		}
		res, err := service.Call(ctx, l.session(), l.Connector, "message.fetch", in)
		if err != nil {
			noteLink(l.key(), false, "could not fetch%s: %v", inChat(chat), err)
			Warn("[bridges] link %s (%s) for %s: %v", l.Service, l.Connector, l.Owner, err)
			return
		}
		msgs, err := service.FetchedMessages(res, chat)
		if err != nil {
			noteLink(l.key(), false, "%v", err)
			return
		}
		for _, m := range msgs {
			if strings.TrimSpace(m.ID) == "" {
				continue
			}
			if strings.TrimSpace(m.Chat) == "" {
				chatless++
				continue
			}
			if !seen {
				// The first fetch: what is there already is history.
				T.seenMessage(l.Owner, m.Chat, m.ID)
				seeded++
				continue
			}
			// A service asked for its latest (one with no cursor) gives
			// again what it gave: passed over, and not counted as new.
			if T.alreadySeen(l.Owner, m.Chat, m.ID) {
				continue
			}
			T.ingestInbound(key, hookRequest{ChatID: m.Chat, Handle: m.From, DisplayName: m.FromName,
				Text: m.Text, MsgID: m.ID, Timestamp: sentAt(m.At)})
			passed++
		}
		if next := strings.TrimSpace(res["cursor"].Text); next != "" {
			cursor = next
		}
		T.DB.Set(linkCursorTable, l.cursorKey(chat), cursor)
	}
	sent, err := T.sendLinked(ctx, l)
	switch {
	case chatless > 0:
		noteLink(l.key(), false, "%d message(s) did not say their chat: name the chats to read", chatless)
	case err != nil:
		noteLink(l.key(), false, "fetched %d new; could not send a reply, kept for the next poll: %v", passed, err)
	case seeded > 0 && passed == 0:
		noteLink(l.key(), true, "connected: %d earlier message(s) passed over", seeded)
	default:
		noteLink(l.key(), true, "%d new, %d sent", passed, sent)
	}
}

// alreadySeen reports whether message id in chat was passed on before,
// recording nothing (seenMessage records).
func (T *Bridges) alreadySeen(owner, chat, id string) bool {
	var at string
	return T.DB.Get(seenMsgTable, T.convoKey(owner, chat)+":"+id, &at)
}

func inChat(chat string) string {
	if chat == "" {
		return ""
	}
	return " " + chat
}

// sentAt is a message's time as the inbound door reads it (RFC3339): one
// given as seconds since 1970 (Slack's ts) is turned into that, one that
// already is passes as it is, and anything else is left out.
func sentAt(at string) string {
	at = strings.TrimSpace(at)
	if at == "" {
		return ""
	}
	if f, err := strconv.ParseFloat(at, 64); err == nil && f > 1e9 {
		return time.Unix(int64(f), 0).UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

// sendLinked sends what l's agents wrote back, oldest first, through the
// connector; on a failure it and everything after it are kept for the next
// poll.
func (T *Bridges) sendLinked(ctx context.Context, l serviceLink) (int, error) {
	items := T.drainOutbox(l.Service, l.Owner)
	sent := 0
	for i, it := range items {
		if strings.TrimSpace(it.Text) == "" {
			continue
		}
		_, err := service.Call(ctx, l.session(), l.Connector, "message.send", map[string]any{"text": it.Text, "to": it.ChatID})
		if err != nil || ctx.Err() != nil {
			for _, r := range items[i:] {
				T.enqueueOutbox(r)
			}
			if err == nil {
				err = ctx.Err()
			}
			return sent, err
		}
		sent++
	}
	return sent, nil
}

// --- the list and the form ---------------------------------------------------

// linkRow is one link as its row shows it.
type linkRow struct {
	Service   string `json:"service"`
	Connector string `json:"connector"`
	Chats     string `json:"chats"`
	Every     string `json:"every"`
	Last      string `json:"last"`
	Trouble   bool   `json:"trouble"`
}

// linkForm is the link form, as posted.
type linkForm struct {
	Service      string `json:"service"`
	Connector    string `json:"connector"`
	Chats        string `json:"chats"`
	IntervalSecs int    `json:"interval_secs"`
}

// handleLinks is the person's linked services:
//
//	GET                       the list
//	GET ?options=connectors   the connectors they may link
func (T *Bridges) handleLinks(c *jsonapi.Call) (any, error) {
	if c.R.URL.Query().Get("options") == "connectors" {
		opts := []map[string]string{}
		for _, conn := range ListConnectors(RootDB) {
			if linkable(c.User, conn.Name) == nil {
				opts = append(opts, map[string]string{"value": conn.Name, "label": conn.Title()})
			}
		}
		return opts, nil
	}
	rows := []linkRow{}
	for _, l := range T.linksOf(c.User) {
		row := linkRow{Service: l.Service, Connector: l.Connector, Chats: strings.Join(l.Chats, ", "),
			Every: l.every().String(), Last: "not polled yet"}
		if conn, ok := GetConnector(RootDB, l.Connector); ok {
			row.Connector = conn.Title()
		}
		if row.Chats == "" {
			row.Chats = "all it gives"
		}
		if st, ok := lastLink(l.key()); ok {
			row.Last, row.Trouble = st.At.Format("15:04:05")+": "+st.Outcome, !st.OK
		}
		if err := linkable(l.Owner, l.Connector); err != nil {
			row.Last, row.Trouble = "not polled: "+err.Error(), true
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// handleLinkSave adds or replaces one.
func (T *Bridges) handleLinkSave(c *jsonapi.Call, f linkForm) (any, error) {
	var chats []string
	for _, ch := range strings.FieldsFunc(f.Chats, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' }) {
		chats = append(chats, ch)
	}
	l := serviceLink{Owner: c.User, Service: f.Service, Connector: f.Connector, Chats: chats, IntervalSecs: f.IntervalSecs}
	if err := T.saveLink(l); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]string{"message": fmt.Sprintf("Linked %s. Its first poll passes over what is there already; bind a channel to %s to have an agent answer.", l.Service, l.Service)}, nil
}

// handleLinkDelete removes the ?service= link.
func (T *Bridges) handleLinkDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	if err := T.removeLink(c.User, strings.TrimSpace(c.R.URL.Query().Get("service"))); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(204), nil
}
