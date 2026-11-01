// Connectors: the unified "bridge type" surface. A Connector is a runtime
// capability an authoring agent DRAFTS and an admin APPROVES — the single
// envelope over the several mechanisms that used to be wired by hand (a
// SecureAPI credential + a call_ watch monitor, a remote MCP server, and later
// a desktop-daemon capability). The point is that an app developer — or the LLM
// itself — can add a whole new integration type ("hook into calendars") without
// a code change: draft a connector, an admin approves it, its tools go live.
//
// core owns only the record + lifecycle; each Kind supplies a ConnectorHandler
// (registered at startup, like RegisterTriggerAction) that knows how to
// validate the kind-specific Spec and, on approval, MATERIALIZE the real
// capability (and tear it down on delete/unapprove). core never interprets a
// Spec — it hands it to the handler.
//
// Governance mirrors credential drafts: create leaves Approved=false and the
// capability inert; the admin approves it in Admin > Connectors. The LLM never
// handles a secret — secret-bearing auth (a bearer token, a credential's
// secret) lives in the underlying subsystem (SecureAPI / the MCP token store),
// referenced only by name.
package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// connectorsTable holds Connector records keyed by Name.
const connectorsTable = "connectors"

// connectorNameRE keeps names tool-namespace-safe (they namespace a kind's
// tools, e.g. <name>.<tool> for remote_mcp) and collision-free.
var connectorNameRE = regexp.MustCompile(`^[a-z0-9_-]+$`)

// Connector is one declared bridge type. Persisted as-is (JSON) under Name; any
// secret lives in the subsystem the handler materializes into, never here.
type Connector struct {
	Name      string          `json:"name"`
	ID        string          `json:"id,omitempty"`       // keys its own space (below): made on create, never taken from a save or import
	// DisplayName is what people see it called (a page's choices, its row);
	// Name is its handle. Title gives one or the other.
	DisplayName string `json:"display_name,omitempty"`
	Kind      string          `json:"kind"`               // selects the ConnectorHandler
	Desc      string          `json:"desc,omitempty"`     // what it's for (human)
	Template  string          `json:"template,omitempty"` // ConnectorTemplate that authored it (drives Configure); portable
	Owner     string          `json:"owner,omitempty"`    // who drafted it (provenance)
	Spec      json.RawMessage `json:"spec,omitempty"`     // kind-specific payload
	Approved  bool            `json:"approved"`        // admin gate; inert until true
	Created   time.Time       `json:"created"`
	Updated   time.Time       `json:"updated"`
	LastError string          `json:"last_error,omitempty"` // last materialize failure
	Provided  bool            `json:"provided,omitempty"`   // gohort provides it (ProvideConnectors): installed at startup, never deleted; set only by MarkConnectorProvided
	Shipped   string          `json:"shipped,omitempty"`    // a provided one's SpecHash as gohort last shipped it: what tells an unedited spec from an edited one; set only by code
}

// --- kind registry -----------------------------------------------------------

// ConnectorHandler is the per-Kind backend. Registered once at startup via
// RegisterConnectorKind. Mirrors the trigger engine's app-supplied dispatchers:
// core owns the lifecycle, the handler owns what the Kind actually means.
type ConnectorHandler interface {
	// Validate checks the Spec is well-formed and safe to save. Called at
	// create/test time, before anything is materialized.
	Validate(c Connector) error
	// Materialize brings the real capability up. Called when an admin approves
	// the connector, on a save of an already-approved one, and (idempotently)
	// on restart for approved connectors.
	Materialize(c Connector) error
	// Teardown drops the capability. Called on delete and on unapprove.
	Teardown(c Connector) error
	// Summary is a one-line human description for the admin table + tool list.
	Summary(c Connector) string
}

// ConnectorAutoApprover is an optional ConnectorHandler capability. A kind that
// reaches out ONLY through resources already admin-governed — e.g. rest_poll,
// which calls an already-enabled SecureAPI credential — can opt to materialize
// on create without a separate approval step, matching the ungated `bridge`
// tool it mirrors. The admin can still Unapprove/Delete it in Admin >
// Connectors. Kinds that register NEW external reach (remote_mcp) do NOT
// implement this, so they stay pending until a human approves.
type ConnectorAutoApprover interface {
	AutoApprove() bool
}


// connectorAutoApproves reports whether a kind materializes on create.
func connectorAutoApproves(h ConnectorHandler) bool {
	aa, ok := h.(ConnectorAutoApprover)
	return ok && aa.AutoApprove()
}

var (
	connectorKinds  = map[string]ConnectorHandler{}
	connectorKindMu sync.RWMutex
)

// RegisterConnectorKind installs the handler for a Kind. Call once at startup.
func RegisterConnectorKind(kind string, h ConnectorHandler) {
	connectorKindMu.Lock()
	connectorKinds[kind] = h
	connectorKindMu.Unlock()
}

// ConnectorHandlerFor returns the handler for a Kind, if registered.
func ConnectorHandlerFor(kind string) (ConnectorHandler, bool) {
	connectorKindMu.RLock()
	defer connectorKindMu.RUnlock()
	h, ok := connectorKinds[kind]
	return h, ok
}

// connectorProber is a ConnectorHandler that can check, live, that a connector
// reaches what it points at, without approving or bringing it up: connect to
// the MCP server, run one poll. Optional: a kind without it is checked for its
// shape only.
type connectorProber interface {
	probe(ctx context.Context, c Connector) (string, error)
}

// ProbeConnector checks, live, that a connector reaches what it points at, and
// says what came back. probed is false for a kind with no live check, whose
// test stops at validating its configuration.
func ProbeConnector(ctx context.Context, c Connector) (preview string, probed bool, err error) {
	h, ok := ConnectorHandlerFor(c.Kind)
	if !ok {
		return "", false, fmt.Errorf("no handler for connector kind %q", c.Kind)
	}
	p, ok := h.(connectorProber)
	if !ok {
		return "", false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	out, err := p.probe(ctx, c)
	return out, true, err
}

// ConnectorKinds lists the registered kinds, sorted.
func ConnectorKinds() []string {
	connectorKindMu.RLock()
	defer connectorKindMu.RUnlock()
	out := make([]string, 0, len(connectorKinds))
	for k := range connectorKinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ValidateConnector runs the kind handler's Validate without persisting —
// the "test" path.
func ValidateConnector(c Connector) error {
	h, ok := ConnectorHandlerFor(c.Kind)
	if !ok {
		return fmt.Errorf("unknown connector kind %q (known: %s)", c.Kind, strings.Join(ConnectorKinds(), ", "))
	}
	return h.Validate(c)
}

// ConnectorSummary returns the handler's one-line summary, or the bare kind if
// the handler is missing.
func ConnectorSummary(c Connector) string {
	if h, ok := ConnectorHandlerFor(c.Kind); ok {
		return h.Summary(c)
	}
	return c.Kind
}

// --- store + lifecycle -------------------------------------------------------

// GetConnector fetches one connector by name.
func GetConnector(db Database, name string) (Connector, bool) {
	var c Connector
	if db == nil || name == "" {
		return c, false
	}
	ok := db.Get(connectorsTable, name, &c)
	return c, ok
}

// ConnectorCredentialRefs returns the SecureAPI credential names a connector's
// spec binds to (rest_poll spells it "credential", the MCP kinds "secure_cred").
// Exported so the credential admin UI can list connector-bound usage — a
// connector generates a tool (call_<cred> / <server>.<tool>) that isn't a
// TempTool, so a credential's tool scan would otherwise miss it.
func ConnectorCredentialRefs(spec json.RawMessage) []string {
	return connectorCredentialRefs(spec)
}

// ListConnectors returns every connector, sorted by name.
func ListConnectors(db Database) []Connector {
	if db == nil {
		return nil
	}
	var out []Connector
	for _, k := range db.Keys(connectorsTable) {
		var c Connector
		if db.Get(connectorsTable, k, &c) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SaveConnector validates and upserts a DRAFT connector. It does NOT approve or
// materialize a fresh connector — approval does. Approval state is preserved
// across a re-save; re-saving an already-approved connector re-materializes it
// so edits take effect.
func SaveConnector(db Database, c Connector) error {
	return saveConnector(db, c, true)
}

// saveConnectorDraft is SaveConnector for a connector that did not originate
// here: an imported recipe. It never auto-approves. An auto-approving kind
// earns that on create because the person creating it chose its target on
// this install; an imported one arrives with somebody else's URL and body, so
// it waits for an admin like every other imported artifact.
func saveConnectorDraft(db Database, c Connector) error {
	return saveConnector(db, c, false)
}

func saveConnector(db Database, c Connector, autoApprove bool) error {
	if db == nil {
		return fmt.Errorf("connector store not ready")
	}
	c.Name = strings.TrimSpace(c.Name)
	c.Kind = strings.TrimSpace(c.Kind)
	if !connectorNameRE.MatchString(c.Name) {
		return fmt.Errorf("name must match %s", connectorNameRE.String())
	}
	h, ok := ConnectorHandlerFor(c.Kind)
	if !ok {
		return fmt.Errorf("unknown connector kind %q (known: %s)", c.Kind, strings.Join(ConnectorKinds(), ", "))
	}
	if err := h.Validate(c); err != nil {
		return err
	}
	prev, existed := GetConnector(db, c.Name)
	now := time.Now()
	if existed {
		c.Created = prev.Created
		c.Approved = prev.Approved // approval only changes via Approve/Unapprove
		c.ID, c.Provided, c.Shipped = prev.ID, prev.Provided, prev.Shipped
	} else {
		c.Created = now
		c.Approved = false
		c.ID, c.Provided, c.Shipped = "", false, ""
	}
	if c.ID == "" {
		c.ID = newConnectionID()
	}
	c.Updated = now
	c.LastError = ""
	db.Set(connectorsTable, c.Name, c)
	// Already live? Re-materialize so an edit takes effect immediately.
	if c.Approved {
		if err := h.Materialize(c); err != nil {
			recordConnectorError(db, c.Name, err)
			return fmt.Errorf("saved, but re-materialize failed: %w", err)
		}
		return nil
	}
	// Fresh connector of an auto-approving kind (reaches out only through
	// already-governed resources) → materialize now, no separate approval.
	if autoApprove && !existed && connectorAutoApproves(h) {
		if err := ApproveConnector(db, c.Name); err != nil {
			return fmt.Errorf("saved, but auto-approve failed: %w", err)
		}
	}
	return nil
}

// ApproveConnector materializes the capability and marks the connector live.
// The gate the admin passes through.
func ApproveConnector(db Database, name string) error {
	c, ok := GetConnector(db, name)
	if !ok {
		return fmt.Errorf("no connector named %q", name)
	}
	h, ok := ConnectorHandlerFor(c.Kind)
	if !ok {
		return fmt.Errorf("unknown connector kind %q", c.Kind)
	}
	if err := h.Materialize(c); err != nil {
		recordConnectorError(db, name, err)
		return err
	}
	c.Approved = true
	c.Updated = time.Now()
	c.LastError = ""
	db.Set(connectorsTable, c.Name, c)
	return nil
}

// UnapproveConnector tears the capability down and marks the connector inert
// (without deleting the draft).
func UnapproveConnector(db Database, name string) error {
	c, ok := GetConnector(db, name)
	if !ok {
		return fmt.Errorf("no connector named %q", name)
	}
	if h, ok := ConnectorHandlerFor(c.Kind); ok {
		_ = h.Teardown(c)
	}
	c.Approved = false
	c.Updated = time.Now()
	db.Set(connectorsTable, c.Name, c)
	return nil
}

// DeleteConnector tears the capability down and removes the record.
func DeleteConnector(db Database, name string) error {
	if db == nil {
		return fmt.Errorf("connector store not ready")
	}
	c, ok := GetConnector(db, name)
	if !ok {
		return nil
	}
	if c.Provided {
		return fmt.Errorf("%s is provided with gohort, so it stays: set it up, or leave it unused", name)
	}
	if h, ok := ConnectorHandlerFor(c.Kind); ok {
		_ = h.Teardown(c)
	}
	db.Unset(connectorsTable, name)
	dropConnectionSpace(db, c)
	return nil
}

// Title is what a connector is shown as: its display name, or its name.
func (c Connector) Title() string {
	if t := strings.TrimSpace(c.DisplayName); t != "" {
		return t
	}
	return c.Name
}

// SetConnectorDisplayName names connector name for people as display (blank
// shows its name), refusing one another connector is shown as.
func SetConnectorDisplayName(db Database, name, display string) error {
	c, ok := GetConnector(db, name)
	if !ok {
		return fmt.Errorf("no connector named %q", name)
	}
	display = strings.TrimSpace(display)
	if len(display) > 80 {
		return fmt.Errorf("a display name is at most 80 characters")
	}
	if display != "" {
		for _, o := range ListConnectors(db) {
			if o.Name != name && strings.EqualFold(o.Title(), display) {
				return fmt.Errorf("%s is shown as %q already: give this one another display name", o.Name, display)
			}
		}
	}
	c.DisplayName, c.Updated = display, time.Now()
	db.Set(connectorsTable, c.Name, c)
	return nil
}

// FreeDisplayName is display, or it with the connector's name after it when
// another connector is shown as display already.
func FreeDisplayName(db Database, name, display string) string {
	display = strings.TrimSpace(display)
	for _, o := range ListConnectors(db) {
		if o.Name != name && strings.EqualFold(o.Title(), display) {
			return display + " (" + name + ")"
		}
	}
	return display
}

// ProvideConnectors, when set, installs the connections gohort provides
// that are not here yet (core/recipes): the choices a page offers by
// default. Called once at startup.
var ProvideConnectors func(db Database)

// MarkConnectorProvided marks connector name as one gohort provides, shown
// as label (blank keeps the name it is shown as), its spec as gohort
// shipped it hashing to shipped: kept at startup, and never deleted.
func MarkConnectorProvided(db Database, name, label, shipped string) error {
	c, ok := GetConnector(db, name)
	if !ok {
		return fmt.Errorf("no connector named %q", name)
	}
	c.Provided, c.Shipped = true, shipped
	if label = strings.TrimSpace(label); label != "" {
		c.DisplayName = label
	}
	c.Updated = time.Now()
	db.Set(connectorsTable, c.Name, c)
	return nil
}

// SpecHash is a spec's fingerprint, the same however its JSON is spaced or
// ordered: what tells whether a provided connection's spec is the one gohort
// shipped.
func SpecHash(spec json.RawMessage) string {
	var v any
	if json.Unmarshal(spec, &v) != nil {
		return ""
	}
	canon, _ := json.Marshal(v)
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:12])
}

// ReloadApprovedConnectors re-materializes every approved connector
// (idempotent). Call once at startup, after the subsystems the handlers
// target (e.g. MCP) are ready. Mirrors MCP().Reload's restore behavior.
func ReloadApprovedConnectors(db Database) {
	// Unfreeze knobs a preset baked into stored specs before they became
	// tunables. Runs BEFORE materialize so a connector comes up on the resolved
	// default rather than the frozen one. Marker-guarded, so it fires once.
	MigrateFrozenImageDefaults(db)
	for _, c := range ListConnectors(db) {
		if !c.Approved {
			continue
		}
		h, ok := ConnectorHandlerFor(c.Kind)
		if !ok {
			continue
		}
		if err := h.Materialize(c); err != nil {
			recordConnectorError(db, c.Name, err)
			Warn("[connector] %q re-materialize failed: %v", c.Name, err)
		}
	}
}

// recordConnectorError stamps LastError onto a stored connector so the admin
// table can surface why a materialize failed.
func recordConnectorError(db Database, name string, err error) {
	c, ok := GetConnector(db, name)
	if !ok {
		return
	}
	c.LastError = err.Error()
	db.Set(connectorsTable, name, c)
}

// ConnectorsUsingCredential names the connectors whose spec references a
// SecureAPI credential.
//
// It exists because a CONNECTOR is a declaring consumer exactly as a
// tool with fetch_via: is, and the admin's "orphaned" check only knew
// about tools — so a secured credential reached only through a
// connector was badged unreachable while working perfectly. Peer image
// backends are the case that surfaced it; the local image and messaging
// connectors have the same shape.
//
// The probe decodes just the shared "credential" key rather than every
// kind's full spec: the alternative is a switch over connector kinds
// that goes stale the first time somebody adds one, and this answers the
// only question being asked.
func ConnectorsUsingCredential(db Database, cred string) []string {
	cred = strings.TrimSpace(cred)
	if db == nil || cred == "" {
		return nil
	}
	var out []string
	for _, c := range ListConnectors(db) {
		if len(c.Spec) == 0 {
			continue
		}
		var probe struct {
			Credential string `json:"credential"`
		}
		if json.Unmarshal(c.Spec, &probe) != nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(probe.Credential), cred) {
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}

// --- a connection's own space ---------------------------------------------
//
// A connection owns its space. Creating a connector gives it an id, made
// here and never taken from a recipe, and under that id a store of its own:
// its settings (config) and its secrets, encrypted. The name stays the
// handle people and pages use; the id keys the space, so nothing one
// connection keeps can collide with another's, and deleting a connection
// deletes what it kept.
//
// A secret is kept with the addresses it was given for, and a call is made
// through it only there: the vault's guarantee that a key goes nowhere it was
// not given for, held by the connection rather than by a named credential.

const (
	connectionConfigTable = "config"
	connectionSecretTable = "secret"
	connectionAccessTable = "access"
)

// connectionScopeName is how an agent's credential scope names connection
// name: the name its secret goes by when a call is made through it, so an
// agent switched off it is refused at the call as well as in its tools.
func connectionScopeName(name string) string { return "connection:" + strings.TrimSpace(name) }

// Access is who may reach connection c through an agent's tools (an image
// or music connector offered to agents, a Research Source, a flow): empty is
// everyone. A page that has it serve the whole instance (an LLM tier, Web
// Search, Embeddings) serves every user whatever it says.
func (c Connector) Access(db Database) []string {
	sp := connectionSpace(db, c)
	if sp == nil {
		return nil
	}
	var users []string
	sp.Get(connectionAccessTable, "users", &users)
	return users
}

// SetAccess limits who may reach connection c through an agent's tools to
// users; none is everyone.
func (c Connector) SetAccess(db Database, users []string) error {
	sp := connectionSpace(db, c)
	if sp == nil {
		return fmt.Errorf("%s is not a connection: it has no space to keep who may reach it", c.Name)
	}
	var keep []string
	seen := map[string]bool{}
	for _, u := range users {
		if u = strings.TrimSpace(u); u != "" && !seen[u] {
			seen[u] = true
			keep = append(keep, u)
		}
	}
	if len(keep) == 0 {
		sp.Unset(connectionAccessTable, "users")
		return nil
	}
	sort.Strings(keep)
	sp.Set(connectionAccessTable, "users", keep)
	return nil
}

// ConnectionBarred says why the agent running in s may not reach the
// connection named name through its tools, or nil: the agent has it switched
// off (its credential scope), or the connection is limited to users s's
// user is not one of. A session with no user is not one of them. No session
// at all is a capability the instance chose for everyone, and is not barred.
func (s *ToolSession) ConnectionBarred(name string) error {
	if s == nil {
		return nil
	}
	c, ok := GetConnector(RootDB, strings.TrimSpace(name))
	if !ok {
		return nil
	}
	if s.CredentialDenied(connectionScopeName(c.Name)) {
		return fmt.Errorf("the connection %s is switched off for this agent (its credential scope on the agent editor)", c.Title())
	}
	if users := c.Access(RootDB); len(users) > 0 && !slices.Contains(users, strings.TrimSpace(s.Username)) {
		return fmt.Errorf("the connection %s is limited to named users, and this run's user is not one of them: an admin grants it under Access on its row in Admin > Extensions > Connectors", c.Title())
	}
	return nil
}

// newConnectionID is a fresh connection id.
func newConnectionID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "c_" + hex.EncodeToString(b)
}

// connectionSpace is connection c's own store, or nil before it has an id.
func connectionSpace(db Database, c Connector) Database {
	if db == nil || strings.TrimSpace(c.ID) == "" {
		return nil
	}
	return db.Sub("connection:" + c.ID)
}

// ConnectionConfig is the settings connection c keeps: the values given for
// what its spec declares, by name.
func ConnectionConfig(db Database, c Connector) map[string]string {
	sp := connectionSpace(db, c)
	if sp == nil {
		return nil
	}
	out := map[string]string{}
	for _, k := range sp.Keys(connectionConfigTable) {
		var v string
		if sp.Get(connectionConfigTable, k, &v) {
			out[k] = v
		}
	}
	return out
}

// SetConnectionConfig replaces the settings connection c keeps with values;
// a blank value is left out, so the spec's default applies.
func SetConnectionConfig(db Database, c Connector, values map[string]string) error {
	sp := connectionSpace(db, c)
	if sp == nil {
		return fmt.Errorf("connection %s has no space yet: save it once to give it one", c.Name)
	}
	sp.Drop(connectionConfigTable)
	for k, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			if err := sp.TrySet(connectionConfigTable, k, v); err != nil {
				return fmt.Errorf("saving %s's setting %s: %w", c.Name, k, err)
			}
		}
	}
	return nil
}

// ConnectionSecret is one secret a connection keeps: its value, and the
// addresses it was given for, the only ones a call may take it to.
type ConnectionSecret struct {
	Value     string    `json:"value"`
	Addresses []string  `json:"addresses"`
	Set       time.Time `json:"set"`
}

// GetConnectionSecret is connection c's secret name.
func GetConnectionSecret(db Database, c Connector, name string) (ConnectionSecret, bool) {
	var s ConnectionSecret
	sp := connectionSpace(db, c)
	if sp == nil || !sp.Get(connectionSecretTable, name, &s) {
		return s, false
	}
	return s, true
}

// UnreadableSecrets are the secrets connection c keeps that cannot be read
// back (a store moved to another machine, a record damaged): there, so a
// call is not refused for a key never given, and unreadable, so it is
// refused anyway.
func (c Connector) UnreadableSecrets(db Database) []string {
	sp := connectionSpace(db, c)
	if sp == nil {
		return nil
	}
	var out []string
	for _, name := range sp.Keys(connectionSecretTable) {
		if _, ok := GetConnectionSecret(db, c, name); !ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// SetConnectionSecret keeps secret value as connection c's secret name, for
// addresses: the address its calls go to, and any other it also reaches.
func SetConnectionSecret(db Database, c Connector, name, value string, addresses []string) error {
	sp := connectionSpace(db, c)
	if sp == nil {
		return fmt.Errorf("connection %s has no space yet: save it once to give it one", c.Name)
	}
	var clean []string
	for _, a := range addresses {
		if a = strings.TrimRight(strings.TrimSpace(a), "/"); a != "" {
			clean = append(clean, a)
		}
	}
	sp.CryptSet(connectionSecretTable, name, ConnectionSecret{Value: strings.TrimSpace(value), Addresses: clean, Set: time.Now()})
	return nil
}

// dropConnectionSpace removes everything connection c kept.
func dropConnectionSpace(db Database, c Connector) {
	if sp := connectionSpace(db, c); sp != nil {
		sp.Drop(connectionConfigTable)
		sp.Drop(connectionSecretTable)
	}
}

// ConnectionCredential is the credential one call of connection name is made
// through: never stored, carrying the connection's secret (or, for an AWS
// signature with no key pair, the machine's identity under profile), and
// allowed only the addresses the secret was given for. send is how the
// secret goes: bearer, header:<name>, query:<name>, basic:<username> (or
// basic, the secret being user:password) or aws_sigv4.
func ConnectionCredential(name, send, secret, profile string, addresses []string) (SecureCredential, error) {
	if len(addresses) == 0 {
		return SecureCredential{}, fmt.Errorf("the connection %s's secret was given for no address", name)
	}
	c := SecureCredential{Name: connectionScopeName(name), BaseURL: addresses[0], connection: name,
		Description: "The secret connection " + name + " keeps, for one call."}
	for _, a := range addresses[1:] {
		if u, err := url.Parse(a); err == nil && u.Host != "" {
			c.AlsoCovers = append(c.AlsoCovers, strings.ToLower(u.Scheme+"://"+u.Host))
		}
	}
	how, param, _ := strings.Cut(strings.TrimSpace(send), ":")
	secret = strings.TrimSpace(secret)
	switch how {
	case "bearer":
		c.Type = SecureCredBearer
	case "header", "query":
		if param = strings.TrimSpace(param); param == "" {
			return c, fmt.Errorf("send %q names no %s", send, how)
		}
		c.Type, c.ParamName = SecureCredHeader, param
		if how == "query" {
			c.Type = SecureCredQuery
		}
	case "basic":
		c.Type, c.Username = SecureCredBasicAuth, strings.TrimSpace(param)
		if c.Username == "" && secret != "" && !strings.Contains(secret, ":") {
			return c, fmt.Errorf("send basic with no username takes user:password")
		}
	case "aws_sigv4":
		c.Type = SecureCredAWS
		if secret == "" {
			c.AWSMachine, c.AWSProfile = true, strings.TrimSpace(profile)
			return c, nil
		}
		if _, err := awsKeyPair(secret); err != nil {
			return c, err
		}
	default:
		return c, fmt.Errorf("send %q: a secret is sent as bearer, header:<name>, query:<name>, basic:<username> or aws_sigv4", send)
	}
	if secret == "" {
		return c, fmt.Errorf("the connection %s has no key yet: give it on the page that uses it, or on its row under Admin > Extensions > Connectors", name)
	}
	c.inlineSecret = secret
	return c, nil
}

// connectionOnly refuses a credential that ConnectionCredential did not
// make: the calls below skip the lookup by name, so a stored credential goes
// through its own, with its shares and limits.
func connectionOnly(c SecureCredential) error {
	if c.connection == "" {
		return fmt.Errorf("credential %q is not a connection's: call it by name", c.Name)
	}
	return nil
}

// DispatchConnection makes r through a connection's credential: the same
// governed path as a stored credential's call (the address limits, Private
// mode, the audit, the response limits). r.Credential is ignored.
func (s *SecureAPI) DispatchConnection(sess *ToolSession, c SecureCredential, r ToolCallRequest) (string, error) {
	if err := connectionOnly(c); err != nil {
		return "", err
	}
	args := map[string]any{"url": r.URL, "method": firstNonEmpty(r.Method, "GET")}
	if r.Body != "" {
		args["body"] = r.Body
	}
	if r.ContentType != "" {
		args["__content_type"] = r.ContentType
	}
	if r.PipeFollowing {
		args["__pipe_following"] = true
	}
	if h := toolCallHeaderArgs(r.Headers); h != nil {
		args["request_headers"] = h
	}
	if r.Upload != nil {
		args[secureUploadArg] = r.Upload
	}
	if r.TimeoutSecs > 0 {
		args[secureTimeoutArg] = r.TimeoutSecs
	}
	return s.dispatch(c, args, sess)
}

// OpenConnectionStream is OpenStream through a connection's credential.
func (s *SecureAPI) OpenConnectionStream(sess *ToolSession, c SecureCredential, r StreamRequest) (*http.Response, func(n int64), error) {
	if err := connectionOnly(c); err != nil {
		return nil, nil, err
	}
	return s.openStream(sess, c, r)
}

// ModelAuth is what a model client, which makes its own requests, is given
// from a connection's credential for rawURL: the key, or the AWS signing.
// Only for an address the credential allows.
func (c SecureCredential) ModelAuth(rawURL string) (string, AWSModelAuth, error) {
	var aws AWSModelAuth
	if err := connectionOnly(c); err != nil {
		return "", aws, err
	}
	if !urlAllowedByCredential(c, rawURL) {
		return "", aws, fmt.Errorf("the connection %s's secret was not given for %s", c.connection, rawURL)
	}
	if c.Type != SecureCredAWS {
		return c.inlineSecret, aws, nil
	}
	host := rawURL
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	host, _, _ = strings.Cut(host, "/")
	region, _, err := awsScope(c, host)
	if err != nil {
		return "", aws, err
	}
	aws.Region, aws.Profile, aws.Machine = region, c.AWSProfile, c.AWSMachine
	if !c.AWSMachine {
		kp, err := awsKeyPair(c.inlineSecret)
		if err != nil {
			return "", aws, err
		}
		aws.AccessKey, aws.SecretKey = kp.AccessKey, kp.SecretKey
	}
	return "", aws, nil
}

// MoveCredentialToConnection keeps the secret of the global credential name
// as connection c's secret setting, for addresses: how a connector made
// before connections kept their own keys has its key moved, the value never
// leaving core. A credential with no secret (one signing as this machine, a
// draft) moves nothing, and says so.
func (s *SecureAPI) MoveCredentialToConnection(db Database, name string, c Connector, setting string, addresses []string) (bool, error) {
	cred, ok := s.Load(name)
	if !ok || cred.Owner != "" || cred.IsPerUser() || (cred.Type == SecureCredAWS && cred.AWSMachine) {
		return false, nil
	}
	sec, ok := s.loadSecret(name)
	if sec = strings.TrimSpace(sec); !ok || sec == "" || sec == "(pending)" {
		return false, nil
	}
	if cred.Type == SecureCredAWS {
		if _, err := awsKeyPair(sec); err != nil {
			return false, nil
		}
	}
	return true, SetConnectionSecret(db, c, setting, sec, addresses)
}
