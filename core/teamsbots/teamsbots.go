// Package teamsbots is a person's own Microsoft Teams bot: one they register
// in Azure (its app id and secret) and add here, which answers in Teams with
// their agents. It is a channel type of its own: Bot Framework is one
// protocol, with a token Microsoft signs on the way in and a reply address
// learned from each conversation on the way out, which no connector spec
// describes.
//
// A bot is reached through a webhook its owner has (core/webhooks), checked
// "botframework" and passing to the channel of the bot's service ("teams"),
// made and removed with the bot. The bridges app reads its activities and
// sends its replies; this package keeps the record, its secret, and what
// both sides need from it: the verifier, a reply token, and the Teams app
// package to upload.
//
// Whether people may add one is the administrator's policy
// (core/governance, TeamsBots): under Needs approval a new one waits, off,
// until an administrator approves it (Admin > Integrations).
package teamsbots

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jwks"
)

const (
	table       = "teams_bots"        // owner \x00 service -> Bot
	secretTable = "teams_bot_secrets" // owner \x00 service -> the app's client secret (encrypted)

	// DefaultService is the service a bot's channels bind to.
	DefaultService = "teams"
	// DefaultServiceHost bounds where replies go: the public cloud's host,
	// every regional serviceUrl (/amer/, /emea/, /apac/) sits under it.
	DefaultServiceHost = "https://smba.trafficmanager.net"
	// VerifyName is the webhook check a bot's webhook uses.
	VerifyName = "botframework"
)

// ConversationTypes are the Teams surfaces a bot may answer in.
var ConversationTypes = []string{"personal", "groupChat", "channel"}

var (
	guidRE    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	serviceRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

// Bot is one person's Teams bot.
type Bot struct {
	Owner   string `json:"owner"`
	Service string `json:"service"` // what its channels bind to
	Name    string `json:"name"`    // what Teams shows it as
	// AppID is the Entra application (client) id of the Azure Bot: the
	// audience of every token Microsoft signs for it, and not a secret.
	AppID string `json:"app_id"`
	// TenantID is the Entra tenant a single-tenant bot belongs to (Azure
	// makes new bots single-tenant); blank for a multi-tenant one. It names
	// where reply tokens come from, and an activity from another tenant is
	// refused.
	TenantID      string   `json:"tenant_id,omitempty"`
	ServiceHost   string   `json:"service_host,omitempty"`
	ReplyInThread bool     `json:"reply_in_thread,omitempty"`
	Accept        []string `json:"accept,omitempty"` // conversation types answered; none = every one
	// Webhook is the name of the webhook it is reached through.
	Webhook string `json:"webhook,omitempty"`

	AwaitingApproval bool      `json:"awaiting_approval,omitempty"`
	AdminDisabled    bool      `json:"admin_disabled,omitempty"`
	Created          time.Time `json:"created"`
}

func key(owner, svc string) string { return owner + "\x00" + svc }

// Key is what tells this bot from every other: its owner and service.
func (b Bot) Key() string { return key(b.Owner, b.Service) }

// Usable reports whether the bot answers: approved and not switched off.
func (b Bot) Usable() bool { return !b.AwaitingApproval && !b.AdminDisabled }

// Accepts reports whether the bot answers in a conversation of type ct.
func (b Bot) Accepts(ct string) bool {
	if len(b.Accept) == 0 {
		return true
	}
	for _, a := range b.Accept {
		if strings.EqualFold(a, strings.TrimSpace(ct)) {
			return true
		}
	}
	return false
}

func db() Database { return RootDB }

// Load is owner's bot for service svc.
func Load(owner, svc string) (Bot, bool) {
	var b Bot
	if db() == nil {
		return b, false
	}
	return b, db().Get(table, key(owner, svc), &b)
}

// All is every bot, by owner then service.
func All() []Bot {
	var out []Bot
	if db() == nil {
		return out
	}
	for _, k := range db().Keys(table) {
		var b Bot
		if db().Get(table, k, &b) {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// List is owner's bots.
func List(owner string) []Bot {
	var out []Bot
	for _, b := range All() {
		if b.Owner == owner {
			out = append(out, b)
		}
	}
	return out
}

// Secret is the app's client secret of owner's bot for svc.
func Secret(owner, svc string) string {
	var s string
	if db() != nil {
		db().Get(secretTable, key(owner, svc), &s)
	}
	return s
}

// check settles b's fields and says what is wrong with them, or nil.
func (b *Bot) check() error {
	b.Service = strings.ToLower(strings.TrimSpace(b.Service))
	if b.Service == "" {
		b.Service = DefaultService
	}
	b.Name = strings.TrimSpace(b.Name)
	b.AppID = strings.ToLower(strings.TrimSpace(b.AppID))
	b.TenantID = strings.ToLower(strings.TrimSpace(b.TenantID))
	b.ServiceHost = strings.TrimRight(strings.TrimSpace(b.ServiceHost), "/")
	if b.ServiceHost == "" {
		b.ServiceHost = DefaultServiceHost
	}
	switch {
	case strings.TrimSpace(b.Owner) == "":
		return fmt.Errorf("a bot is someone's")
	case !serviceRE.MatchString(b.Service):
		return fmt.Errorf("a service name is lowercase letters, digits, dashes or underscores, such as teams")
	case b.Name == "" || len(b.Name) > 30:
		return fmt.Errorf("give it the name Teams shows, up to 30 characters")
	case !guidRE.MatchString(b.AppID):
		return fmt.Errorf("the app id is the Azure Bot's Microsoft App ID, a GUID such as 00000000-0000-0000-0000-000000000000")
	case b.TenantID != "" && !guidRE.MatchString(b.TenantID):
		return fmt.Errorf("the tenant id is a GUID, or blank for a multi-tenant bot")
	case !strings.HasPrefix(b.ServiceHost, "https://"):
		return fmt.Errorf("replies go to an https address")
	}
	var accept []string
	for _, a := range b.Accept {
		a = strings.TrimSpace(a)
		ok := false
		for _, ct := range ConversationTypes {
			if strings.EqualFold(a, ct) {
				a, ok = ct, true
			}
		}
		if a == "" {
			continue
		}
		if !ok {
			return fmt.Errorf("it answers in personal, groupChat or channel conversations, not %q", a)
		}
		accept = append(accept, a)
	}
	b.Accept = accept
	return nil
}

// Save adds or updates b, its secret when one is given (an update keeps the
// one it has otherwise), held for an administrator's approval when hold. An
// update keeps what an administrator decided: approval, and a switch-off.
func Save(b Bot, secret string, hold bool) (Bot, error) {
	if db() == nil {
		return b, fmt.Errorf("the store is not ready")
	}
	if err := b.check(); err != nil {
		return b, err
	}
	secret = strings.TrimSpace(secret)
	if prev, ok := Load(b.Owner, b.Service); ok {
		b.Created, b.AwaitingApproval, b.AdminDisabled = prev.Created, prev.AwaitingApproval, prev.AdminDisabled
		if b.Webhook == "" {
			b.Webhook = prev.Webhook
		}
		if prev.AppID != b.AppID && secret == "" {
			return b, fmt.Errorf("a new app id needs its own client secret")
		}
	} else {
		if secret == "" {
			return b, fmt.Errorf("give it the app's client secret")
		}
		b.Created, b.AwaitingApproval = time.Now(), hold
	}
	db().Set(table, b.Key(), b)
	if secret != "" {
		db().CryptSet(secretTable, b.Key(), secret)
		forgetToken(b.Key())
	}
	return b, nil
}

// SetWebhook records the webhook owner's bot for svc is reached through.
func SetWebhook(owner, svc, webhook string) error {
	b, ok := Load(owner, svc)
	if !ok {
		return fmt.Errorf("there is no bot for %s", svc)
	}
	b.Webhook = webhook
	db().Set(table, b.Key(), b)
	return nil
}

// AdminSetDisabled is an administrator approving (enabling) or switching
// off owner's bot for svc.
func AdminSetDisabled(owner, svc string, disabled bool) error {
	b, ok := Load(owner, svc)
	if !ok {
		return fmt.Errorf("%s has no bot for %s", owner, svc)
	}
	b.AdminDisabled = disabled
	if !disabled {
		b.AwaitingApproval = false
	}
	db().Set(table, b.Key(), b)
	return nil
}

// Remove deletes owner's bot for svc and its secret.
func Remove(owner, svc string) error {
	b, ok := Load(owner, svc)
	if !ok {
		return fmt.Errorf("there is no bot for %s", svc)
	}
	db().Unset(table, b.Key())
	db().Unset(secretTable, b.Key())
	forgetToken(b.Key())
	return nil
}

// --- inbound tokens --------------------------------------------------------

const (
	issuer      = "https://api.botframework.com"
	metadataURL = "https://login.botframework.com/v1/.well-known/openidconfiguration"
)

// verifier is shared by every bot: one issuer, one key set, one cache.
var verifier = &jwks.Verifier{MetadataURL: metadataURL, Issuer: issuer, Name: "bot framework"}

// Verifier checks the tokens Microsoft signs on every activity: Verify with
// the bot's app id as the audience, which is what makes a token this bot's
// rather than any bot's.
func Verifier() *jwks.Verifier { return verifier }

// --- reply tokens ------------------------------------------------------------

type cachedToken struct {
	value string
	until time.Time
}

var tokens = struct {
	sync.Mutex
	by map[string]cachedToken
}{by: map[string]cachedToken{}}

func forgetToken(k string) {
	tokens.Lock()
	delete(tokens.by, k)
	tokens.Unlock()
}
