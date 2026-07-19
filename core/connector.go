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
	"encoding/json"
	"fmt"
	"regexp"
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
	Kind      string          `json:"kind"`            // selects the ConnectorHandler
	Desc      string          `json:"desc,omitempty"`  // what it's for (human)
	Owner     string          `json:"owner,omitempty"` // who drafted it (provenance)
	Spec      json.RawMessage `json:"spec,omitempty"`  // kind-specific payload
	Approved  bool            `json:"approved"`        // admin gate; inert until true
	Created   time.Time       `json:"created"`
	Updated   time.Time       `json:"updated"`
	LastError string          `json:"last_error,omitempty"` // last materialize failure
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
	} else {
		c.Created = now
		c.Approved = false
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
	if !existed && connectorAutoApproves(h) {
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
	if h, ok := ConnectorHandlerFor(c.Kind); ok {
		_ = h.Teardown(c)
	}
	db.Unset(connectorsTable, name)
	return nil
}

// ReloadApprovedConnectors re-materializes every approved connector
// (idempotent). Call once at startup, after the subsystems the handlers
// target (e.g. MCP) are ready. Mirrors MCP().Reload's restore behavior.
func ReloadApprovedConnectors(db Database) {
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
