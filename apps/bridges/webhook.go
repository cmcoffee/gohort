// Provider-native webhook adapter — the PUSH counterpart to the rest_messaging
// poller. Where the poller pulls a service's API on a timer, this receives the
// service's real-time event callbacks on a public route and feeds them through
// the SAME Bridges inbound contract (ingestInbound), so channel binding, dedup,
// dispatch, and outbound all work identically.
//
// A raw provider webhook can't hit /api/hook directly: it doesn't speak the
// hookRequest shape, doesn't hold a BridgeKey, and expects a provider-specific
// URL-verification handshake + request signature. This adapter bridges that gap.
// One public route, /bridges/api/webhook/<connector-name>, dispatches by connector
// name to the provider registered for that connector's spec.WebhookProvider.
//
// The signing secret (Slack) / clientState (Graph) is a SECRET, so it lives in an
// encrypted, connector-scoped bridges table — NEVER in the connector Spec (which
// exports secret-free). Slack's signing secret is admin-set (POST
// /bridges/api/webhook-secret); Graph's clientState is generated at materialize.
//
// Reply delivery: a webhook connector has no poll tick to drain the outbox, so
// startPoller runs an outbound-only loop for it (outboundLoop) that reuses
// deliverOutbound on a short timer.
//
// Two turnkey providers:
//   - SLACK (Events API): paste the route as the app's Request URL, admin sets the
//     signing secret, done.
//   - GRAPH (Teams change notifications): on materialize the bridge creates a Graph
//     subscription against this route (validationToken challenge + auto-generated
//     clientState), renews it before its ~60-min expiry, and deletes it on
//     teardown (graphEnsureSubscription / graphSubscriptionLoop / teardownWebhook).
//     Requires this deployment's public URL to be reachable by Graph.
package bridges

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const webhookSecretTable = "bridges_webhook_secrets" // connector name → signing secret (encrypted)

// webhookProvider is the per-service adapter: answer the URL-verification
// handshake, authenticate the request, and turn a verified event body into
// normalized inbound messages.
type webhookProvider interface {
	// challenge answers a provider URL-verification handshake and returns true if
	// it consumed the request (nothing more to do). Runs BEFORE verify because the
	// handshake establishes the endpoint before a secret is in play.
	challenge(w http.ResponseWriter, r *http.Request, body []byte) bool
	// verify authenticates the request against the connector's stored secret.
	verify(r *http.Request, body []byte, secret string) error
	// extract parses inbound messages from a verified event body. It may call the
	// service's API (spec.Credential) to resolve a message the notification only
	// references (Graph). Return (nil, nil) to ignore an irrelevant event.
	extract(body []byte, spec RestMessagingSpec) ([]hookRequest, error)
	// autoSecret returns a secret to generate + store at materialize (Graph
	// clientState) with generated=true, or ("", false) when the admin must set it.
	autoSecret() (secret string, generated bool)
}

var webhookProviders = map[string]webhookProvider{
	"slack": slackProvider{},
	"graph": graphProvider{},
}

// handleWebhook is the public inbound route for every webhook connector.
func (T *Bridges) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/webhook/"), "/")
	if name == "" {
		http.Error(w, "connector name required in path", http.StatusNotFound)
		return
	}
	c, ok := GetConnector(RootDB, name)
	if !ok || c.Kind != RestMessagingConnectorKind {
		http.Error(w, "no such webhook connector", http.StatusNotFound)
		return
	}
	var spec RestMessagingSpec
	if err := json.Unmarshal(c.Spec, &spec); err != nil {
		http.Error(w, "bad connector", http.StatusInternalServerError)
		return
	}
	prov, ok := webhookProviders[spec.WebhookProvider]
	if !ok {
		http.Error(w, "connector is not a webhook connector", http.StatusNotFound)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))

	// URL-verification handshake first — may arrive before/without a secret.
	if prov.challenge(w, r, body) {
		return
	}
	// A draft (unapproved) connector shouldn't route; ack so the provider doesn't
	// hammer retries, but do nothing.
	if !c.Approved {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	secret := T.getWebhookSecret(name)
	if secret == "" {
		Warn("[bridges] webhook %q rejected: no signing secret set (POST /bridges/api/webhook-secret)", name)
		http.Error(w, "webhook not configured", http.StatusUnauthorized)
		return
	}
	if err := prov.verify(r, body, secret); err != nil {
		Warn("[bridges] webhook %q signature check failed: %v", name, err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Master panic switch: recorded-only when transport is off.
	if !T.config().Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	reqs, err := prov.extract(body, spec)
	if err != nil {
		Warn("[bridges] webhook %q extract failed: %v", name, err)
		w.WriteHeader(http.StatusAccepted) // ack; don't make the provider retry a bad payload
		return
	}
	key := BridgeKey{Owner: c.Owner, Service: spec.Service, Enabled: true, Name: c.Name}
	for _, req := range reqs {
		T.ingestInbound(key, req)
	}
	w.WriteHeader(http.StatusOK)
}

// handleWebhookSecret lets the connector's owner set its signing secret (Slack
// signing secret / a manual Graph clientState). Stored encrypted, out of the Spec.
//
//	POST /bridges/api/webhook-secret {"connector":"...","secret":"..."}
func (T *Bridges) handleWebhookSecret(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct{ Connector, Secret string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Connector)
	c, ok := GetConnector(RootDB, name)
	if !ok {
		http.Error(w, "no such connector", http.StatusNotFound)
		return
	}
	if c.Owner != "" && c.Owner != user && !IsAdminAllowed(r) {
		http.Error(w, "not your connector", http.StatusForbidden)
		return
	}
	if strings.TrimSpace(req.Secret) == "" {
		http.Error(w, "secret required", http.StatusBadRequest)
		return
	}
	T.setWebhookSecret(name, strings.TrimSpace(req.Secret))
	Log("[bridges] webhook signing secret set for %q by %s", name, user)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (T *Bridges) getWebhookSecret(name string) string {
	var s string
	T.DB.Get(webhookSecretTable, name, &s)
	return s
}

func (T *Bridges) setWebhookSecret(name, secret string) {
	T.DB.CryptSet(webhookSecretTable, name, secret)
}

// --- Slack (Events API) — turnkey --------------------------------------------

type slackProvider struct{}

func (slackProvider) challenge(w http.ResponseWriter, r *http.Request, body []byte) bool {
	var p struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if json.Unmarshal(body, &p) == nil && p.Type == "url_verification" && p.Challenge != "" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(p.Challenge))
		return true
	}
	return false
}

// verify checks Slack's v0 signature: hmac_sha256(signing_secret, "v0:ts:body"),
// hex, prefixed "v0=". Rejects a stale timestamp (>5 min) to blunt replay.
func (slackProvider) verify(r *http.Request, body []byte, secret string) error {
	ts := r.Header.Get("X-Slack-Request-Timestamp")
	sig := r.Header.Get("X-Slack-Signature")
	if ts == "" || sig == "" {
		return fmt.Errorf("missing X-Slack-Signature/Timestamp headers")
	}
	tsN, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("bad timestamp")
	}
	if d := time.Now().Unix() - tsN; d > 300 || d < -300 {
		return fmt.Errorf("stale request timestamp (%ds skew)", d)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// extract pulls a single user message from a Slack event_callback. It DROPS the
// bridge's own outbound (bot_id set) — critical to avoid a reply→event→reply loop
// — plus edits/joins/other subtypes and non-message events.
func (slackProvider) extract(body []byte, _ RestMessagingSpec) ([]hookRequest, error) {
	var p struct {
		Type  string `json:"type"`
		Event struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			BotID   string `json:"bot_id"`
			Channel string `json:"channel"`
			User    string `json:"user"`
			Text    string `json:"text"`
			Ts      string `json:"ts"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	if p.Type != "event_callback" {
		return nil, nil
	}
	e := p.Event
	if e.Type != "message" || e.Subtype != "" || e.BotID != "" {
		return nil, nil // skip bot/edited/system events (and our own replies)
	}
	if e.Channel == "" || strings.TrimSpace(e.Text) == "" {
		return nil, nil
	}
	return []hookRequest{{ChatID: e.Channel, Handle: e.User, Text: e.Text, MsgID: e.Ts}}, nil
}

func (slackProvider) autoSecret() (string, bool) { return "", false } // admin sets the signing secret

// --- Microsoft Graph (change notifications) ----------------------------------
//
// challenge + clientState verify + resource-fetch extract are real; the Graph
// SUBSCRIPTION lifecycle (create /subscriptions with this notificationUrl +
// clientState, and renew before the ~60-min expiry) is NOT yet automated — until
// it is, a Graph webhook only fires if the subscription is created externally.

type graphProvider struct{}

func (graphProvider) challenge(w http.ResponseWriter, r *http.Request, _ []byte) bool {
	if vt := r.URL.Query().Get("validationToken"); vt != "" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(vt))
		return true
	}
	return false
}

func (graphProvider) verify(_ *http.Request, body []byte, secret string) error {
	var p struct {
		Value []struct {
			ClientState string `json:"clientState"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return err
	}
	if len(p.Value) == 0 {
		return fmt.Errorf("no notifications in payload")
	}
	for _, v := range p.Value {
		if v.ClientState != secret {
			return fmt.Errorf("clientState mismatch")
		}
	}
	return nil
}

// extract fetches each referenced message (the notification carries only a
// resource path, not the body) and maps it with the connector's teams-style
// dot-paths.
func (graphProvider) extract(body []byte, spec RestMessagingSpec) ([]hookRequest, error) {
	var p struct {
		Value []struct {
			Resource string `json:"resource"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	m := spec.Map
	var out []hookRequest
	for _, v := range p.Value {
		if strings.TrimSpace(v.Resource) == "" {
			continue
		}
		url := "https://graph.microsoft.com/v1.0/" + strings.TrimPrefix(v.Resource, "/")
		respBody, status, err := authedRequest(spec.Credential, "GET", url, "")
		if err != nil || status >= 300 {
			Warn("[bridges] graph webhook resource fetch %s → err=%v status=%d", url, err, status)
			continue
		}
		var root any
		if json.Unmarshal([]byte(respBody), &root) != nil {
			continue
		}
		req := hookRequest{
			ChatID:      firstNonEmpty(jsonPathString(root, m.ChatID), spec.ChatIDConst),
			MsgID:       jsonPathString(root, m.MsgID),
			Handle:      jsonPathString(root, m.Sender),
			DisplayName: jsonPathString(root, m.SenderName),
			Text:        jsonPathString(root, m.Text),
		}
		if spec.StripHTML {
			req.Text = stripHTML(req.Text)
		}
		if req.ChatID != "" && strings.TrimSpace(req.Text) != "" {
			out = append(out, req)
		}
	}
	return out, nil
}

func (graphProvider) autoSecret() (string, bool) { return newToken(), true } // generated clientState

// --- Graph subscription lifecycle --------------------------------------------
//
// Graph only pushes notifications while a live /subscriptions resource points at
// our route. Channel-message subscriptions max out at ~60 min, so they must be
// created at materialize and renewed on a timer, and deleted on teardown. State
// (the subscription id + expiry) is persisted so a restart renews the existing
// one instead of orphaning it.

const webhookSubTable = "bridges_webhook_subs" // connector name → webhookSub

const graphBase = "https://graph.microsoft.com/v1.0/"

// graphSubExpiry is how far ahead each (re)subscription reaches — safely under the
// ~60-min channel-message cap. Renewal cadence is owned by the core push-sub
// sweeper (graphRenewBefore).
const (
	graphSubExpiry   = 55 * time.Minute
	graphRenewBefore = 20 * time.Minute // renew once <20 min remain
	graphSubResource = "created"        // changeType: new channel messages
)

// graphPushKind is the core push-sub handler kind for Graph channel-message
// webhook subscriptions.
const graphPushKind = "graph_channel_msgs"

// graphPushHandler is the core PushSubHandler backend: it resolves the connector
// by key and delegates to the create/renew (Ensure) and delete calls below, so
// the shared sweeper owns renewal + restart recovery.
type graphPushHandler struct{ T *Bridges }

func (g graphPushHandler) Ensure(key string) (time.Time, error) {
	c, spec, err := g.T.graphConnector(key)
	if err != nil {
		return time.Time{}, err
	}
	return g.T.graphEnsureSubscription(c, spec)
}

func (g graphPushHandler) Delete(key string) error {
	c, spec, err := g.T.graphConnector(key)
	if err != nil {
		return nil // gone already; nothing to delete
	}
	g.T.graphDeleteSubscription(c, spec)
	return nil
}

// graphConnector resolves a connector name to its record + parsed spec.
func (T *Bridges) graphConnector(name string) (Connector, RestMessagingSpec, error) {
	c, ok := GetConnector(RootDB, name)
	if !ok {
		return Connector{}, RestMessagingSpec{}, fmt.Errorf("connector %q not found", name)
	}
	var spec RestMessagingSpec
	if err := json.Unmarshal(c.Spec, &spec); err != nil {
		return Connector{}, RestMessagingSpec{}, err
	}
	return c, spec, nil
}

type webhookSub struct {
	ID        string `json:"id"`
	Resource  string `json:"resource"`
	ExpiresAt string `json:"expires_at"`
}

func (T *Bridges) getWebhookSub(name string) webhookSub {
	var s webhookSub
	T.DB.Get(webhookSubTable, name, &s)
	return s
}

func (T *Bridges) setWebhookSub(name string, s webhookSub) { T.DB.Set(webhookSubTable, name, s) }

// graphChannelResource derives the subscription resource (e.g.
// "teams/{id}/channels/{id}/messages") from the connector's send_url, which the
// teams preset already points at that exact endpoint.
func graphChannelResource(spec RestMessagingSpec) (string, error) {
	su := strings.TrimSpace(spec.SendURL)
	for _, base := range []string{graphBase, "https://graph.microsoft.com/beta/"} {
		if strings.HasPrefix(su, base) {
			return strings.TrimPrefix(su, base), nil
		}
	}
	return "", fmt.Errorf("can't derive subscription resource — send_url must be a graph.microsoft.com messages endpoint (got %q); use the teams preset", su)
}

// graphEnsureSubscription creates the subscription, or renews (PATCH) an existing
// one, persisting its id + expiry and returning the new expiry. Called
// synchronously at materialize (so a failure surfaces to the approving admin) and
// by the core push-sub sweeper on each renewal.
func (T *Bridges) graphEnsureSubscription(c Connector, spec RestMessagingSpec) (time.Time, error) {
	resource, err := graphChannelResource(spec)
	if err != nil {
		return time.Time{}, err
	}
	clientState := T.getWebhookSecret(c.Name)
	if clientState == "" {
		return time.Time{}, fmt.Errorf("no clientState secret for %q", c.Name)
	}
	notifyURL := DashboardURL() + "/bridges/api/webhook/" + c.Name
	expTime := time.Now().Add(graphSubExpiry)
	exp := expTime.UTC().Format(time.RFC3339)

	sub := T.getWebhookSub(c.Name)
	if sub.ID != "" {
		// Renew in place first.
		body, _ := json.Marshal(map[string]any{"expirationDateTime": exp})
		_, status, err := authedRequest(spec.Credential, "PATCH", graphBase+"subscriptions/"+sub.ID, string(body))
		if err == nil && status < 300 {
			sub.ExpiresAt = exp
			T.setWebhookSub(c.Name, sub)
			return expTime, nil
		}
		Warn("[bridges] graph webhook %q renew failed (status=%d) — recreating subscription", c.Name, status)
	}

	body, _ := json.Marshal(map[string]any{
		"changeType":         graphSubResource,
		"notificationUrl":    notifyURL,
		"resource":           resource,
		"expirationDateTime": exp,
		"clientState":        clientState,
	})
	respBody, status, err := authedRequest(spec.Credential, "POST", graphBase+"subscriptions", string(body))
	if err != nil {
		return time.Time{}, err
	}
	if status >= 300 {
		return time.Time{}, fmt.Errorf("create subscription → HTTP %d: %s (notificationUrl %s must be publicly reachable for Graph to validate)", status, snippet(respBody), notifyURL)
	}
	var created struct {
		ID  string `json:"id"`
		Exp string `json:"expirationDateTime"`
	}
	if err := json.Unmarshal([]byte(respBody), &created); err != nil || created.ID == "" {
		return time.Time{}, fmt.Errorf("subscription created but response unparseable: %s", snippet(respBody))
	}
	T.setWebhookSub(c.Name, webhookSub{ID: created.ID, Resource: resource, ExpiresAt: firstNonEmpty(created.Exp, exp)})
	Log("[bridges] graph webhook %q subscription %s created (resource=%s, expires=%s)", c.Name, created.ID, resource, created.Exp)
	if t, perr := time.Parse(time.RFC3339, created.Exp); perr == nil {
		return t, nil
	}
	return expTime, nil
}

// graphDeleteSubscription tears the subscription down (unapprove / delete).
func (T *Bridges) graphDeleteSubscription(c Connector, spec RestMessagingSpec) {
	sub := T.getWebhookSub(c.Name)
	if sub.ID == "" {
		return
	}
	_, status, err := authedRequest(spec.Credential, "DELETE", graphBase+"subscriptions/"+sub.ID, "")
	Log("[bridges] graph webhook %q subscription %s deleted (status=%d err=%v)", c.Name, sub.ID, status, err)
	T.DB.Unset(webhookSubTable, c.Name)
}

// teardownWebhook releases provider-side resources when a webhook connector is
// unapproved or deleted. Called from the messaging-poller teardown seam. The
// core push-sub primitive owns the delete + record cleanup via the handler.
func (T *Bridges) teardownWebhook(c Connector) {
	var spec RestMessagingSpec
	if json.Unmarshal(c.Spec, &spec) != nil {
		return
	}
	if spec.WebhookProvider == "graph" {
		RemovePushSubscription(graphPushKind, c.Name)
	}
}
