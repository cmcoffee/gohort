package service

// Connectors made before a connection kept its own settings and key move
// onto connections, once, at startup (connections:v2, on Admin >
// Migrations; v1 ran before it knew the connectors the settings of this
// instance's own servers were moved onto, and Bedrock's region; every step
// is safe to run again, so v2 runs where v1 did):
//
//   - an outdated connector (its key in a vault credential, or its address
//     and model written into its spec) with a connection gohort provides in
//     its place hands it its address, model and key, the pages naming it
//     name that one instead, and it goes;
//   - one with no provided connection in its place is made again where it
//     is, from its template's spec now (or with its credential made a secret
//     setting, when no template made it), keeping its name and use;
//   - Embeddings, Transcription and Speech still set to this instance's own
//     server (before connectors) name the local server connection gohort
//     provides, at the address and with the model they had: embeddings keep
//     their space, so nothing is re-embedded;
//   - a vault credential only those connectors used, and nothing else
//     names, is deleted.
//
// Run once the provided connections are installed. Delete this once the
// deployments that predate connections have run it.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/recipes"
)

// MigrateConnections moves what predates connections onto them, once.
func MigrateConnections(db Database) {
	NewMigrationRunner("core", "").Once("connections:v2", func() int {
		return migrateConnections(db)
	})
	NewMigrationRunner("core", "").Once("connections:v3", func() int {
		return nameChoicesForJobs(db)
	})
	NewMigrationRunner("core", "").Once("images:v1", func() int {
		return comfyToService(db)
	})
}

// comfyToService moves each ComfyUI backend kept as a rest_image connector
// onto a service connection running its workflow (comfyui.go): the same
// name, so the pages and agents naming it still do, the same use and
// template, and its key moved from the credential it was sent with into the
// connection, that credential deleted when nothing else names it. One whose
// spec does not translate stays as it is, and the log says why. Other
// rest_image connectors (Automatic1111, a peer's renderer) stay too.
func comfyToService(db Database) int {
	if db == nil {
		return 0
	}
	m := &connMove{db: db, creds: map[string]bool{}}
	for _, c := range ListConnectors(db) {
		if c.Kind != RestImageConnectorKind {
			continue
		}
		var r RestImageSpec
		if json.Unmarshal(c.Spec, &r) != nil || strings.TrimSpace(r.ComfyWorkflow) == "" {
			continue
		}
		s, _, err := fromComfy(r)
		if err != nil {
			Log("[migration] the ComfyUI backend %s stays a rest_image connector: %v", c.Name, err)
			continue
		}
		raw, err := json.Marshal(s)
		if err != nil {
			continue
		}
		cred := strings.TrimSpace(r.Credential)
		if cred == "no_auth" || cred == "none" {
			cred = ""
		}
		c.Kind, c.Spec = Kind, raw
		if c.Template == "" {
			c.Template = "comfyui"
		}
		if err := SaveConnector(db, c); err != nil {
			Log("[migration] the ComfyUI backend %s could not move onto a connection: %v", c.Name, err)
			continue
		}
		m.changed++
		if cred != "" {
			m.creds[cred] = true
			c, _ = GetConnector(db, c.Name)
			m.keep(c, nil, cred)
		}
		Log("[migration] the ComfyUI backend %s is now a connection, keeping its workflow, its use and its key", c.Name)
	}
	m.dropCredentials()
	if m.changed > 0 {
		Settle(db)
	}
	return m.changed
}

// providerPages are the settings that choose a connection by a single value,
// and the job each has it do.
var providerPages = []struct{ table, field, job string }{
	{SearchTable, "provider", media.JobSearch}, {MailTable, "provider", media.JobMail}, {BrowseTable, "source", media.JobRender},
}

// nameChoicesForJobs rewrites each setting that names a connection
// "service:<name>" to name it for the job it has the connection do
// (llm:anthropic, transcribe:local-transcription). An embedding config keeps
// its endpoint, which names the space its vectors are in: only the provider
// it names changes.
func nameChoicesForJobs(db Database) int {
	changed := 0
	for _, p := range providerPages {
		var v string
		if db.Get(p.table, p.field, &v) {
			if next := media.AsJob(p.job, v); next != v {
				db.Set(p.table, p.field, next)
				changed++
			}
		}
	}
	var emb EmbeddingConfig
	if db.Get(EmbeddingTable, "current", &emb) {
		if next := media.AsJob(media.JobEmbed, emb.Provider); next != emb.Provider {
			emb.Provider = next
			db.Set(EmbeddingTable, "current", emb)
			SetEmbeddingConfig(emb)
			changed++
		}
	}
	var tr media.TranscribeConfig
	if db.Get(TranscribeTable, "current", &tr) {
		if next := media.AsJob(media.JobTranscribe, tr.Provider); next != tr.Provider {
			tr.Provider = next
			db.Set(TranscribeTable, "current", tr)
			SetTranscribeConfig(tr)
			changed++
		}
	}
	var sp media.SpeechConfig
	if db.Get(media.SpeechTable, "current", &sp) {
		if next := media.AsJob(media.JobSpeak, sp.Provider); next != sp.Provider {
			sp.Provider = next
			db.Set(media.SpeechTable, "current", sp)
			media.SetSpeechConfig(sp)
			changed++
		}
	}
	if changed > 0 {
		Log("[migration] %d setting(s) now name their connection for the job they give it", changed)
	}
	return changed
}

// connMove is one run of the move: the credentials the moved connectors
// sent their keys with, and how many records it changed.
type connMove struct {
	db      Database
	creds   map[string]bool
	changed int
}

func migrateConnections(db Database) int {
	if db == nil {
		return 0
	}
	m := &connMove{db: db, creds: map[string]bool{}}
	for _, c := range ListConnectors(db) {
		if Outdated(c) {
			m.outdated(c)
		}
	}
	m.ownServers()
	m.dropCredentials()
	Settle(db)
	return m.changed
}

// outdated moves connector c onto a connection: the one provided in its
// place, or itself made again.
func (m *connMove) outdated(c Connector) {
	old, err := Parse(c.Spec)
	if err != nil {
		return
	}
	cred := strings.TrimSpace(old.Provider.Auth.Credential)
	if cred != "" {
		m.creds[cred] = true
	}
	if next, carry := Successor(c); next != "" {
		nc, _ := GetConnector(m.db, next)
		m.keep(nc, carry, cred)
		m.repoint(c.Name, next)
		if pages := TieIns(m.db)[c.Name]; len(pages) > 0 {
			Log("[migration] %s moved onto %s, but %s still names it: it is kept", c.Name, next, strings.Join(pages, ", "))
			return
		}
		if err := DeleteConnector(m.db, c.Name); err == nil {
			forgetRecord(c)
			m.changed++
			Log("[migration] %s is now %s, a connection gohort provides", c.Name, next)
		}
		return
	}
	var spec json.RawMessage
	if c.Template != "" {
		spec, _ = recipes.TemplateConnectorSpec(c.Template)
	}
	if spec == nil {
		spec = withSecretSetting(old, cred)
	}
	if spec == nil {
		return
	}
	next, err := Parse(spec)
	if err != nil {
		return
	}
	carry := carryOver(old, next)
	c.Spec = spec
	if err := SaveConnector(m.db, c); err != nil {
		Log("[migration] %s could not be made again as a connection: %v", c.Name, err)
		return
	}
	m.changed++
	if r, _, ok := recipes.Get(m.db, c.Template); ok && c.Template != "" && strings.TrimSpace(c.DisplayName) == "" {
		SetConnectorDisplayName(m.db, c.Name, FreeDisplayName(m.db, c.Name, r.Title))
	}
	c, _ = GetConnector(m.db, c.Name)
	m.keep(c, carry, cred)
	Log("[migration] %s is made again as a connection, keeping its own settings and key", c.Name)
}

// keep gives connection c the settings carried over, each where it keeps
// none or still its default (one set on purpose stays), and the key of
// credential cred (when it keeps none).
func (m *connMove) keep(c Connector, carry map[string]string, cred string) {
	kept := ConnectionConfig(m.db, c)
	defaults := map[string]string{}
	for _, st := range SettingsFor(c, "") {
		defaults[st.Key] = st.Field.Default
	}
	set := false
	for k, v := range carry {
		if kept[k] == "" || kept[k] == defaults[k] {
			if kept[k] != v {
				kept[k], set = v, true
			}
		}
	}
	if set {
		if err := SetConnectionConfig(m.db, c, kept); err == nil {
			m.changed++
		}
	}
	if cred == "" {
		return
	}
	// Signing as this machine moves no key: its profile is a setting.
	if old, ok := Secure().Load(cred); ok && old.Type == SecureCredAWS && strings.TrimSpace(old.AWSProfile) != "" {
		values := ConnectionConfig(m.db, c)
		if _, declared := declaredOnly(c, map[string]string{"profile": "x"})["profile"]; declared && values["profile"] == "" {
			values["profile"] = strings.TrimSpace(old.AWSProfile)
			if SetConnectionConfig(m.db, c, values) == nil {
				m.changed++
			}
		}
	}
	s, err := Parse(c.Spec)
	if err != nil {
		return
	}
	settled, err := s.settle(ConnectionConfig(m.db, c))
	if err != nil {
		Log("[migration] %s's key stays in the credential %s: %v", c.Name, cred, err)
		return
	}
	f, ok := settled.secretField()
	if !ok || HasSecret(c, f.Name) {
		return
	}
	if moved, err := Secure().MoveCredentialToConnection(m.db, cred, c, f.Name, settled.secretAddresses(f)); err != nil {
		Log("[migration] %s's key could not be moved from the credential %s: %v", c.Name, cred, err)
	} else if moved {
		m.changed++
	}
}

// carryOver is what an outdated spec had written in that a current one
// takes as settings: its address, its model, and for Bedrock its region
// (read from its host) and API.
func carryOver(old, next Spec) map[string]string {
	carry := map[string]string{}
	base := strings.TrimSpace(old.Provider.BaseURL)
	if base != "" && !strings.Contains(base, "{") {
		carry["base_url"] = base
	}
	if m := bedrockRegionRE.FindStringSubmatch(base); m != nil {
		carry["region"] = m[1]
	}
	for _, b := range old.Provides {
		if v, ok := b.Defaults["model"].(string); ok && v != "" && !strings.Contains(v, "{") {
			carry["model"], carry[settingKey(b.Capability, "model")] = v, v
		}
	}
	out := map[string]string{}
	for _, st := range next.settings() {
		if v, ok := carry[st.Key]; ok && !st.Field.IsSecret() {
			out[st.Key] = v
		}
	}
	return out
}

// bedrockRegionRE reads the region from a Bedrock host.
var bedrockRegionRE = regexp.MustCompile(`^https://bedrock(?:-runtime|-mantle)?\.([a-z0-9-]+)\.(?:amazonaws\.com|api\.aws)`)

// withSecretSetting is spec s with the vault credential it named made a
// secret setting, sent as the credential sent it: for a connector no
// template made.
func withSecretSetting(s Spec, cred string) json.RawMessage {
	if cred == "" {
		return nil
	}
	s.Provider.Auth = Auth{Secret: "api_key"}
	s.Config = append(s.Config, ConfigField{Name: "api_key", Label: "API key", Type: "secret", Send: credentialSend(cred), Required: true})
	raw, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return raw
}

// credentialSend is how a vault credential sent its key, as a secret
// setting's send.
func credentialSend(name string) string {
	c, ok := Secure().Load(name)
	if !ok {
		return "bearer"
	}
	switch c.Type {
	case SecureCredHeader:
		return "header:" + c.ParamName
	case SecureCredQuery:
		return "query:" + c.ParamName
	case SecureCredBasicAuth:
		return "basic:" + c.Username
	case SecureCredAWS:
		return "aws_sigv4"
	}
	return "bearer"
}

// repoint makes every page naming connector from name connector to.
func (m *connMove) repoint(from, to string) {
	names := func(v string) bool { n, ok := media.ChosenConnection(v); return ok && n == from }
	for _, p := range providerPages {
		var v string
		if m.db.Get(p.table, p.field, &v) && names(v) {
			m.db.Set(p.table, p.field, media.Choice(p.job, to))
			m.changed++
		}
	}
	// The records keep their endpoint and model: an embedding's space is
	// named by them, so the vectors stay comparable.
	var emb EmbeddingConfig
	if m.db.Get(EmbeddingTable, "current", &emb) && names(emb.Provider) {
		emb.Provider = media.Choice(media.JobEmbed, to)
		m.db.Set(EmbeddingTable, "current", emb)
		SetEmbeddingConfig(emb)
		m.changed++
	}
	var tr media.TranscribeConfig
	if m.db.Get(TranscribeTable, "current", &tr) && names(tr.Provider) {
		tr.Provider = media.Choice(media.JobTranscribe, to)
		m.db.Set(TranscribeTable, "current", tr)
		SetTranscribeConfig(tr)
		m.changed++
	}
	var sp media.SpeechConfig
	if m.db.Get(media.SpeechTable, "current", &sp) && names(sp.Provider) {
		sp.Provider = media.Choice(media.JobSpeak, to)
		m.db.Set(media.SpeechTable, "current", sp)
		media.SetSpeechConfig(sp)
		m.changed++
	}
	for _, list := range [][2]string{{ImageTable, "agents"}, {MusicTable, "agents"}} {
		var names []string
		if !m.db.Get(list[0], list[1], &names) {
			continue
		}
		for i, n := range names {
			if n == from {
				names[i] = to
				m.changed++
			}
		}
		m.db.Set(list[0], list[1], names)
	}
	var img string
	if m.db.Get(ImageTable, "provider", &img) && img == from {
		m.db.Set(ImageTable, "provider", to)
		m.changed++
	}
}

// declaredOnly is values without what connection c does not take, or blank.
func declaredOnly(c Connector, values map[string]string) map[string]string {
	out := map[string]string{}
	for _, st := range SettingsFor(c, "") {
		if v := strings.TrimSpace(values[st.Key]); v != "" {
			out[st.Key] = v
		}
	}
	return out
}

// differs reports whether a connection keeping kept was set up otherwise
// than values.
func differs(kept, values map[string]string) bool {
	for k, v := range values {
		if kept[k] != "" && kept[k] != v {
			return true
		}
	}
	return false
}

// otherKey reports whether connection c keeps a key other than key.
func (m *connMove) otherKey(c Connector, key string) bool {
	if key == "" {
		return false
	}
	kept, ok := GetConnectionSecret(m.db, c, "api_key")
	return ok && kept.Value != "" && kept.Value != key
}

// another installs a second connection from the template gohort provides
// name from.
func (m *connMove) another(name string) (Connector, bool) {
	id := recipes.ProvidedTemplate(name)
	free := freeConnectorName(m.db, name+"-lead")
	if id == "" {
		return Connector{}, false
	}
	if _, err := recipes.Install(m.db, id, "", map[string]string{"name": free}); err != nil {
		Log("[migration] a second %s connection could not be made: %v", name, err)
		return Connector{}, false
	}
	c, ok := GetConnector(m.db, free)
	return c, ok
}

// ownServers moves Embeddings, Transcription and Speech still set to this
// instance's own server onto the local server connection gohort provides,
// at the address and with the model they had.
func (m *connMove) ownServers() {
	own := func(provider string) bool {
		p := strings.TrimSpace(provider)
		return p == "" || p == EmbeddingProviderLocal
	}
	var emb EmbeddingConfig
	if m.db.Get(EmbeddingTable, "current", &emb) && own(emb.Provider) && strings.TrimSpace(emb.Endpoint) != "" {
		base := strings.TrimRight(strings.TrimSpace(emb.Endpoint), "/")
		name := "local-embeddings"
		if strings.HasSuffix(base, "/api") || strings.HasSuffix(base, "/api/embed") || strings.HasSuffix(base, "/api/embeddings") {
			name, base = "ollama-embeddings", base[:strings.LastIndex(base, "/api")]
		}
		base = strings.TrimSuffix(base, "/embeddings")
		if m.ownServer(name, base, emb.Model, emb.APIKey, "embed.text") {
			emb.Provider, emb.APIKey = media.Choice(media.JobEmbed, name), ""
			m.db.Set(EmbeddingTable, "current", emb)
			SetEmbeddingConfig(emb)
		}
	}
	var tr media.TranscribeConfig
	if m.db.Get(TranscribeTable, "current", &tr) && own(tr.Provider) && strings.TrimSpace(tr.Endpoint) != "" {
		base := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(tr.Endpoint), "/"), "/audio/transcriptions")
		if m.ownServer("local-transcription", base, tr.Model, tr.APIKey, "speech.transcribe") {
			tr.Provider, tr.APIKey = media.Choice(media.JobTranscribe, "local-transcription"), ""
			m.db.Set(TranscribeTable, "current", tr)
			SetTranscribeConfig(tr)
		}
	}
	var sp media.SpeechConfig
	if m.db.Get(media.SpeechTable, "current", &sp) && own(sp.Provider) && strings.TrimSpace(sp.Endpoint) != "" {
		base := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(sp.Endpoint), "/"), "/audio/speech")
		if m.ownServer("local-speech", base, sp.Model, sp.APIKey, "speech.synthesize") {
			sp.Provider, sp.APIKey = media.Choice(media.JobSpeak, "local-speech"), ""
			m.db.Set(media.SpeechTable, "current", sp)
			media.SetSpeechConfig(sp)
		}
	}
}

// ownServer sets the provided connection name up at base with model, and
// key when it takes one, for capability.
func (m *connMove) ownServer(name, base, model, key, capability string) bool {
	c, ok := GetConnector(m.db, name)
	if !ok || !c.Provided {
		return false
	}
	values := declaredOnly(c, map[string]string{"base_url": base, "model": model, settingKey(capability, "model"): model})
	merged := ConnectionConfig(m.db, c)
	for k, v := range values {
		merged[k] = v
	}
	if err := SaveSettings(c, capability, merged, declaredOnly(c, map[string]string{"api_key": key})); err != nil {
		Log("[migration] %s could not move onto %s: %v", capability, name, err)
		return false
	}
	m.changed++
	Log("[migration] %s now runs on the connection %s, at %s", capability, name, base)
	return true
}

// dropCredentials deletes each vault credential the moved connectors sent
// their keys with that nothing names any more: no connector's spec, and no
// tool.
func (m *connMove) dropCredentials() {
	for cred := range m.creds {
		if CredentialToolsResolver != nil && len(CredentialToolsResolver(cred)) > 0 {
			continue
		}
		named := false
		for _, c := range ListConnectors(m.db) {
			if strings.Contains(string(c.Spec), fmt.Sprintf("%q", cred)) {
				named = true
				break
			}
		}
		if named {
			continue
		}
		if err := Secure().Delete(cred); err == nil {
			m.changed++
			Log("[migration] the credential %s, which only connectors before connections used, is deleted", cred)
		}
	}
}

