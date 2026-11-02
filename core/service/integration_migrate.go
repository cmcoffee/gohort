package service

// The one migration of the integration rework (docs/design/integration-model.md,
// step 9): it takes an install into the shape the rework settled on, from
// either starting point, once, at startup (MigrateIntegration):
//
//   - before plan B: providers built into gohort, keys in each page's own
//     settings and in the vault, Source Hooks, a research tool written into a
//     connector's spec;
//   - plan B: LLM tiers and pages naming a connection "<job>:<name>", an LLM
//     tier's key kept by the connection it ran on.
//
// What it cannot map (an engine with no template, a connection that is not
// here, an image backend in a shape no builtin runs) it leaves as it is, and
// the page says it needs setting again (StaleSettings): it never falls back
// to something else. It changes no name agents use, and keeps the embeddings'
// space by rewriting only which connection makes them, never the endpoint and
// model the space is named by.
//
// Finding is read only and applying writes, so a dry run (gohort
// --migrate-dry-run, on a copy of the database) lists exactly what the
// migration does. Delete this file a release after it has run.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/migrate"
	"github.com/cmcoffee/gohort/core/recipes"
	"github.com/cmcoffee/gohort/core/sourcehooks"
)

// IntegrationMigration is the marker the migration leaves once it has run.
const IntegrationMigration = "integration_model:v1"

// MigrateIntegration runs the migration once. Run it once the credential
// store is wired (AuthDB), before anything serves.
func MigrateIntegration(db Database) {
	NewMigrationRunner("core", "").Once(IntegrationMigration, func() int {
		return planIntegration(db).apply()
	})
}

// IntegrationPlan is what the migration would change on db, one line each,
// changing nothing: the dry run.
func IntegrationPlan(db Database) []string {
	var out []string
	for _, m := range planIntegration(db).moves {
		out = append(out, m.what)
	}
	return out
}

// IntegrationMigrated reports whether the migration has run on db.
func IntegrationMigrated(db Database) bool {
	var m migrate.MigrationMarker
	key := migrate.MigrationMarker{App: "core", Name: IntegrationMigration}.Key()
	return db != nil && db.Get(migrate.MigrationsTable, key, &m) && m.Done
}

// move is one change: what it is, as the log says it, and doing it.
type move struct {
	what string
	do   func() error
}

// upgrade is one run: the moves found, the vault credentials whose keys
// moved (deleted once nothing names them), and whether an LLM tier changed.
type upgrade struct {
	db    Database
	moves []move
	creds map[string]bool
	tiers bool
}

func (u *upgrade) add(what string, do func() error) {
	u.moves = append(u.moves, move{what, do})
}

// planIntegration finds every move, in the order they apply: connections
// first, so the pages after them name connections that are there.
func planIntegration(db Database) *upgrade {
	u := &upgrade{db: db, creds: map[string]bool{}}
	if db == nil {
		return u
	}
	u.vaultKeyedConnectors()
	u.imageBackends()
	u.researchInSpecs()
	u.sourceHooks()
	u.tier(LLMTable, "Worker LLM")
	u.tier(LeadLLMTable, "Lead LLM")
	u.prefixedChoices()
	u.searchEngines()
	u.imageProviders()
	u.ownServers()
	return u
}

// apply does every move, logging each, and how many it did.
func (u *upgrade) apply() int {
	done := 0
	for _, m := range u.moves {
		if err := m.do(); err != nil {
			Log("[migration] not done: %s: %v", m.what, err)
			continue
		}
		done++
		Log("[migration] %s", m.what)
	}
	done += u.dropCredentials()
	if u.tiers {
		if err := ReloadLLMs(); err != nil {
			Log("[migration] the LLM tiers did not reload: %v", err)
		}
	}
	return done
}

// ---- LLM tiers --------------------------------------------------------------

// llmProviders are the providers a tier names plainly, each also the
// dialect a plan B model connection spoke.
var llmProviders = map[string]bool{"anthropic": true, "openai": true, "gemini": true, "bedrock": true,
	"ollama": true, "llama.cpp": true, "vllm": true}

// tier moves the LLM tier kept in table: off the connection it named (plan
// B), with that connection's address, model and key; or, named plainly with
// a key kept before keys were bound to an address, that key bound to the
// address the tier sends to, the only one it was ever sent to.
func (u *upgrade) tier(table, page string) {
	var provider, endpoint, key string
	u.db.Get(table, "provider", &provider)
	u.db.Get(table, "endpoint", &endpoint)
	if name, ok := media.LegacyConnection(provider); ok {
		u.tierFromConnection(table, page, name)
		return
	}
	var given ConnectionSecret
	if !llmProviders[strings.ToLower(strings.TrimSpace(provider))] || !u.db.Get(table, "api_key", &key) ||
		strings.TrimSpace(key) == "" || u.db.Get(table, "api_key_for", &given) {
		return
	}
	cfg := LLMProviderConfig{Provider: strings.TrimSpace(provider), Endpoint: endpoint}
	addr := cfg.KeyAddress()
	if addr == "" {
		return
	}
	u.add(fmt.Sprintf("the %s's key is bound to %s, where it was sent", page, addr), func() error {
		u.db.Set(table, "api_key_for", ConnectionSecret{Addresses: []string{addr}, Set: time.Now()})
		return nil
	})
}

// modelSpec is what a plan B model connection's spec said, read as data:
// the rework's specs have no llm.chat.
type modelSpec struct {
	Config []struct {
		Name    string `json:"name"`
		Default any    `json:"default"`
	} `json:"config"`
	Provider struct {
		BaseURL string `json:"base_url"`
	} `json:"provider"`
	Provides []struct {
		Capability string         `json:"capability"`
		Dialect    string         `json:"dialect"`
		Defaults   map[string]any `json:"defaults"`
	} `json:"provides"`
}

var configPlaceholderRE = regexp.MustCompile(`\{config\.([a-z0-9_]+)\}`)

// tierFromConnection moves the tier in table off connection name.
func (u *upgrade) tierFromConnection(table, page, name string) {
	c, ok := GetConnector(u.db, name)
	if !ok {
		return
	}
	var s modelSpec
	if json.Unmarshal(c.Spec, &s) != nil {
		return
	}
	var dialect string
	defaults := map[string]any{}
	for _, b := range s.Provides {
		if b.Capability == "llm.chat" {
			dialect, defaults = strings.TrimSpace(b.Dialect), b.Defaults
		}
	}
	if !llmProviders[dialect] {
		return
	}
	values := ConnectionConfig(u.db, c)
	for _, f := range s.Config {
		if strings.TrimSpace(values[f.Name]) == "" && f.Default != nil {
			values[f.Name] = strings.TrimSpace(fmt.Sprint(f.Default))
		}
	}
	base := strings.TrimRight(configPlaceholderRE.ReplaceAllStringFunc(s.Provider.BaseURL, func(p string) string {
		return values[configPlaceholderRE.FindStringSubmatch(p)[1]]
	}), "/")
	if strings.Contains(base, "{") {
		return
	}
	var model string
	u.db.Get(table, "model", &model)
	if strings.TrimSpace(model) == "" {
		model = values["model"]
		if model == "" && defaults["model"] != nil {
			model = fmt.Sprint(defaults["model"])
		}
	}
	cfg := LLMProviderConfig{Provider: dialect}
	set := map[string]string{"provider": dialect, "model": model}
	if dialect == "bedrock" {
		api := values["api"]
		if api == "" && defaults["api"] != nil {
			api = fmt.Sprint(defaults["api"])
		}
		if api == "" {
			api = "converse"
		}
		region := values["region"]
		if m := bedrockRegionRE.FindStringSubmatch(base); region == "" && m != nil {
			region = m[1]
		}
		set["aws_region"], set["aws_profile"], set["bedrock_api"], set["endpoint"] = region, values["profile"], api, ""
	} else if !sameHost(base, cfg.KeyAddress()) {
		set["endpoint"], cfg.Endpoint = base, base
	} else {
		set["endpoint"] = ""
	}
	key, keyOK := "", false
	if sec, ok := GetConnectionSecret(u.db, c, "api_key"); ok && strings.TrimSpace(sec.Value) != "" && dialect != "bedrock" {
		if sentTo(sec.Addresses, cfg.KeyAddress()) {
			key, keyOK = sec.Value, true
		} else {
			Log("[migration] the %s's key on %s was given for %s, not %s: it is not moved, and is given again on its page", page, name, strings.Join(sec.Addresses, ", "), cfg.KeyAddress())
		}
	}
	what := fmt.Sprintf("the %s runs on %s directly (was the connection %s)", page, dialect, name)
	if keyOK {
		what += ", with its key"
	}
	u.add(what, func() error {
		for k, v := range set {
			if v == "" {
				u.db.Unset(table, k)
			} else {
				u.db.Set(table, k, v)
			}
		}
		if keyOK {
			cfg.SaveKey(u.db, table, key)
		}
		u.tiers = true
		return nil
	})
}

// sameHost reports whether a and b are at the same scheme, host and port.
func sameHost(a, b string) bool {
	ua, ea := url.Parse(strings.TrimSpace(a))
	ub, eb := url.Parse(strings.TrimSpace(b))
	return ea == nil && eb == nil && ua.Host != "" && strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}

// sentTo reports whether a key given for addresses was sent to addr: one of
// them, at the same scheme, host and port.
func sentTo(addresses []string, addr string) bool {
	for _, a := range addresses {
		if strings.TrimRight(a, "/") == strings.TrimRight(addr, "/") || sameHost(a, addr) {
			return true
		}
	}
	return false
}

// bedrockRegionRE reads the region from a Bedrock host.
var bedrockRegionRE = regexp.MustCompile(`^https://bedrock(?:-runtime|-mantle)?\.([a-z0-9-]+)\.(?:amazonaws\.com|api\.aws)`)

// ---- the pages' choices -----------------------------------------------------

// prefixedChoices names plainly each connection a page names the old way
// ("search:serper-search", "service:mail-out"). An embedding, transcription
// or speech record keeps its endpoint and model: the embeddings' space is
// named by them.
func (u *upgrade) prefixedChoices() {
	for _, p := range []struct{ table, key, page string }{
		{SearchTable, "provider", "Web Search"}, {MailTable, "provider", "Mail"},
		{BrowseTable, "source", "Page Rendering"}, {ImageTable, "provider", "Image Generation"},
	} {
		var v string
		u.db.Get(p.table, p.key, &v)
		if name, ok := media.LegacyConnection(v); ok {
			table, key := p.table, p.key
			u.add(fmt.Sprintf("%s names %s plainly (was %s)", p.page, name, v), func() error {
				u.db.Set(table, key, name)
				return nil
			})
		}
	}
	var emb EmbeddingConfig
	if u.db.Get(EmbeddingTable, "current", &emb) {
		if name, ok := media.LegacyConnection(emb.Provider); ok {
			u.add(fmt.Sprintf("Embeddings names %s plainly (was %s), its space kept", name, emb.Provider), func() error {
				emb.Provider = name
				u.db.Set(EmbeddingTable, "current", emb)
				SetEmbeddingConfig(emb)
				return nil
			})
		}
	}
	var tr media.TranscribeConfig
	if u.db.Get(TranscribeTable, "current", &tr) {
		if name, ok := media.LegacyConnection(tr.Provider); ok {
			u.add(fmt.Sprintf("Audio Transcription names %s plainly (was %s)", name, tr.Provider), func() error {
				tr.Provider = name
				u.db.Set(TranscribeTable, "current", tr)
				SetTranscribeConfig(tr)
				return nil
			})
		}
	}
	var sp media.SpeechConfig
	if u.db.Get(media.SpeechTable, "current", &sp) {
		if name, ok := media.LegacyConnection(sp.Provider); ok {
			u.add(fmt.Sprintf("Speech names %s plainly (was %s)", name, sp.Provider), func() error {
				sp.Provider = name
				u.db.Set(media.SpeechTable, "current", sp)
				media.SetSpeechConfig(sp)
				return nil
			})
		}
	}
	for _, p := range []struct{ table, page string }{{ImageTable, "Image Generation"}, {MusicTable, "Music"}} {
		var names []string
		if !u.db.Get(p.table, "agents", &names) {
			continue
		}
		plain, changed := make([]string, len(names)), false
		for i, n := range names {
			plain[i] = n
			if name, ok := media.LegacyConnection(n); ok {
				plain[i], changed = name, true
			}
		}
		if changed {
			table := p.table
			u.add(fmt.Sprintf("%s offers %s to agents, named plainly", p.page, strings.Join(plain, ", ")), func() error {
				u.db.Set(table, "agents", plain)
				return nil
			})
		}
	}
}

// engineTemplates are the engines Web Search once ran itself, by the
// template each is now a connector from.
var engineTemplates = map[string]string{"brave": "brave-search", "serper": "serper-search"}

// searchEngines makes the engine Web Search ran itself, with the key it was
// given, a connector from its template, and names it. One with no template
// (google), or no key, is left to be set again.
func (u *upgrade) searchEngines() {
	var engine, key string
	u.db.Get(SearchTable, "provider", &engine)
	u.db.Get(SearchTable, "api_key", &key)
	template, ok := engineTemplates[strings.TrimSpace(engine)]
	if !ok || strings.TrimSpace(key) == "" {
		return
	}
	u.add(fmt.Sprintf("Web Search's %s engine is a connector from the %s template, with its key", engine, template), func() error {
		c, err := u.install(template, "web.search", nil, map[string]string{"api_key": key})
		if err != nil {
			return err
		}
		u.db.Set(SearchTable, "provider", c.Name)
		u.db.Unset(SearchTable, "api_key")
		return nil
	})
}

// imageTemplates are the image providers gohort once ran itself, by the
// template each is now a connector from.
var imageTemplates = map[string]string{"openai": "openai-images", "gemini": "gemini-images"}

// imageProviders makes the image provider gohort ran itself, with the key it
// was given, a connector from its template, and names it.
func (u *upgrade) imageProviders() {
	var provider, key string
	u.db.Get(ImageTable, "provider", &provider)
	u.db.Get(ImageTable, "api_key", &key)
	template, ok := imageTemplates[strings.TrimSpace(provider)]
	if !ok || strings.TrimSpace(key) == "" {
		return
	}
	u.add(fmt.Sprintf("Image Generation's %s is a connector from the %s template, with its key", provider, template), func() error {
		c, err := u.install(template, "image.generate", nil, map[string]string{"api_key": key})
		if err != nil {
			return err
		}
		u.db.Set(ImageTable, "provider", c.Name)
		u.db.Unset(ImageTable, "api_key")
		return nil
	})
}

// ownServers makes Embeddings, Transcription and Speech still set to this
// instance's own server (before every one was a connector) a connector from
// its template at the address and with the model they had, and names it.
// The embeddings record keeps its endpoint and model, which name its space,
// so nothing is embedded again.
func (u *upgrade) ownServers() {
	own := func(enabled bool, provider, endpoint string) bool {
		p := strings.TrimSpace(provider)
		return enabled && (p == "" || p == EmbeddingProviderLocal) && strings.TrimSpace(endpoint) != ""
	}
	var emb EmbeddingConfig
	if u.db.Get(EmbeddingTable, "current", &emb) && own(emb.Enabled, emb.Provider, emb.Endpoint) {
		base, template := strings.TrimRight(strings.TrimSpace(emb.Endpoint), "/"), "openai-compatible-embeddings"
		if i := strings.LastIndex(base, "/api"); i >= 0 && (strings.HasSuffix(base, "/api") || strings.HasSuffix(base, "/api/embed") || strings.HasSuffix(base, "/api/embeddings")) {
			base, template = base[:i], "ollama-embeddings"
		}
		base = strings.TrimSuffix(base, "/embeddings")
		u.add(fmt.Sprintf("Embeddings come from a connector from the %s template at %s, their space kept", template, base), func() error {
			c, err := u.install(template, "embed.text", map[string]string{"base_url": base, "model": emb.Model}, nil)
			if err != nil {
				return err
			}
			u.keyNotKept("Embeddings", emb.APIKey, c)
			emb.Provider, emb.APIKey = c.Name, ""
			u.db.Set(EmbeddingTable, "current", emb)
			SetEmbeddingConfig(emb)
			return nil
		})
	}
	var tr media.TranscribeConfig
	if u.db.Get(TranscribeTable, "current", &tr) && own(tr.Enabled, tr.Provider, tr.Endpoint) {
		base := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(tr.Endpoint), "/"), "/audio/transcriptions")
		u.add(fmt.Sprintf("Audio Transcription comes from a connector from the openai-compatible-transcription template at %s", base), func() error {
			c, err := u.install("openai-compatible-transcription", "speech.transcribe", map[string]string{"base_url": base, "model": tr.Model}, nil)
			if err != nil {
				return err
			}
			u.keyNotKept("Audio Transcription", tr.APIKey, c)
			tr.Provider, tr.APIKey = c.Name, ""
			u.db.Set(TranscribeTable, "current", tr)
			SetTranscribeConfig(tr)
			return nil
		})
	}
	var sp media.SpeechConfig
	if u.db.Get(media.SpeechTable, "current", &sp) && own(sp.Enabled, sp.Provider, sp.Endpoint) {
		base := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(sp.Endpoint), "/"), "/audio/speech")
		u.add(fmt.Sprintf("Speech comes from a connector from the openai-compatible-speech template at %s", base), func() error {
			c, err := u.install("openai-compatible-speech", "speech.synthesize", map[string]string{"base_url": base, "model": sp.Model, "voice": sp.Voice}, nil)
			if err != nil {
				return err
			}
			u.keyNotKept("Speech", sp.APIKey, c)
			sp.Provider, sp.APIKey = c.Name, ""
			u.db.Set(media.SpeechTable, "current", sp)
			media.SetSpeechConfig(sp)
			return nil
		})
	}
}

// keyNotKept says in the log that page's key was not carried onto connector
// c, whose template takes none.
func (u *upgrade) keyNotKept(page, key string, c Connector) {
	if strings.TrimSpace(key) == "" || HasSecret(c, "api_key") {
		return
	}
	Log("[migration] %s had a key, which %s's template does not take: if its server needs one, add it to the connector's spec", page, c.Name)
}

// install adds a connector from template, sets the settings of values it
// takes (each for capability too) and the secrets, and gives it.
func (u *upgrade) install(template, capability string, values, secrets map[string]string) (Connector, error) {
	res, err := recipes.Install(u.db, template, "", nil)
	if err != nil {
		return Connector{}, err
	}
	var c Connector
	for _, o := range res.Outcomes {
		if o.Type == "connector" && o.Status == "imported" {
			c, _ = GetConnector(u.db, o.Name)
		}
	}
	if c.Name == "" {
		return c, fmt.Errorf("the %s template made no connector: %v", template, res.Warnings)
	}
	set := map[string]string{}
	for k, v := range values {
		set[k] = v
		set[settingKey(capability, k)] = v
	}
	merged := ConnectionConfig(u.db, c)
	for k, v := range declaredOnly(c, set) {
		merged[k] = v
	}
	if err := SaveSettings(c, capability, merged, declaredOnly(c, secrets)); err != nil {
		return c, fmt.Errorf("%s: %w", c.Name, err)
	}
	return c, nil
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

// ---- connectors -------------------------------------------------------------

// vaultKeyedConnectors makes each connector that sent its key with a vault
// credential (before a connection kept its own) keep it itself: made again
// from its template's spec, with the address and model it had written in,
// or, made by no template, with the credential made a secret setting sent
// as the credential sent it. Its name, and so its use, stay.
func (u *upgrade) vaultKeyedConnectors() {
	for _, c := range ListConnectors(u.db) {
		if c.Kind != Kind {
			continue
		}
		var spec map[string]any
		if json.Unmarshal(c.Spec, &spec) != nil {
			continue
		}
		provider, _ := spec["provider"].(map[string]any)
		auth, _ := provider["auth"].(map[string]any)
		cred, _ := auth["credential"].(string)
		if cred = strings.TrimSpace(cred); cred == "" {
			continue
		}
		var next json.RawMessage
		how := "with its credential made its own setting"
		if c.Template != "" {
			if t, ok := recipes.TemplateConnectorSpec(c.Template); ok {
				next, how = t, "from its template"
			}
		}
		if next == nil {
			auth = map[string]any{"secret": "api_key"}
			provider["auth"] = auth
			config, _ := spec["config"].([]any)
			spec["config"] = append(config, map[string]any{"name": "api_key", "label": "API key", "type": "secret",
				"send": credentialSend(cred), "required": true})
			next, _ = json.Marshal(spec)
		}
		carry := writtenIn(spec)
		c := c
		u.add(fmt.Sprintf("the connector %s keeps its own key (was the credential %s), made again %s", c.Name, cred, how), func() error {
			c.Spec = next
			if err := SaveConnector(u.db, c); err != nil {
				return err
			}
			c, _ = GetConnector(u.db, c.Name)
			if keep := declaredOnly(c, carry); len(keep) > 0 {
				merged := ConnectionConfig(u.db, c)
				for k, v := range keep {
					if merged[k] == "" {
						merged[k] = v
					}
				}
				if err := SetConnectionConfig(u.db, c, merged); err != nil {
					return err
				}
			}
			return u.moveKey(c, cred)
		})
	}
}

// writtenIn is what an old spec had written in that a template now takes as
// settings: its address, and the model each capability defaulted to.
func writtenIn(spec map[string]any) map[string]string {
	carry := map[string]string{}
	provider, _ := spec["provider"].(map[string]any)
	if base, _ := provider["base_url"].(string); strings.TrimSpace(base) != "" && !strings.Contains(base, "{") {
		carry["base_url"] = strings.TrimRight(strings.TrimSpace(base), "/")
	}
	provides, _ := spec["provides"].([]any)
	for _, p := range provides {
		b, _ := p.(map[string]any)
		capability, _ := b["capability"].(string)
		defaults, _ := b["defaults"].(map[string]any)
		if m, ok := defaults["model"].(string); ok && m != "" && !strings.Contains(m, "{") {
			carry["model"], carry[settingKey(capability, "model")] = m, m
		}
	}
	return carry
}

// moveKey moves vault credential cred's key into connection c's secret
// setting, for the addresses its spec now sends it to.
func (u *upgrade) moveKey(c Connector, cred string) error {
	s, err := Parse(c.Spec)
	if err != nil {
		return err
	}
	settled, err := s.settle(ConnectionConfig(u.db, c))
	if err != nil {
		return fmt.Errorf("its key stays in the credential %s: %w", cred, err)
	}
	f, ok := settled.secretField()
	if !ok || HasSecret(c, f.Name) {
		return nil
	}
	if _, err := Secure().MoveCredentialToConnection(u.db, cred, c, f.Name, settled.secretAddresses(f)); err != nil {
		return fmt.Errorf("its key could not be moved from the credential %s: %w", cred, err)
	}
	u.creds[cred] = true
	return nil
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

// restImageKind is the kind image backends were kept as before they were
// connections.
const restImageKind = "rest_image"

// imageBackends makes each image backend kept as a rest_image connector a
// connection running the comfyui or a1111 builtin under the same name, its
// key moved from the credential it was sent with. A peer's renderer is left
// to the peer's next sync, which rewrites it; a shape no builtin runs (a
// hosted API polled for its result) is left, and Image Generation says it
// needs setting again.
func (u *upgrade) imageBackends() {
	for _, c := range ListConnectors(u.db) {
		if c.Kind != restImageKind || IsPeerRenderer(c) {
			continue
		}
		var r RestImageSpec
		if json.Unmarshal(c.Spec, &r) != nil {
			continue
		}
		cred := strings.TrimSpace(r.Credential)
		if cred == "no_auth" || cred == "none" {
			cred = ""
		}
		send := ""
		if cred != "" {
			send = credentialSend(cred)
		}
		kind, raw, err := r.AsService(send)
		if err != nil {
			Log("[migration] the image backend %s is left as it was: %v", c.Name, err)
			continue
		}
		builtin := "a1111"
		if strings.TrimSpace(r.ComfyWorkflow) != "" {
			builtin = "comfyui"
		}
		c := c
		u.add(fmt.Sprintf("the image backend %s is a connection running the %s builtin, keeping its name and key", c.Name, builtin), func() error {
			c.Kind, c.Spec = kind, raw
			if c.Template == "" {
				c.Template = builtin
			}
			if err := SaveConnector(u.db, c); err != nil {
				return err
			}
			if cred == "" {
				return nil
			}
			c, _ = GetConnector(u.db, c.Name)
			return u.moveKey(c, cred)
		})
	}
}

// ---- research sources -------------------------------------------------------

// researchInSpecs makes the research topics and agent tool a search
// connector's spec held (before Research Sources) its Research Sources
// entry, on when the connector was approved, and takes them out of the spec.
func (u *upgrade) researchInSpecs() {
	for _, c := range ListConnectors(u.db) {
		if c.Kind != Kind {
			continue
		}
		var spec map[string]any
		if json.Unmarshal(c.Spec, &spec) != nil {
			continue
		}
		provides, _ := spec["provides"].([]any)
		rs := ResearchSource{Connector: c.Name, Off: !c.Approved}
		found := false
		for _, p := range provides {
			b, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if r, ok := b["research"].(map[string]any); ok {
				found = true
				rs.Always, _ = r["always"].(bool)
				ts, _ := r["topics"].([]any)
				for _, t := range ts {
					rs.Topics = append(rs.Topics, fmt.Sprint(t))
				}
			}
			if t, ok := b["tool"].(map[string]any); ok {
				found = true
				rs.ToolName, _ = t["name"].(string)
				rs.ToolDescription, _ = t["description"].(string)
			}
			delete(b, "research")
			delete(b, "tool")
		}
		if !found {
			continue
		}
		raw, err := json.Marshal(spec)
		if err != nil {
			continue
		}
		c := c
		u.add(fmt.Sprintf("the connector %s is a research source (its topics and tool moved out of its spec)", c.Name), func() error {
			c.Spec = raw
			if err := SaveConnector(u.db, c); err != nil {
				return err
			}
			return SaveResearchSource(u.db, rs)
		})
	}
}

// sourceHooks makes each stored Source Hook the search connector it
// described (HookConnector), its Research Sources entry on when the hook
// was, and the engine of Web Search when it was; then the hook is gone. A
// paywall hook, which nothing ever applied, is left.
func (u *upgrade) sourceHooks() {
	var engine, engineHook string
	u.db.Get(SearchTable, "provider", &engine)
	u.db.Get(SearchTable, "hook", &engineHook)
	for _, key := range u.db.Keys(sourceHookTable) {
		if strings.HasSuffix(key, "_auth") {
			continue
		}
		var h sourcehooks.SourceHook
		if !u.db.Get(sourceHookTable, key, &h) || h.Type == sourcehooks.HookTypePaywall {
			continue
		}
		u.db.Get(sourceHookTable, key+"_auth", &h.AuthKey)
		isEngine := engine == "hook" && strings.EqualFold(strings.TrimSpace(engineHook), strings.TrimSpace(h.Name))
		what := fmt.Sprintf("the Source Hook %s is a search connector", h.Name)
		if isEngine {
			what += ", and Web Search's engine"
		}
		key := key
		u.add(what, func() error {
			name, _, err := HookConnector(u.db, h, "Source Hook "+h.Name)
			if err != nil {
				return err
			}
			if rs, ok := GetResearchSource(u.db, name); ok {
				rs.Off = h.Disabled
				if err := SaveResearchSource(u.db, rs); err != nil {
					return err
				}
			}
			if isEngine {
				u.db.Set(SearchTable, "provider", name)
				u.db.Unset(SearchTable, "hook")
			}
			u.db.Unset(sourceHookTable, key)
			u.db.Unset(sourceHookTable, key+"_auth")
			return nil
		})
	}
}

// dropCredentials deletes each vault credential whose key moved that nothing
// names any more: no connector's spec, and no tool. How many it deleted.
func (u *upgrade) dropCredentials() int {
	n := 0
	for cred := range u.creds {
		if CredentialToolsResolver != nil && len(CredentialToolsResolver(cred)) > 0 {
			continue
		}
		named := false
		for _, c := range ListConnectors(u.db) {
			if strings.Contains(string(c.Spec), fmt.Sprintf("%q", cred)) {
				named = true
				break
			}
		}
		if named {
			continue
		}
		if err := Secure().Delete(cred); err == nil {
			n++
			Log("[migration] the credential %s, whose key its connection now keeps, is deleted", cred)
		}
	}
	return n
}
