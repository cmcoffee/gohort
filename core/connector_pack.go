// Connector packs: the portable, secret-free export/import format for the
// unified connector surface. A Connector is already a clean JSON recipe
// (Name/Kind/Desc/Spec + governance/identity fields); a pack is just the
// portable subset of one or more connectors wrapped in a small envelope so a
// whole set of integrations can be saved, shared, backed up, or shipped as a
// starter bundle.
//
// The design deliberately mirrors the agent recipe (apps/orchestrate: strip
// identity, reassign owner, reborn on import): the runtime/identity fields
// (Owner/Approved/Created/Updated/LastError) never travel — the importer owns
// what they import, and governance re-applies on their install.
//
// SECRETS NEVER TRAVEL. A connector's Spec references auth by NAME only (a
// SecureAPI credential, a per-user OAuth account) — the secret lives in the
// subsystem the handler materializes into, not in the connector. That is what
// makes a pack shareable: it carries the shape of an integration, and the
// importer supplies (or drafts) the matching credential on their side.
//
// The envelope is intentionally connector-scoped but generically shaped
// (Bundle/ExportedAt header) so a future unified artifact bundle — agents +
// pipelines + connectors together — can adopt the same header without a
// re-design.
package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ConnectorPackBundle identifies the pack wire format. Bumped only on a
// breaking change to the envelope; importers should accept older minor forms.
const ConnectorPackBundle = "gohort.connectors/v1"

// PortableConnector is the identity-free, secret-free recipe shape: exactly the
// fields that describe WHAT an integration is, none of the fields that describe
// a particular install of it. This is what a pack carries and what import
// reconstitutes a live Connector from.
type PortableConnector struct {
	Name string          `json:"name"`
	Kind string          `json:"kind"`
	Desc string          `json:"desc,omitempty"`
	Spec json.RawMessage `json:"spec,omitempty"`
}

// ConnectorPack is a portable bundle of one or more connectors.
type ConnectorPack struct {
	Bundle     string              `json:"bundle"`
	ExportedAt time.Time           `json:"exported_at"`
	Connectors []PortableConnector `json:"connectors"`
}

// toPortable strips a stored Connector down to its portable recipe.
func toPortable(c Connector) PortableConnector {
	return PortableConnector{Name: c.Name, Kind: c.Kind, Desc: c.Desc, Spec: c.Spec}
}

// ExportConnectorPack builds a pack from stored connectors. With no names it
// exports every connector; with names it exports exactly those (erroring on the
// first one that doesn't exist, so a typo is caught rather than silently
// dropped).
func ExportConnectorPack(db Database, names ...string) (ConnectorPack, error) {
	pack := ConnectorPack{Bundle: ConnectorPackBundle, ExportedAt: time.Now()}
	if len(names) == 0 {
		for _, c := range ListConnectors(db) {
			pack.Connectors = append(pack.Connectors, toPortable(c))
		}
		return pack, nil
	}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		c, ok := GetConnector(db, n)
		if !ok {
			return ConnectorPack{}, fmt.Errorf("no connector named %q", n)
		}
		pack.Connectors = append(pack.Connectors, toPortable(c))
	}
	return pack, nil
}

// ParseConnectorPack decodes pack bytes, tolerating three hand-writable shapes:
// a full pack object ({"bundle":...,"connectors":[...]}), a bare array of
// portable connectors, or a single connector object. All normalize to a
// ConnectorPack so callers have one shape to consume.
func ParseConnectorPack(data []byte) (ConnectorPack, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return ConnectorPack{}, Error("empty connector pack")
	}
	switch trimmed[0] {
	case '[':
		var list []PortableConnector
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return ConnectorPack{}, fmt.Errorf("invalid connector array: %w", err)
		}
		return ConnectorPack{Bundle: ConnectorPackBundle, Connectors: list}, nil
	case '{':
		// A pack has a "connectors" key; a single connector does not.
		var probe struct {
			Connectors json.RawMessage `json:"connectors"`
		}
		_ = json.Unmarshal(trimmed, &probe)
		if len(probe.Connectors) > 0 {
			var pack ConnectorPack
			if err := json.Unmarshal(trimmed, &pack); err != nil {
				return ConnectorPack{}, fmt.Errorf("invalid connector pack: %w", err)
			}
			return pack, nil
		}
		var one PortableConnector
		if err := json.Unmarshal(trimmed, &one); err != nil {
			return ConnectorPack{}, fmt.Errorf("invalid connector object: %w", err)
		}
		return ConnectorPack{Bundle: ConnectorPackBundle, Connectors: []PortableConnector{one}}, nil
	}
	return ConnectorPack{}, Error("connector pack must be a JSON object or array")
}

// ConnectorImportSkip records one connector that could not be imported and why,
// so the caller can report a partial import honestly rather than swallow
// failures.
type ConnectorImportSkip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// ConnectorImportResult summarizes an import: what landed, and what didn't.
type ConnectorImportResult struct {
	Imported []string              `json:"imported"`
	Skipped  []ConnectorImportSkip `json:"skipped,omitempty"`
}

// ImportConnectorPack reconstitutes connectors from pack bytes as new records
// owned by owner. Each connector goes through SaveConnector, so GOVERNANCE
// RE-APPLIES on import exactly as on create: remote_mcp / desktop_* land
// UNAPPROVED and inert until an admin approves them; rest_poll auto-approves
// (it reaches out only through an already-governed credential). A name that
// already exists is SKIPPED, never overwritten — import can't clobber a live
// integration. Referenced credentials must exist on this install; a validate
// failure surfaces as a per-connector skip, not a fatal error, so the rest of
// the pack still imports.
func ImportConnectorPack(db Database, data []byte, owner string) (ConnectorImportResult, error) {
	var res ConnectorImportResult
	pack, err := ParseConnectorPack(data)
	if err != nil {
		return res, err
	}
	if len(pack.Connectors) == 0 {
		return res, Error("no connectors in pack")
	}
	for _, pc := range pack.Connectors {
		name := strings.TrimSpace(pc.Name)
		if name == "" {
			res.Skipped = append(res.Skipped, ConnectorImportSkip{Name: "(unnamed)", Reason: "missing name"})
			continue
		}
		if _, exists := GetConnector(db, name); exists {
			res.Skipped = append(res.Skipped, ConnectorImportSkip{Name: name, Reason: "a connector with this name already exists"})
			continue
		}
		c := Connector{
			Name:  name,
			Kind:  strings.TrimSpace(pc.Kind),
			Desc:  pc.Desc,
			Spec:  pc.Spec,
			Owner: owner,
		}
		if err := SaveConnector(db, c); err != nil {
			res.Skipped = append(res.Skipped, ConnectorImportSkip{Name: name, Reason: err.Error()})
			continue
		}
		res.Imported = append(res.Imported, name)
	}
	return res, nil
}
