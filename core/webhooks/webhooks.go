// Package webhooks is a person's webhooks: an address on this instance a
// service calls (/hook/<name>), the mirror of a connector, which gohort
// calls. A webhook checks who is calling, reads the event out of what was
// posted, and feeds it on: to a watch (one of its owner's webhook
// monitors), which wakes that monitor's agent with the event, or to a
// channel, as a message on one of its owner's linked services (Bridges >
// Linked services), which the channel bound to that service answers
// through the link's connector.
//
// A webhook is its owner's, as a monitor is: it feeds only the owner's own
// watches and linked services. Its address is unique on the instance. One that checks nothing
// gets a long random name, since its address is then its only secret.
//
// A watch's own token URL (/orchestrate/api/operator/event/<token>) keeps
// working as it did; a webhook is the door for a service that posts its own
// payload and signs it. Both doors spend the watch's one budget
// (core.EventMonitorFires).
package webhooks

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	table       = "webhooks"        // name -> Webhook
	secretTable = "webhook_secrets" // name -> the shared token or signing secret (encrypted)
	callsTable  = "webhook_calls"   // name -> []Call, the last few

	// Checks: how a webhook tells its caller is the service it is for.
	VerifyNone  = "none"  // nothing: the address is the only secret
	VerifyToken = "token" // a shared token in a named header
	VerifyHMAC  = "hmac"  // a signature over the body in a named header
	// VerifySlack is Slack's request signing: an HMAC SHA-256 over
	// "v0:<timestamp>:<body>", the timestamp within five minutes; it
	// answers Slack's URL check too.
	VerifySlack = "slack"

	// FeedWatch is the prefix of what a webhook feeds: watch:<monitor>.
	FeedWatch = "watch:"
	// FeedChannel is the prefix of feeding a channel: channel:<service>,
	// one of the owner's linked services.
	FeedChannel = "channel:"
	// ReachDirect is reached at this instance's own address; peer:<relay
	// connection> through a peer's relay.
	ReachDirect = "direct"
	ReachPeer   = "peer:"

	// keptCalls is how many calls a webhook's row shows.
	keptCalls = 20
)

// Webhook is one webhook.
type Webhook struct {
	Name  string `json:"name"`  // its address: /hook/<name>
	Label string `json:"label"` // what its owner calls it
	Owner string `json:"owner"`

	Verify    string `json:"verify"`              // none | token | hmac
	Header    string `json:"header,omitempty"`    // the header the token or signature comes in
	Algorithm string `json:"algorithm,omitempty"` // hmac: sha256 (default) or sha1
	Prefix    string `json:"prefix,omitempty"`    // hmac: before the signature, as GitHub's "sha256="

	Map   Map      `json:"map"`             // how the event reads
	Kinds []string `json:"kinds,omitempty"` // the kinds it passes on; none = every one

	Feeds string `json:"feeds"` // watch:<monitor>
	Reach string `json:"reach"` // direct | peer:<relay connection>

	Created time.Time `json:"created"`
}

// Map is where each part of an event is read from: a dotted path into the
// posted JSON (arrays by index), or header:<Name>. Text blank passes the
// body on as it came.
type Map struct {
	Text string `json:"text,omitempty"` // what the agent reads
	Kind string `json:"kind,omitempty"` // what sort of event (push, payment.failed)
	Chat string `json:"chat,omitempty"` // the conversation, for a message fed to a channel
	ID   string `json:"id,omitempty"`   // the event's own id, to drop a provider's retry
	From string `json:"from,omitempty"` // who or what it is from
}

// Call is one request a webhook answered, for its row.
type Call struct {
	At      time.Time `json:"at"`
	Outcome string    `json:"outcome"` // what happened, as its row says it
	Kind    string    `json:"kind,omitempty"`
	OK      bool      `json:"ok"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)

func db() Database { return RootDB }

// Get is the webhook named name.
func Get(name string) (Webhook, bool) {
	var h Webhook
	if db() == nil || !nameRE.MatchString(name) {
		return h, false
	}
	return h, db().Get(table, name, &h)
}

// List is owner's webhooks, by label.
func List(owner string) []Webhook {
	var out []Webhook
	if db() == nil {
		return out
	}
	for _, k := range db().Keys(table) {
		var h Webhook
		if db().Get(table, k, &h) && h.Owner == owner {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out
}

// Owned is owner's webhook named name.
func Owned(owner, name string) (Webhook, bool) {
	h, ok := Get(name)
	return h, ok && h.Owner == owner
}

// Create adds h for h.Owner under a name made from its label, with secret
// when it checks one, and gives it.
func Create(h Webhook, secret string) (Webhook, error) {
	if db() == nil {
		return h, fmt.Errorf("the store is not ready")
	}
	h.Verify = strings.TrimSpace(h.Verify)
	if err := h.check(); err != nil {
		return h, err
	}
	secret = strings.TrimSpace(secret)
	if h.Verify != VerifyNone && secret == "" {
		return h, fmt.Errorf("give it the secret the service sends with")
	}
	h.Name = freeName(slug(h.Label), h.Verify == VerifyNone)
	h.Created = time.Now()
	db().Set(table, h.Name, h)
	if h.Verify != VerifyNone {
		db().CryptSet(secretTable, h.Name, secret)
	}
	return h, nil
}

// Update saves what owner may change of their webhook h.Name: everything
// but its name, owner and how it checks (to check another way, add
// another: the address and secret go with the check).
func Update(owner string, h Webhook) (Webhook, error) {
	cur, ok := Owned(owner, h.Name)
	if !ok {
		return h, fmt.Errorf("you have no webhook %q", h.Name)
	}
	h.Owner, h.Verify, h.Created = cur.Owner, cur.Verify, cur.Created
	if err := h.check(); err != nil {
		return h, err
	}
	db().Set(table, h.Name, h)
	return h, nil
}

// NewSecret gives owner's webhook name secret in place of the one it had.
func NewSecret(owner, name, secret string) error {
	h, ok := Owned(owner, name)
	if !ok {
		return fmt.Errorf("you have no webhook %q", name)
	}
	if h.Verify == VerifyNone {
		return fmt.Errorf("%s checks nothing, so it has no secret", h.Label)
	}
	if secret = strings.TrimSpace(secret); secret == "" {
		return fmt.Errorf("give it the new secret")
	}
	db().CryptSet(secretTable, name, secret)
	return nil
}

// NewSecretValue is a fresh random secret, for a form to start with: its
// owner copies it to the service before saving it.
func NewSecretValue() string { return randomHex(24) }

// Remove deletes owner's webhook name, its secret and its calls.
func Remove(owner, name string) error {
	if _, ok := Owned(owner, name); !ok {
		return fmt.Errorf("you have no webhook %q", name)
	}
	db().Unset(table, name)
	db().Unset(secretTable, name)
	db().Unset(callsTable, name)
	return nil
}

// Calls are the last calls webhook name answered, newest first.
func Calls(name string) []Call {
	var out []Call
	if db() != nil {
		db().Get(callsTable, name, &out)
	}
	return out
}

// note keeps one call on webhook name's row.
func note(name string, c Call) {
	if db() == nil {
		return
	}
	calls := append([]Call{c}, Calls(name)...)
	if len(calls) > keptCalls {
		calls = calls[:keptCalls]
	}
	db().Set(callsTable, name, calls)
}

// secret is webhook name's secret.
func secret(name string) string {
	var s string
	if db() != nil {
		db().Get(secretTable, name, &s)
	}
	return s
}

// check is what is wrong with h as its owner set it up, or nil.
func (h *Webhook) check() error {
	h.Label = strings.TrimSpace(h.Label)
	h.Header = strings.TrimSpace(h.Header)
	h.Algorithm = strings.ToLower(strings.TrimSpace(h.Algorithm))
	h.Feeds, h.Reach = strings.TrimSpace(h.Feeds), strings.TrimSpace(h.Reach)
	if h.Label == "" {
		return fmt.Errorf("give it a name")
	}
	switch h.Verify {
	case VerifyNone:
		h.Header, h.Algorithm, h.Prefix = "", "", ""
	case VerifyToken:
		h.Algorithm, h.Prefix = "", ""
		if h.Header == "" {
			return fmt.Errorf("say which header the token comes in")
		}
	case VerifySlack:
		h.Header, h.Algorithm, h.Prefix = "", "", ""
	case VerifyHMAC:
		if h.Header == "" {
			return fmt.Errorf("say which header the signature comes in")
		}
		if h.Algorithm == "" {
			h.Algorithm = "sha256"
		}
		if h.Algorithm != "sha256" && h.Algorithm != "sha1" {
			return fmt.Errorf("the signature is sha256 or sha1")
		}
	default:
		return fmt.Errorf("it checks a token, a signature, or nothing")
	}
	if svc, ok := strings.CutPrefix(h.Feeds, FeedChannel); ok {
		svc = strings.TrimSpace(svc)
		if !h.linked(svc) {
			return fmt.Errorf("%s is not one of your linked services", svc)
		}
		if strings.TrimSpace(h.Map.Chat) == "" {
			return fmt.Errorf("to feed a channel, say where a message's chat is read")
		}
	} else {
		monitor, ok := strings.CutPrefix(h.Feeds, FeedWatch)
		if !ok || strings.TrimSpace(monitor) == "" {
			return fmt.Errorf("choose the watch it wakes")
		}
		if m, ok := GetEventMonitor(RootDB, h.Owner, strings.TrimSpace(monitor)); !ok || m.Kind != EventKindWebhook {
			return fmt.Errorf("%s is not one of your watches woken by a post", monitor)
		}
	}
	if h.Reach == "" {
		h.Reach = ReachDirect
	}
	if relay, ok := strings.CutPrefix(h.Reach, ReachPeer); ok {
		if c, found := GetConnector(RootDB, strings.TrimSpace(relay)); !found || c.Kind != PeerRelayConnectorKind {
			return fmt.Errorf("%s is not a peer relay here", relay)
		}
	} else if h.Reach != ReachDirect {
		return fmt.Errorf("it is reached here directly, or through a peer's relay")
	}
	var kinds []string
	for _, k := range h.Kinds {
		if k = strings.TrimSpace(k); k != "" {
			kinds = append(kinds, k)
		}
	}
	h.Kinds = kinds
	if len(h.Kinds) > 0 && strings.TrimSpace(h.Map.Kind) == "" {
		return fmt.Errorf("to pass on only some kinds of event, say where its kind is read")
	}
	for _, p := range []string{h.Map.Text, h.Map.Kind, h.Map.ID, h.Map.From, h.Map.Chat} {
		if p = strings.TrimSpace(p); strings.HasPrefix(p, "header:") && strings.TrimSpace(strings.TrimPrefix(p, "header:")) == "" {
			return fmt.Errorf("header: needs the header's name")
		}
	}
	return nil
}

// Watch is the monitor h feeds, or "".
func (h Webhook) Watch() string {
	if m, ok := strings.CutPrefix(h.Feeds, FeedWatch); ok {
		return strings.TrimSpace(m)
	}
	return ""
}

// Channel is the linked service h feeds, or "".
func (h Webhook) Channel() string {
	if s, ok := strings.CutPrefix(h.Feeds, FeedChannel); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// LinkedServices, set by the app that keeps linked services (bridges),
// is owner's linked services by name: what a webhook may feed as a
// channel. Nil, and none may be fed.
var LinkedServices func(owner string) []string

// ToChannel, set with LinkedServices, passes a message on to owner's
// linked service svc, as the service's own fetch would have.
var ToChannel func(owner, svc string, m ChannelMessage)

// ChannelMessage is a message a webhook passes to a channel.
type ChannelMessage struct {
	ID, Chat, From, Text string
}

// linked reports whether svc is one of h's owner's linked services.
func (h Webhook) linked(svc string) bool {
	if LinkedServices == nil || svc == "" {
		return false
	}
	for _, s := range LinkedServices(h.Owner) {
		if s == svc {
			return true
		}
	}
	return false
}

// FeedsChannel are owner's webhooks feeding linked service svc.
func FeedsChannel(owner, svc string) []Webhook {
	var out []Webhook
	for _, h := range List(owner) {
		if h.Channel() == svc {
			out = append(out, h)
		}
	}
	return out
}

// Address is where h is reached, from base (this instance's own address,
// or a peer relay's), and whether it can be said: a relay not connected
// has none yet.
func (h Webhook) Address(base string) (string, bool) {
	if relay, ok := strings.CutPrefix(h.Reach, ReachPeer); ok {
		public, up := PeerRelayAddress(strings.TrimSpace(relay))
		if !up {
			return "", false
		}
		base = public
	}
	return strings.TrimRight(base, "/") + Prefix + h.Name, true
}

// Prefix is where every webhook's address starts.
const Prefix = "/hook/"

// slug is label as a name: lowercase letters and digits, the rest single
// dashes.
func slug(label string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(label) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		return "webhook"
	}
	return s
}

// freeName is base, numbered past every name taken; with random, base
// followed by a long random part, since the address is then the only
// secret.
func freeName(base string, random bool) string {
	if random {
		for {
			name := base + "-" + randomHex(16)
			if _, taken := Get(name); !taken {
				return name
			}
		}
	}
	name := base
	for n := 2; ; n++ {
		if _, taken := Get(name); !taken {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, n)
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
