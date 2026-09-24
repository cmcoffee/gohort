// Artifact bundles: the ONE portable export/import format for every shareable
// gohort artifact — connectors, tools, and (as they register) agents, APIs,
// pipelines. It generalizes the connector pack (core/connector_pack.go) into a
// single envelope carrying a list of TYPED artifacts, so the whole surface has
// one wire format, one import governance rule, and one file the marketplace
// distributes.
//
// THE KEY IDEA: an individual export is just a one-item bundle. "Export this
// tool", "export these three", and "export everything" are the same code path
// at different cardinality — there is no separate per-type pack format.
//
// EXTENSIBILITY comes from the artifact-type registry: each type registers an
// ArtifactType (Type/List/Export/Import). core/artifact_types.go registers
// connectors and tools; adding agents or credentials later is a registration,
// not a new format. The envelope header ({bundle, exported_at}) is exactly the
// one the connector pack already used, so old connector packs still import.
//
// SECRETS NEVER TRAVEL. Every type's recipe references auth by NAME only (a
// SecureAPI credential, a per-user OAuth account); the secret lives in the
// subsystem the artifact materializes into. That is what makes a bundle
// shareable — it carries the SHAPE of a capability, and the importer supplies
// (or drafts) the matching credential on their side.
//
// IMPORT IS ALWAYS A DRAFT. A recipe can carry executable code (a tool's
// command_template / script_body, an agent's rules). So import reconstitutes
// artifacts in each type's REVIEW state — connectors land unapproved, tools
// land in the pending pool — never live. Approval stays the human gate.
package core

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ArtifactClientJS is the shared browser half of the bundle format
// (assets/artifact_client.js): window.gohortArtifacts.download(href, name) and
// window.gohortArtifacts.importFlow({previewURL, importURL, invalidate,
// subtitle, onDone}). A page that exports or imports bundles includes it in
// its head and points it at its own endpoints.
//
//go:embed assets/artifact_client.js
var ArtifactClientJS string

// ArtifactBundleFormat identifies the unified wire format. Bumped only on a
// breaking envelope change; importers accept older minor forms (and the legacy
// gohort.connectors/v1 pack).
const ArtifactBundleFormat = "gohort.bundle/v1"

// PortableArtifact is one typed, identity-free, secret-free recipe. Type selects
// the registered ArtifactType that knows how to reconstitute Recipe; Name is a
// human/label convenience (the recipe carries its own authoritative name).
type PortableArtifact struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Recipe json.RawMessage `json:"recipe"`
}

// ArtifactBundle is the unified export/import envelope. A one-item Artifacts
// slice is an individual export; many items is a bundle. Same shape either way.
type ArtifactBundle struct {
	Bundle     string    `json:"bundle"`
	ExportedAt time.Time `json:"exported_at"`
	// GohortVersion is the version of the install that wrote the bundle. It
	// changes nothing on import by itself — recipes carry their own schema
	// stamps where meaning can drift — but it is the one fact a reader needs
	// when a recipe degrades: "authored on 0.6.4xx" turns a mystery into a
	// diff. Empty on bundles written before it existed.
	GohortVersion string             `json:"gohort_version,omitempty"`
	Artifacts     []PortableArtifact `json:"artifacts"`
}

// ArtifactSel names one artifact to export. Owner scopes per-user types (tools);
// global types (connectors) ignore it.
type ArtifactSel struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Owner string `json:"owner,omitempty"`
}

// ArtifactType is the pluggable interface each portable artifact type registers.
// One implementation per type (connector, tool, …) wraps that type's existing
// store — the registry adds portability without the types knowing about bundles.
type ArtifactType interface {
	// ArtifactType returns the type discriminator ("connector", "tool", …).
	ArtifactType() string
	// ListArtifacts enumerates everything of this type that can be exported,
	// with Owner set for per-user types. Powers "export all" and the future
	// catalog browse.
	ListArtifacts(db Database) []ArtifactSel
	// ExportArtifact produces the portable recipe for one named artifact.
	ExportArtifact(db Database, name, owner string) (json.RawMessage, error)
	// ImportArtifact reconstitutes a DRAFT/PENDING artifact owned by owner.
	// Returns (name, "", nil) on success; (name, reason, nil) to skip (e.g. a
	// live artifact of that name already exists); (_, _, err) on hard failure.
	ImportArtifact(db Database, recipe json.RawMessage, owner string) (string, string, error)
}

// ArtifactDependencies is an OPTIONAL interface an ArtifactType implements when
// its artifacts reference OTHER artifacts by name — a tool referencing its API
// credential, a connector referencing the SecureAPI credential it polls
// through. Export-time closure (ExportArtifactBundle) walks these so selecting
// one artifact folds in everything it needs to install cleanly on a fresh box.
//
// Dependencies is BEST-EFFORT: return the artifacts this one references; the
// exporter resolves each and silently drops any that can't be exported (a
// bootstrap no_auth credential, a reference to something that no longer
// exists). A dependency must never fail an otherwise-valid export — import
// surfaces an unmet reference as a skip.
type ArtifactDependencies interface {
	Dependencies(db Database, name, owner string) []ArtifactSel
}

// ArtifactRecipeDependencies is the recipe-side twin of ArtifactDependencies,
// implemented when a type's references can be extracted from a RECIPE alone —
// no store lookup, because import PREVIEW inspects bundles whose artifacts
// aren't in any store yet. A type should back both interfaces with one shared
// walk over the decoded record so preview and the post-import warning pass
// can never disagree about what an artifact references.
//
// inBundle reports whether the bundle under preview carries an artifact of
// (type, name). It exists for per-user reference filtering: a skill/agent
// allowlist name counts as a portable-tool reference when the tool is on this
// install OR traveling in the same bundle — the store-side walk gets the
// latter for free (the tool has landed by the time it runs), the recipe-side
// walk needs the predicate. May be nil (treat as always-false).
type ArtifactRecipeDependencies interface {
	RecipeDependencies(db Database, recipe json.RawMessage, owner string, inBundle func(typ, name string) bool) []ArtifactSel
}

// artifactDeps returns the declared dependencies of one selection, or nil if
// its type declares none (doesn't implement ArtifactDependencies).
func artifactDeps(db Database, s ArtifactSel) []ArtifactSel {
	at, ok := lookupArtifactType(strings.TrimSpace(s.Type))
	if !ok {
		return nil
	}
	dep, ok := at.(ArtifactDependencies)
	if !ok {
		return nil
	}
	return dep.Dependencies(db, strings.TrimSpace(s.Name), strings.TrimSpace(s.Owner))
}

// exportableCredential reports whether a credential NAME is worth folding into
// a bundle as a dependency. The bootstrap sentinels ("", "none", "no_auth")
// name gohort's built-in open-pattern credential, which exists on every
// install — pulling it in as a dependency is noise, not portability. A caller
// who genuinely wants to ship a custom-scoped no_auth selects it explicitly.
func exportableCredential(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "none", "no_auth":
		return false
	}
	return true
}

// ArtifactRecipeSniffer is an OPTIONAL ArtifactType capability for a type whose
// recipe also circulates as a BARE file, from before the bundle envelope
// existed or from a per-app Export button (an agent's .agent.json, a
// pipeline's .pipeline.json). Given the top-level keys of a JSON object that
// is neither a bundle nor a bare {type, recipe} artifact, the type says
// whether the object is one of its recipes, so ParseArtifactBundle can lift it
// into a one-item bundle instead of falling through to the legacy connector
// parser, which reads any {"name": ...} object as a connector.
//
// Claim only on a key no other type's recipe has.
type ArtifactRecipeSniffer interface {
	SniffsRecipe(fields map[string]json.RawMessage) bool
}

// ArtifactUserImportable is an OPTIONAL ArtifactType capability: a type that
// returns true may be imported by an ordinary user into their OWN namespace.
// Every such type's import must land inside the importer's reach and inert
// (drafts, disabled, pending review). A type without it (connectors,
// credentials, source hooks: deployment-wide records) is admin-only.
type ArtifactUserImportable interface {
	UserImportable() bool
}

// ArtifactTypeUserImportable reports whether an ordinary user may import
// artifacts of the named type (see ArtifactUserImportable). Unknown types are
// not importable by anyone, so they read false.
func ArtifactTypeUserImportable(typ string) bool {
	at, ok := lookupArtifactType(typ)
	if !ok {
		return false
	}
	ui, ok := at.(ArtifactUserImportable)
	return ok && ui.UserImportable()
}

// adminOnlyArtifactDetail is the skip reason for a type an ordinary user's
// import may not bring in.
const adminOnlyArtifactDetail = "only an administrator can import this kind of artifact"

var artifactTypes = map[string]ArtifactType{}

// RegisterArtifactType adds a portable artifact type to the registry. Called
// from init() in core/artifact_types.go. Last registration for a given type
// name wins (types are distinct in practice).
func RegisterArtifactType(t ArtifactType) {
	if t == nil {
		return
	}
	artifactTypes[strings.TrimSpace(t.ArtifactType())] = t
}

func lookupArtifactType(name string) (ArtifactType, bool) {
	t, ok := artifactTypes[strings.TrimSpace(name)]
	return t, ok
}

// ExportArtifactBundle builds a bundle from an explicit selection, then folds in
// the transitive DEPENDENCY CLOSURE so a selected artifact travels with the
// credentials (and other artifacts) it references — "export this tool" produces
// a bundle that installs cleanly, credential and all, on a fresh box.
//
// Two tiers, different failure modes:
//   - The EXPLICIT selection is strict: an unknown type or missing artifact
//     errors on the first bad selector, so a typo is caught rather than
//     silently dropped (mirrors ExportConnectorPack).
//   - DEPENDENCIES are best-effort: a reference that can't be resolved (a
//     bootstrap credential, something since deleted) is skipped, never fatal.
//     A bad dependency must not sink an otherwise-valid export; import reports
//     the unmet reference.
//
// Every artifact lands at most once (dedup by type+name+owner), so this is
// idempotent — "export all" already contains every dependency and the closure
// is a no-op over it. Dependency waves are sorted for byte-stable output.
func ExportArtifactBundle(db Database, sels []ArtifactSel) (ArtifactBundle, error) {
	return exportArtifactBundle(db, sels, true, nil)
}

// ExportArtifactBundleAsUser is the export an ordinary user may run: only
// their OWN artifacts, of the kinds they may import (ArtifactUserImportable).
// Every selector is resolved in owner's namespace whatever Owner it named, and
// a type outside the user's kinds is an error, not a silent drop. The
// dependency closure is held to the same line: a dependency of a user kind is
// resolved in owner's namespace (one that belongs to somebody else, a tool
// shared with them say, does not resolve there and is left out), and a
// deployment-wide dependency (a credential's configuration, a connector) is
// left out entirely. Those are an administrator's to export; the import on
// the other side warns that the reference is missing.
func ExportArtifactBundleAsUser(db Database, owner string, sels []ArtifactSel, includeDeps bool) (ArtifactBundle, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return ArtifactBundle{}, Error("an export needs a signed-in user")
	}
	own := make([]ArtifactSel, 0, len(sels))
	for _, s := range sels {
		if !ArtifactTypeUserImportable(s.Type) {
			return ArtifactBundle{}, fmt.Errorf("%q artifacts are exported by an administrator", strings.TrimSpace(s.Type))
		}
		s.Owner = owner
		own = append(own, s)
	}
	return exportArtifactBundle(db, own, includeDeps, func(dep ArtifactSel) (ArtifactSel, bool) {
		if !ArtifactTypeUserImportable(dep.Type) {
			return dep, false
		}
		dep.Owner = owner
		return dep, true
	})
}

// ArtifactSelectionForOwner is "everything this user owns": every artifact of
// a user kind whose Owner is owner, sorted as ArtifactSelectionForTypes sorts.
// The account-level backup starts from it.
func ArtifactSelectionForOwner(db Database, owner string) []ArtifactSel {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil
	}
	var types []string
	for name := range artifactTypes {
		if ArtifactTypeUserImportable(name) {
			types = append(types, name)
		}
	}
	if len(types) == 0 {
		return nil
	}
	var mine []ArtifactSel
	for _, s := range ArtifactSelectionForTypes(db, types...) {
		if strings.TrimSpace(s.Owner) == owner {
			mine = append(mine, s)
		}
	}
	return mine
}

// ExportArtifactBundleShallow exports EXACTLY the selection with no dependency
// closure — the bare artifacts. For the caller who knows the target install
// already has the referenced credentials/tools (the "Include dependencies"
// opt-out in the export UI). The explicit selection is still strict: a typo
// errors, same as the closure path.
func ExportArtifactBundleShallow(db Database, sels []ArtifactSel) (ArtifactBundle, error) {
	return exportArtifactBundle(db, sels, false, nil)
}

// exportArtifactBundle is the shared body: it always exports the explicit
// selection strictly, and walks the transitive dependency closure only when
// includeDeps is set. depFilter, when set, rewrites or drops each dependency
// before it is resolved (false = leave it out).
func exportArtifactBundle(db Database, sels []ArtifactSel, includeDeps bool, depFilter func(ArtifactSel) (ArtifactSel, bool)) (ArtifactBundle, error) {
	bundle := ArtifactBundle{Bundle: ArtifactBundleFormat, ExportedAt: time.Now(), GohortVersion: AppVersion}
	seen := map[string]bool{}
	selKey := func(s ArtifactSel) string {
		return strings.TrimSpace(s.Type) + "\x00" + strings.TrimSpace(s.Name) + "\x00" + strings.TrimSpace(s.Owner)
	}
	// addArtifact exports one selection and appends it. strict=true (explicit
	// selection) propagates a resolution failure as an error; strict=false
	// (dependency) skips it. Returns whether a new artifact was actually added,
	// so the caller only chases the deps of things that made it into the bundle.
	addArtifact := func(s ArtifactSel, strict bool) (bool, error) {
		if seen[selKey(s)] {
			return false, nil
		}
		typ := strings.TrimSpace(s.Type)
		name := strings.TrimSpace(s.Name)
		at, ok := lookupArtifactType(typ)
		if !ok {
			if strict {
				return false, fmt.Errorf("unknown artifact type %q", typ)
			}
			return false, nil
		}
		recipe, err := at.ExportArtifact(db, name, strings.TrimSpace(s.Owner))
		if err != nil {
			if strict {
				return false, fmt.Errorf("export %s %q: %w", typ, name, err)
			}
			return false, nil
		}
		seen[selKey(s)] = true
		bundle.Artifacts = append(bundle.Artifacts, PortableArtifact{Type: typ, Name: name, Recipe: recipe})
		return true, nil
	}

	// Explicit selection first, in caller order, strict. Collect each added
	// artifact's declared dependencies to seed the closure (skipped entirely
	// when includeDeps is false — a bare export of exactly what was asked for).
	var pending []ArtifactSel
	for _, s := range sels {
		added, err := addArtifact(s, true)
		if err != nil {
			return ArtifactBundle{}, err
		}
		if added && includeDeps {
			pending = append(pending, artifactDeps(db, s)...)
		}
	}

	// Breadth-first closure over dependencies. seen[] both dedups and breaks any
	// reference cycle. Each wave is sorted (type, owner, name) so the bundle is
	// deterministic byte-for-byte given the same store.
	for len(pending) > 0 {
		wave := pending
		pending = nil
		sort.SliceStable(wave, func(i, j int) bool {
			if wave[i].Type != wave[j].Type {
				return wave[i].Type < wave[j].Type
			}
			if wave[i].Owner != wave[j].Owner {
				return wave[i].Owner < wave[j].Owner
			}
			return wave[i].Name < wave[j].Name
		})
		for _, s := range wave {
			if depFilter != nil {
				var keep bool
				if s, keep = depFilter(s); !keep {
					continue
				}
			}
			added, _ := addArtifact(s, false)
			if added {
				pending = append(pending, artifactDeps(db, s)...)
			}
		}
	}
	return bundle, nil
}

// ArtifactSelectionForTypes returns the selection "export all" would carry —
// every registered artifact, optionally restricted to the named types (empty =
// all types) — sorted (type, then owner, then name) so exports are
// deterministic byte-for-byte given the same store. Exposed so a caller can
// build the same selection and then choose whether to apply dependency closure
// (ExportArtifactBundle vs ExportArtifactBundleShallow).
func ArtifactSelectionForTypes(db Database, types ...string) []ArtifactSel {
	only := map[string]bool{}
	for _, t := range types {
		if t = strings.TrimSpace(t); t != "" {
			only[t] = true
		}
	}
	var sels []ArtifactSel
	for name, at := range artifactTypes {
		if len(only) > 0 && !only[name] {
			continue
		}
		sels = append(sels, at.ListArtifacts(db)...)
	}
	sort.SliceStable(sels, func(i, j int) bool {
		if sels[i].Type != sels[j].Type {
			return sels[i].Type < sels[j].Type
		}
		if sels[i].Owner != sels[j].Owner {
			return sels[i].Owner < sels[j].Owner
		}
		return sels[i].Name < sels[j].Name
	})
	return sels
}

// ExportAllArtifacts builds a bundle of every registered artifact, optionally
// restricted to the named types (empty = all types), WITH dependency closure —
// though for a whole-store export the closure is a no-op (every dependency is
// already in the selection).
func ExportAllArtifacts(db Database, types ...string) (ArtifactBundle, error) {
	return ExportArtifactBundle(db, ArtifactSelectionForTypes(db, types...))
}

// ParseArtifactBundle decodes bundle bytes, tolerating several shapes so both
// this format and the legacy connector pack import through one path:
//   - a full unified bundle ({"bundle":"gohort.bundle/v1","artifacts":[...]})
//   - a bare single artifact ({"type":...,"name":...,"recipe":{...}})
//   - a legacy connector pack / bare connector(s) — lifted into connector
//     artifacts (back-compat with everything exported before this format)
func ParseArtifactBundle(data []byte) (ArtifactBundle, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return ArtifactBundle{}, Error("empty artifact bundle")
	}
	var probe struct {
		Bundle     string          `json:"bundle"`
		Artifacts  json.RawMessage `json:"artifacts"`
		Type       string          `json:"type"`
		Recipe     json.RawMessage `json:"recipe"`
		Connectors json.RawMessage `json:"connectors"`
	}
	if trimmed[0] == '{' {
		_ = json.Unmarshal(trimmed, &probe)
	}
	// New unified bundle.
	if probe.Bundle == ArtifactBundleFormat || len(probe.Artifacts) > 0 {
		var b ArtifactBundle
		if err := json.Unmarshal(trimmed, &b); err != nil {
			return ArtifactBundle{}, fmt.Errorf("invalid artifact bundle: %w", err)
		}
		if strings.TrimSpace(b.Bundle) == "" {
			b.Bundle = ArtifactBundleFormat
		}
		return b, nil
	}
	// Bare single artifact (hand-writable individual export).
	if strings.TrimSpace(probe.Type) != "" && len(probe.Recipe) > 0 {
		var one PortableArtifact
		if err := json.Unmarshal(trimmed, &one); err != nil {
			return ArtifactBundle{}, fmt.Errorf("invalid artifact: %w", err)
		}
		return ArtifactBundle{Bundle: ArtifactBundleFormat, Artifacts: []PortableArtifact{one}}, nil
	}
	if trimmed[0] == '{' {
		var fields map[string]json.RawMessage
		if json.Unmarshal(trimmed, &fields) == nil {
			// A form's file field posts the file's TEXT as a string under its
			// field name: {"recipe": "<file>"} or {"pack": "<file>"}. Unwrap
			// and parse what the file says.
			if inner, ok := uploadWrappedText(fields); ok {
				return ParseArtifactBundle([]byte(inner))
			}
			if typ, ok := sniffArtifactType(fields); ok {
				one := PortableArtifact{Type: typ, Recipe: json.RawMessage(trimmed)}
				one.Name = artifactRecipeName(one)
				return ArtifactBundle{Bundle: ArtifactBundleFormat, Artifacts: []PortableArtifact{one}}, nil
			}
		}
	}
	// Legacy connector pack / bare connector(s): reuse the connector parser and
	// lift each into a connector artifact so old files keep importing.
	pack, err := ParseConnectorPack(trimmed)
	if err != nil {
		return ArtifactBundle{}, err
	}
	b := ArtifactBundle{Bundle: ArtifactBundleFormat, ExportedAt: pack.ExportedAt}
	for _, pc := range pack.Connectors {
		recipe, mErr := json.Marshal(pc)
		if mErr != nil {
			continue
		}
		b.Artifacts = append(b.Artifacts, PortableArtifact{Type: "connector", Name: pc.Name, Recipe: recipe})
	}
	return b, nil
}

// IsArtifactEnvelope reports whether data (after any upload wrapper) is a
// bundle envelope or a bare {type, recipe} artifact, as opposed to a bare
// recipe of one type. A per-type import door uses it to hand envelopes to the
// bundle importer and keep its own handling for the bare files it has always
// taken.
func IsArtifactEnvelope(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(trimmed, &fields) != nil {
		return false
	}
	if inner, ok := uploadWrappedText(fields); ok {
		return IsArtifactEnvelope([]byte(inner))
	}
	var probe struct {
		Bundle    string          `json:"bundle"`
		Artifacts json.RawMessage `json:"artifacts"`
		Type      string          `json:"type"`
		Recipe    json.RawMessage `json:"recipe"`
	}
	_ = json.Unmarshal(trimmed, &probe)
	return probe.Bundle == ArtifactBundleFormat || len(probe.Artifacts) > 0 ||
		(strings.TrimSpace(probe.Type) != "" && len(probe.Recipe) > 0)
}

// UnwrapArtifactUpload returns the file text inside a form upload wrapper
// ({"recipe": "<text>"} or {"pack": "<text>"}), or data unchanged when it is
// not one.
func UnwrapArtifactUpload(data []byte) []byte {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return data
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(trimmed, &fields) != nil {
		return data
	}
	if inner, ok := uploadWrappedText(fields); ok {
		return UnwrapArtifactUpload([]byte(inner))
	}
	return data
}

// uploadWrappedText returns the file text when fields is exactly an upload
// wrapper: one key, "recipe" or "pack", holding a JSON STRING. A {"recipe": {...}}
// object is a bare artifact's recipe, not a wrapper, and is left alone.
func uploadWrappedText(fields map[string]json.RawMessage) (string, bool) {
	if len(fields) != 1 {
		return "", false
	}
	for _, key := range []string{"recipe", "pack"} {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		var text string
		if json.Unmarshal(raw, &text) != nil || strings.TrimSpace(text) == "" {
			return "", false
		}
		return text, true
	}
	return "", false
}

// sniffArtifactType asks each registered ArtifactRecipeSniffer, in type-name
// order so the answer never depends on map iteration, whether fields is one of
// its bare recipes.
func sniffArtifactType(fields map[string]json.RawMessage) (string, bool) {
	names := make([]string, 0, len(artifactTypes))
	for n := range artifactTypes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if sn, ok := artifactTypes[n].(ArtifactRecipeSniffer); ok && sn.SniffsRecipe(fields) {
			return n, true
		}
	}
	return "", false
}

// ArtifactImportOutcome records what happened to one artifact in an import.
// Warnings are non-fatal: the artifact DID import, but a reference it needs (an
// API credential, a tool an agent allowlists) isn't present on this install, so
// it won't function until the admin supplies it.
type ArtifactImportOutcome struct {
	Type     string   `json:"type"`
	Name     string   `json:"name"`
	Status   string   `json:"status"` // "imported" | "skipped"
	Detail   string   `json:"detail,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// ArtifactImportResult summarizes a bundle import: per-artifact outcomes plus
// running counts, so the caller can report a partial import honestly. Warnings
// aggregates every outcome's warnings (each already artifact-qualified) for a
// flat top-level display.
type ArtifactImportResult struct {
	Bundle string `json:"bundle"`
	// GohortVersion echoes the bundle's stamp so a report can say where the
	// recipes came from next to what happened to them.
	GohortVersion string                  `json:"gohort_version,omitempty"`
	Imported      int                     `json:"imported"`
	Skipped       int                     `json:"skipped"`
	Outcomes      []ArtifactImportOutcome `json:"outcomes"`
	Warnings      []string                `json:"warnings,omitempty"`
}

// Summary renders a one-glance human summary of an import: the counts, then any
// unmet-dependency warnings each on their own line. Suitable for a form's
// post-submit message. Returns "" for a nil/empty result.
func (r ArtifactImportResult) Summary() string {
	if r.Imported == 0 && r.Skipped == 0 && len(r.Warnings) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Imported %d, skipped %d.", r.Imported, r.Skipped)
	if v := strings.TrimSpace(r.GohortVersion); v != "" && v != AppVersion {
		fmt.Fprintf(&b, " Bundle exported by gohort %s (this install is %s).", v, AppVersion)
	}
	for _, w := range r.Warnings {
		b.WriteString("\nWarning: ")
		b.WriteString(w)
	}
	return b.String()
}

// artifactExists reports whether an artifact selection resolves on this install
// — the existence probe used to tell a satisfied dependency from a missing one.
// It reuses the type's ExportArtifact (which errors when the artifact isn't
// found) so no separate lookup path can drift from what export/import see.
func artifactExists(db Database, s ArtifactSel) bool {
	at, ok := lookupArtifactType(strings.TrimSpace(s.Type))
	if !ok {
		return false
	}
	_, err := at.ExportArtifact(db, strings.TrimSpace(s.Name), strings.TrimSpace(s.Owner))
	return err == nil
}

// ImportArtifactBundle reconstitutes every artifact in a bundle as owner's DRAFT
// (each type's review state — connectors unapproved, tools pending). One
// artifact's failure never aborts the rest: an unknown type or a per-type
// import error surfaces as a skip with a reason.
//
// After importing, a second pass WARNS about unmet dependencies: for each
// imported artifact it resolves the references the artifact declares (a tool's
// API credential, an agent's allowlisted tools) and checks each is present on
// this install — brought in by this same bundle or already here. A reference
// that resolves to nothing is a per-outcome warning (also aggregated on the
// result): the artifact imported, but won't function until the admin supplies
// the missing piece. The check runs AFTER all imports so a credential bundled
// alongside the tool that needs it doesn't false-warn.
func ImportArtifactBundle(db Database, data []byte, owner string) (ArtifactImportResult, error) {
	return importArtifactBundle(db, data, owner, false)
}

// ImportArtifactBundleAsUser is ImportArtifactBundle for an ordinary user's
// own namespace: only ArtifactUserImportable types import, and every other
// artifact in the bundle is reported as skipped rather than silently dropped,
// so the person can see what needs an administrator.
func ImportArtifactBundleAsUser(db Database, data []byte, owner string) (ArtifactImportResult, error) {
	return importArtifactBundle(db, data, owner, true)
}

func importArtifactBundle(db Database, data []byte, owner string, userOnly bool) (ArtifactImportResult, error) {
	var res ArtifactImportResult
	bundle, err := ParseArtifactBundle(data)
	if err != nil {
		return res, err
	}
	res.Bundle = bundle.Bundle
	res.GohortVersion = bundle.GohortVersion
	if len(bundle.Artifacts) == 0 {
		return res, Error("no artifacts in bundle")
	}
	// imported tracks the selection of each artifact that actually landed, so the
	// dependency pass knows what to inspect (and can resolve it from the store).
	var imported []ArtifactSel
	for _, a := range bundle.Artifacts {
		typ := strings.TrimSpace(a.Type)
		at, ok := lookupArtifactType(typ)
		if !ok {
			res.Outcomes = append(res.Outcomes, ArtifactImportOutcome{
				Type: typ, Name: a.Name, Status: "skipped", Detail: "unknown artifact type"})
			res.Skipped++
			continue
		}
		if userOnly && !ArtifactTypeUserImportable(typ) {
			res.Outcomes = append(res.Outcomes, ArtifactImportOutcome{
				Type: typ, Name: artifactRecipeName(a), Status: "skipped", Detail: adminOnlyArtifactDetail})
			res.Skipped++
			continue
		}
		name, skip, ierr := at.ImportArtifact(db, a.Recipe, owner)
		if strings.TrimSpace(name) == "" {
			name = a.Name
		}
		switch {
		case ierr != nil:
			res.Outcomes = append(res.Outcomes, ArtifactImportOutcome{
				Type: typ, Name: name, Status: "skipped", Detail: ierr.Error()})
			res.Skipped++
		case skip != "":
			res.Outcomes = append(res.Outcomes, ArtifactImportOutcome{
				Type: typ, Name: name, Status: "skipped", Detail: skip})
			res.Skipped++
		default:
			res.Outcomes = append(res.Outcomes, ArtifactImportOutcome{
				Type: typ, Name: name, Status: "imported"})
			res.Imported++
			imported = append(imported, ArtifactSel{Type: typ, Name: name, Owner: owner})
		}
	}
	warnMissingDependencies(db, &res, imported)
	return res, nil
}

// warnMissingDependencies is the post-import pass: for every imported artifact
// it resolves declared references and flags any that aren't present on the
// install. Warnings attach to the matching outcome and to the aggregated list.
func warnMissingDependencies(db Database, res *ArtifactImportResult, imported []ArtifactSel) {
	// Index outcomes by the imported selection so a warning lands on the right
	// row. Only "imported" outcomes are in `imported`, so keys are unique.
	outcomeAt := map[string]int{}
	for i, o := range res.Outcomes {
		if o.Status == "imported" {
			outcomeAt[o.Type+"\x00"+o.Name] = i
		}
	}
	for _, s := range imported {
		for _, dep := range artifactDeps(db, s) {
			if artifactExists(db, dep) {
				continue
			}
			msg := missingDepWarning(s, dep)
			res.Warnings = append(res.Warnings, msg)
			if i, ok := outcomeAt[s.Type+"\x00"+s.Name]; ok {
				res.Outcomes[i].Warnings = append(res.Outcomes[i].Warnings, msg)
			}
		}
	}
}

// missingDepWarning is the ONE unmet-reference message, shared by the
// post-import warning pass and the import preview so a preview reads
// identically to the import result it predicts.
func missingDepWarning(s, dep ArtifactSel) string {
	return fmt.Sprintf("%s %q references %s %q, which isn't present on this install: add it before this %s can be used.",
		s.Type, s.Name, dep.Type, dep.Name, s.Type)
}

// ArtifactPreviewItem is one artifact's predicted import outcome. Action is
// "import" (would land as a draft) or "skip" (with the reason in Detail);
// Warnings carries unmet references, same shape as the import result's.
type ArtifactPreviewItem struct {
	Type     string   `json:"type"`
	Name     string   `json:"name"`
	Action   string   `json:"action"` // "import" | "skip"
	Detail   string   `json:"detail,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// ArtifactPreviewResult is the dry-run summary of a bundle: per-artifact
// predictions plus running counts and the aggregated warning list — the
// preview twin of ArtifactImportResult.
type ArtifactPreviewResult struct {
	Bundle      string                `json:"bundle"`
	ExportedAt  time.Time             `json:"exported_at"`
	WouldImport int                   `json:"would_import"`
	WouldSkip   int                   `json:"would_skip"`
	Items       []ArtifactPreviewItem `json:"items"`
	Warnings    []string              `json:"warnings,omitempty"`
}

// PreviewArtifactBundle is the DRY-RUN twin of ImportArtifactBundle: it parses
// bundle bytes and predicts — writing nothing — what import would do. Which
// artifacts land as drafts, which skip (unknown type / missing name / a name
// already present), and which declared references are unmet (in neither the
// bundle nor this install). Predictions reuse import's own machinery —
// artifactExists for presence, ArtifactRecipeDependencies for references,
// missingDepWarning for the message — so a clean preview reads identically to
// the import result it predicts. One deliberate approximation: a tool whose
// name matches only a PENDING draft previews as a skip, while import actually
// replaces that draft in place.
func PreviewArtifactBundle(db Database, data []byte, owner string) (ArtifactPreviewResult, error) {
	return previewArtifactBundle(db, data, owner, false)
}

// PreviewArtifactBundleAsUser is the dry-run twin of ImportArtifactBundleAsUser.
func PreviewArtifactBundleAsUser(db Database, data []byte, owner string) (ArtifactPreviewResult, error) {
	return previewArtifactBundle(db, data, owner, true)
}

func previewArtifactBundle(db Database, data []byte, owner string, userOnly bool) (ArtifactPreviewResult, error) {
	var res ArtifactPreviewResult
	bundle, err := ParseArtifactBundle(data)
	if err != nil {
		return res, err
	}
	res.Bundle = bundle.Bundle
	res.ExportedAt = bundle.ExportedAt
	if len(bundle.Artifacts) == 0 {
		return res, Error("no artifacts in bundle")
	}
	// Index what the bundle itself carries so an in-bundle reference never
	// reads as missing — the same reason the post-import warning pass runs
	// only after every artifact has landed. Indexed by recipe name AND by
	// traveled recipe ID: cross-artifact references can be either (a skill's
	// AttachedCollections and an agent's AttachedPipelines are IDs).
	carried := map[string]bool{}
	for _, a := range bundle.Artifacts {
		typ := strings.TrimSpace(a.Type)
		carried[typ+"\x00"+artifactRecipeName(a)] = true
		if id := artifactRecipeID(a); id != "" {
			carried[typ+"\x00"+id] = true
		}
	}
	inBundle := func(typ, name string) bool {
		return carried[strings.TrimSpace(typ)+"\x00"+strings.TrimSpace(name)]
	}
	for _, a := range bundle.Artifacts {
		typ := strings.TrimSpace(a.Type)
		name := artifactRecipeName(a)
		item := ArtifactPreviewItem{Type: typ, Name: name}
		at, known := lookupArtifactType(typ)
		switch {
		case !known:
			item.Action, item.Detail = "skip", "unknown artifact type"
		case userOnly && !ArtifactTypeUserImportable(typ):
			item.Action, item.Detail = "skip", adminOnlyArtifactDetail
		case name == "":
			item.Action, item.Detail = "skip", "missing artifact name"
		case artifactExists(db, ArtifactSel{Type: typ, Name: name, Owner: owner}):
			item.Action, item.Detail = "skip", "an artifact with this name already exists on this install"
		default:
			item.Action = "import"
			// Unmet references, mirroring warnMissingDependencies: only for
			// artifacts that would import, satisfied by the bundle or the
			// install, one shared message shape.
			if rd, ok := at.(ArtifactRecipeDependencies); ok {
				self := ArtifactSel{Type: typ, Name: name, Owner: owner}
				for _, dep := range rd.RecipeDependencies(db, a.Recipe, owner, inBundle) {
					if inBundle(dep.Type, dep.Name) || artifactExists(db, dep) {
						continue
					}
					msg := missingDepWarning(self, dep)
					item.Warnings = append(item.Warnings, msg)
					res.Warnings = append(res.Warnings, msg)
				}
			}
		}
		if item.Action == "import" {
			res.WouldImport++
		} else {
			res.WouldSkip++
		}
		res.Items = append(res.Items, item)
	}
	return res, nil
}

// artifactRecipeName returns the authoritative name for one bundle artifact:
// the recipe's own "name" field when present (every registered type's recipe
// carries one), falling back to the envelope's convenience label.
func artifactRecipeName(a PortableArtifact) string {
	var probe struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(a.Recipe, &probe) == nil && strings.TrimSpace(probe.Name) != "" {
		return strings.TrimSpace(probe.Name)
	}
	return strings.TrimSpace(a.Name)
}

// artifactRecipeID returns a recipe's traveled "id", set only by types whose
// identity travels because it is the cross-artifact reference key (collections,
// pipelines); every other type strips identity and yields "".
func artifactRecipeID(a PortableArtifact) string {
	var probe struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(a.Recipe, &probe) != nil {
		return ""
	}
	return strings.TrimSpace(probe.ID)
}
