package main

// gohort --config-report: what this instance's integration settings are, as
// the values to type back in after the connector rework
// (docs/design/integration-model.md), which reconfigures by hand rather than
// migrating. It reads a copy of the database and changes nothing, and it
// never shows a key: only that one is kept, and since when.
//
// It reads the records as they are stored today, with its own small shapes
// for them, so it still reads them once the code that wrote them is gone.
// It reads only the tables named here, never an app's data beyond its
// agents' names, granted tools and credential opt-outs. Delete it a release
// after the rework.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// configReport is --config-report: the report of a copy of this instance's
// database. gohort must be stopped, since a running one holds the database.
func configReport(w io.Writer) error {
	init_config_file()
	data_dir := loadPath("data_dir", fmt.Sprintf("%s/data", global.root))
	src := mainDBFile(data_dir)
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("no database at %s: %w", src, err)
	}
	scratch, err := os.MkdirTemp("", "gohort-config-report-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	dst := filepath.Join(scratch, filepath.Base(src))
	if err := copyStoppedDB(src, dst); err != nil {
		return err
	}
	db, err := SecureDatabase(dst)
	if err != nil {
		return err
	}
	defer db.Close()
	savedAuth := AuthDB
	AuthDB = func() Database { return db }
	defer func() { AuthDB = savedAuth }()
	writeConfigReport(db, w)
	return nil
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// reportConnector is a stored connector, as the report reads it.
type reportConnector struct {
	Name        string
	ID          string
	DisplayName string
	Kind        string
	Template    string
	Spec        json.RawMessage
	Approved    bool
	Provided    bool
}

// reportAgent is a stored agent, as the report reads it.
type reportAgent struct {
	ID                  string
	Owner               string
	Name                string
	AllowedTools        []string
	DisabledCredentials []string
}

// reportMedia is the stored embeddings, transcription and speech settings,
// as the report reads them.
type reportMedia struct {
	Enabled  bool
	Provider string
	Endpoint string
	Model    string
	Voice    string
	APIKey   string
}

// reportSource is a stored Research Source, as the report reads it.
type reportSource struct {
	Connector string
	Topics    []string
	Always    bool
	ToolName  string
}

// choicePrefixes are what a page's choice of a connection was written with,
// by the job it does it for (and before that, service:).
var choicePrefixes = []string{"llm:", "embed:", "transcribe:", "speak:", "search:", "render:", "mail:", "convert:", "service:"}

// chosenConnection is the connection a page's stored choice names, or "".
func chosenConnection(v string) string {
	for _, p := range choicePrefixes {
		if name, ok := strings.CutPrefix(strings.TrimSpace(v), p); ok {
			return name
		}
	}
	return ""
}

// report writes one report: its sections in turn, each a list of lines.
type report struct {
	db         Database
	w          io.Writer
	connectors map[string]reportConnector
	named      map[string]bool // connections a page or an agent names
	wrote      int             // lines in the section being written
	open       bool
}

// writeConfigReport writes the report of db to w.
func writeConfigReport(db Database, w io.Writer) {
	r := &report{db: db, w: w, connectors: map[string]reportConnector{}, named: map[string]bool{}}
	for _, k := range db.Keys("connectors") {
		var c reportConnector
		if db.Get("connectors", k, &c) {
			r.connectors[c.Name] = c
		}
	}
	fmt.Fprintln(w, "gohort configuration report")
	fmt.Fprintln(w, "Type these back in after the connector rework. Keys are never shown: gather them first.")
	r.tiers()
	r.media("Embeddings", EmbeddingTable, "keep this exactly: it names the space your vectors are in")
	r.media("Transcription", TranscribeTable, "")
	r.media("Speech", "speech_config", "")
	r.search()
	r.images()
	r.mail()
	r.rendering()
	r.agents()
	r.allConnections()
	r.endSection()
}

func (r *report) section(title string) {
	r.endSection()
	fmt.Fprintf(r.w, "\n== %s ==\n", title)
	r.wrote, r.open = 0, true
}

// endSection says so when the section it ends had nothing set, so an empty
// one does not read as a report that stopped.
func (r *report) endSection() {
	if r.open && r.wrote == 0 {
		fmt.Fprintln(r.w, "  nothing set")
	}
	r.open = false
}

func (r *report) line(label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	fmt.Fprintf(r.w, "  %s: %s\n", label, value)
	r.wrote++
}

// str reads a string setting from table.
func (r *report) str(table, key string) string {
	var v string
	r.db.Get(table, key, &v)
	return v
}

// kept says whether a key is stored in table under key, never what it is.
func (r *report) kept(table, key string) string {
	if r.str(table, key) == "" {
		return ""
	}
	return "a key is kept"
}

// choice writes a page's stored choice, and the connection it names.
func (r *report) choice(label, v string) {
	r.line(label, v)
	if name := chosenConnection(v); name != "" {
		r.connection("  "+label+" is the connection", name)
	}
}

// connection writes connection name's settings, and whether it keeps a key.
func (r *report) connection(label, name string) {
	r.named[name] = true
	c, ok := r.connectors[name]
	if !ok {
		r.line(label, name+" (no such connection is stored)")
		return
	}
	title := c.Name
	if c.DisplayName != "" {
		title = c.DisplayName + " (" + c.Name + ")"
	}
	what := []string{c.Kind}
	if c.Template != "" {
		what = append(what, "from template "+c.Template)
	}
	if c.Provided {
		what = append(what, "provided by gohort, and gone after the rework")
	}
	r.line(label, title+": "+strings.Join(what, ", "))
	conn := Connector{Name: c.Name, ID: c.ID}
	values := ConnectionConfig(r.db, conn)
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r.line("    "+k, values[k])
	}
	for _, secret := range r.secretNames(c) {
		if s, ok := GetConnectionSecret(r.db, conn, secret); ok && s.Value != "" {
			given := ""
			if !s.Set.IsZero() {
				given = ", given " + s.Set.Format("2 Jan 2006")
			}
			r.line("    "+secret, "a key is kept"+given)
		}
	}
	if users := conn.Access(r.db); len(users) > 0 {
		r.line("    reachable through agents by", strings.Join(users, ", "))
	}
}

// secretNames are the secrets connection c declares, read off its spec.
func (r *report) secretNames(c reportConnector) []string {
	var spec struct {
		Config []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"config"`
	}
	json.Unmarshal(c.Spec, &spec)
	var out []string
	for _, f := range spec.Config {
		if f.Type == "secret" {
			out = append(out, f.Name)
		}
	}
	return out
}

// tiers writes each LLM tier's settings.
func (r *report) tiers() {
	for _, t := range []struct{ title, table string }{{"LLM: worker tier", LLMTable}, {"LLM: lead tier", LeadLLMTable}} {
		r.section(t.title)
		r.choice("provider", r.str(t.table, "provider"))
		for _, k := range []string{"model", "endpoint", "aws_region", "aws_profile", "bedrock_api", "default_effort", "max_effort"} {
			r.line(k, r.str(t.table, k))
		}
		var n int
		for _, k := range []string{"context_size", "thinking_budget", "request_timeout_seconds"} {
			n = 0
			if r.db.Get(t.table, k, &n) && n != 0 {
				r.line(k, fmt.Sprint(n))
			}
		}
		var b bool
		for _, k := range []string{"disable_thinking", "no_images", "native_tools"} {
			b = false
			if r.db.Get(t.table, k, &b) && b {
				r.line(k, "on")
			}
		}
		r.line("api_key", r.kept(t.table, "api_key"))
	}
}

// media writes the embeddings, transcription or speech settings in table.
func (r *report) media(title, table, note string) {
	r.section(title)
	var m reportMedia
	if !r.db.Get(table, "current", &m) {
		return
	}
	r.line("enabled", fmt.Sprint(m.Enabled))
	r.choice("provider", m.Provider)
	endpoint := m.Endpoint
	if note != "" && endpoint != "" {
		endpoint += "   <- " + note
	}
	r.line("endpoint", endpoint)
	if name := chosenConnection(m.Endpoint); name != "" {
		if name != chosenConnection(m.Provider) {
			r.connection("  endpoint is the connection", name)
		}
		if note != "" {
			r.line("  so", "add its connection back under the name "+name+", or every collection is embedded again")
		}
	}
	r.line("model", m.Model)
	r.line("voice", m.Voice)
	if m.APIKey != "" {
		r.line("api_key", "a key is kept")
	}
}

// search writes Web Search's and the Research Sources' settings.
func (r *report) search() {
	r.section("Web Search")
	r.choice("provider", r.str(SearchTable, "provider"))
	r.line("source", r.str(SearchTable, "source"))
	r.line("endpoint", r.str(SearchTable, "endpoint"))
	r.line("hook", r.str(SearchTable, "hook"))
	r.line("api_key", r.kept(SearchTable, "api_key"))

	r.section("Research Sources")
	keys := r.db.Keys("research_sources")
	sort.Strings(keys)
	for _, k := range keys {
		var s reportSource
		if !r.db.Get("research_sources", k, &s) {
			continue
		}
		when := "on " + strings.Join(s.Topics, ", ")
		if s.Always {
			when = "on every run"
		}
		if s.ToolName != "" {
			when += "; agents' tool " + s.ToolName
		}
		r.line(s.Connector, when)
		r.connection("  it is the connection", s.Connector)
	}
}

// images writes Image Generation's and Music's settings.
func (r *report) images() {
	r.section("Image Generation")
	def := r.str(ImageTable, "provider")
	r.line("default", def)
	if def != "" && def != "none" && !strings.HasPrefix(def, "peer:") {
		r.connection("  the default is the connection", def)
	}
	var agents []string
	r.db.Get(ImageTable, "agents", &agents)
	r.line("also offered to agents", strings.Join(agents, ", "))
	for _, a := range agents {
		if a != def && !strings.HasPrefix(a, "peer:") {
			r.connection("  offered", a)
		}
	}
	r.line("its spec, to paste back", "see All connections below (a ComfyUI workflow and node map are in it)")

	r.section("Music")
	var music []string
	r.db.Get("music_config", "agents", &music)
	r.line("offered to agents", strings.Join(music, ", "))
	for _, a := range music {
		r.connection("  offered", a)
	}
}

// mail writes Mail's settings.
func (r *report) mail() {
	r.section("Mail")
	r.choice("provider", r.str(MailTable, "provider"))
	for _, k := range []string{"server", "username", "from", "recipient"} {
		r.line(k, r.str(MailTable, k))
	}
	r.line("password", r.kept(MailTable, "password"))
}

// rendering writes where pages are rendered.
func (r *report) rendering() {
	r.section("Page rendering")
	r.choice("source", r.str(BrowseTable, "source"))
}

// agents writes every agent whose granted tools or credential opt-outs name
// a connection, so each can be checked once the connections are back.
func (r *report) agents() {
	r.section("Agents naming a connection")
	base := r.db.Bucket("orchestrate")
	stores := []struct {
		who string
		db  Database
	}{{"(instance)", base}}
	for _, u := range AuthListUsers(r.db) {
		stores = append(stores, struct {
			who string
			db  Database
		}{u.Username, UserDB(base, u.Username)})
	}
	for _, s := range stores {
		ids := s.db.Keys("orchestrate_agents")
		sort.Strings(ids)
		for _, id := range ids {
			var a reportAgent
			if !s.db.Get("orchestrate_agents", id, &a) {
				continue
			}
			var names []string
			for _, t := range a.AllowedTools {
				if c := r.toolConnection(t); c != "" {
					names = append(names, "tool "+t)
					r.named[c] = true
				}
			}
			for _, cred := range a.DisabledCredentials {
				if c, ok := strings.CutPrefix(cred, "connection:"); ok {
					note := ""
					if r.connectors[c].Provided {
						note = " (gone after the rework)"
					}
					names = append(names, "kept from "+c+note)
				}
			}
			if len(names) == 0 {
				continue
			}
			owner := a.Owner
			if owner == "" {
				owner = s.who
			}
			r.line(a.Name+" ["+owner+"]", strings.Join(names, "; "))
		}
	}
}

// toolConnection is the connection a granted tool runs on, or "".
func (r *report) toolConnection(tool string) string {
	if name, ok := strings.CutPrefix(tool, "generate_image_"); ok {
		for n := range r.connectors {
			if strings.ReplaceAll(n, "-", "_") == name {
				return n
			}
		}
	}
	var s reportSource
	for _, k := range r.db.Keys("research_sources") {
		if r.db.Get("research_sources", k, &s) && s.ToolName != "" && s.ToolName == tool {
			return s.Connector
		}
	}
	return ""
}

// allConnections writes every stored connection, its settings, and its spec
// to paste back (a spec never holds a key).
func (r *report) allConnections() {
	r.section("All connections")
	names := make([]string, 0, len(r.connectors))
	for n := range r.connectors {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := r.connectors[n]
		state := "not in use"
		if c.Approved {
			state = "in use"
		}
		fmt.Fprintln(r.w)
		r.connection(state, n)
		var pretty bytes.Buffer
		if json.Indent(&pretty, c.Spec, "      ", "  ") == nil {
			fmt.Fprintf(r.w, "    spec:\n      %s\n", pretty.String())
		}
	}
}
