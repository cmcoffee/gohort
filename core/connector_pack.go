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
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	DisplayName string          `json:"display_name,omitempty"`
	Desc        string          `json:"desc,omitempty"`
	Template    string          `json:"template,omitempty"` // authoring template — travels so import can offer Configure
	Spec        json.RawMessage `json:"spec,omitempty"`
	// Settings are the values a connection keeps for what its spec declares
	// (its address, its model), never a secret: an import keeps those its
	// spec takes, and says why it left any out.
	Settings map[string]string `json:"settings,omitempty"`
}

// ConnectorPack is a portable bundle of one or more connectors.
type ConnectorPack struct {
	Bundle     string              `json:"bundle"`
	ExportedAt time.Time           `json:"exported_at"`
	Connectors []PortableConnector `json:"connectors"`
}

// toPortable strips a stored Connector down to its portable recipe, with the
// settings it keeps in db.
func toPortable(db Database, c Connector) PortableConnector {
	return PortableConnector{Name: c.Name, Kind: c.Kind, DisplayName: c.DisplayName, Desc: c.Desc, Template: c.Template, Spec: c.Spec,
		Settings: ConnectionConfig(db, c)}
}

// settingsImporter is a connector kind that checks the settings an import
// brings against its spec: those it takes, and why each other is left out.
// It changes nothing, so asking again gives the same answer.
type settingsImporter interface {
	ImportSettings(c Connector, values map[string]string) (keep map[string]string, dropped []string)
}

// importConnector saves pc as a draft owned by owner, shown by its display
// name unless another connector is shown so already, and keeps the settings
// it brought that its spec takes.
func importConnector(db Database, pc PortableConnector, owner string) error {
	name := strings.TrimSpace(pc.Name)
	c := Connector{
		Name:        name,
		Kind:        strings.TrimSpace(pc.Kind),
		Desc:        pc.Desc,
		DisplayName: FreeDisplayName(db, name, pc.DisplayName),
		Template:    strings.TrimSpace(pc.Template),
		Spec:        pc.Spec,
		Owner:       owner,
	}
	if err := saveConnectorDraft(db, c); err != nil {
		return err
	}
	if keep, _ := importedSettings(db, pc); len(keep) > 0 {
		saved, _ := GetConnector(db, name)
		return SetConnectionConfig(db, saved, keep)
	}
	return nil
}

// importedSettings is what an imported pc keeps of the settings it brought,
// and why it left any out: none for a kind that keeps no settings.
func importedSettings(db Database, pc PortableConnector) (map[string]string, []string) {
	if len(pc.Settings) == 0 {
		return nil, nil
	}
	saved, ok := GetConnector(db, strings.TrimSpace(pc.Name))
	if !ok {
		return nil, nil
	}
	h, _ := ConnectorHandlerFor(saved.Kind)
	si, ok := h.(settingsImporter)
	if !ok {
		return nil, []string{"its settings were not carried: a " + saved.Kind + " connector keeps none"}
	}
	return si.ImportSettings(saved, pc.Settings)
}

// ExportConnectorPack builds a pack from stored connectors. With no names it
// exports every connector; with names it exports exactly those (erroring on the
// first one that doesn't exist, so a typo is caught rather than silently
// dropped).
func ExportConnectorPack(db Database, names ...string) (ConnectorPack, error) {
	pack := ConnectorPack{Bundle: ConnectorPackBundle, ExportedAt: time.Now()}
	if len(names) == 0 {
		for _, c := range ListConnectors(db) {
			pack.Connectors = append(pack.Connectors, toPortable(db, c))
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
		pack.Connectors = append(pack.Connectors, toPortable(db, c))
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
	// Notes say what an imported connector left out of what it brought.
	Notes []string `json:"notes,omitempty"`
}

// ImportConnectorPack reconstitutes connectors from pack bytes as new records
// owned by owner. Every connector lands UNAPPROVED and inert until an admin
// approves it (saveConnectorDraft), including kinds that auto-approve on
// create: an imported recipe's target was chosen somewhere else. A name that
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
		if err := importConnector(db, pc, owner); err != nil {
			res.Skipped = append(res.Skipped, ConnectorImportSkip{Name: name, Reason: err.Error()})
			continue
		}
		res.Imported = append(res.Imported, name)
		if _, dropped := importedSettings(db, pc); len(dropped) > 0 {
			res.Notes = append(res.Notes, name+": "+strings.Join(dropped, "; "))
		}
	}
	return res, nil
}
