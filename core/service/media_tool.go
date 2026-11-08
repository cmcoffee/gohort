package service

// The agent tool of a media.generate connector: generate_<kind>_<connector>,
// shown while Media Generation offers the connector and the agent may reach
// its connection, as a music connector's tool is (media.go).

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

var (
	mediaToolsMu sync.Mutex
	mediaTools   = map[string]bool{}
)

// A music or media connector's tool is claimed from the stored connectors,
// so a list naming one keeps it through a restart, when it is registered
// only once the connectors are served.
func init() { RegisterToolNameClaim(connectorMakesTool) }

// connectorMakesTool reports whether a stored connector makes the agent tool
// name: a music connector's generate_music_<name>, or a media connector's
// generate_<kind>_<name>.
func connectorMakesTool(name string) bool {
	if RootDB == nil || !strings.HasPrefix(name, "generate_") {
		return false
	}
	for _, c := range ListConnectors(RootDB) {
		if c.Kind != Kind {
			continue
		}
		s, err := Parse(c.Spec)
		if err != nil {
			continue
		}
		if _, _, err := s.binding("music.generate"); err == nil && (&musicTool{connector: c.Name}).Name() == name {
			return true
		}
		if _, _, err := s.binding("media.generate"); err == nil && (&mediaTool{connector: c.Name}).Name() == name {
			return true
		}
	}
	return false
}

// registerMediaTool registers connector name's agent tool once; the tool
// reads its kind and what it makes from the spec on each use, so an edited
// kind is the next schema's.
func registerMediaTool(name string) {
	mediaToolsMu.Lock()
	defer mediaToolsMu.Unlock()
	if mediaTools[name] {
		return
	}
	mediaTools[name] = true
	RegisterChatTool(&mediaTool{connector: name})
}

// mediaTool is generate_<kind>_<connector>.
type mediaTool struct{ connector string }

// binding is the connector's media.generate binding as its spec now says.
func (t *mediaTool) binding() (Binding, bool) {
	c, ok := GetConnector(RootDB, t.connector)
	if !ok {
		return Binding{}, false
	}
	s, err := Parse(c.Spec)
	if err != nil {
		return Binding{}, false
	}
	for _, b := range s.Provides {
		if b.Capability == "media.generate" {
			return b, true
		}
	}
	return Binding{}, false
}

// MediaKindOf is the kind connector c's media.generate says it makes, or ""
// when it gives no media.generate.
func MediaKindOf(c Connector) string {
	s, err := Parse(c.Spec)
	if err != nil {
		return ""
	}
	for _, b := range s.Provides {
		if b.Capability == "media.generate" {
			return b.MediaKind()
		}
	}
	return ""
}

// slug is s as part of a tool name: lower case, runs of anything else one _.
func slug(s string) string {
	return strings.Trim(unsafeToolChars.ReplaceAllString(strings.ToLower(s), "_"), "_")
}

func (t *mediaTool) kind() string {
	b, _ := t.binding()
	return b.MediaKind()
}

// Category files the tool with the others that make media.
func (t *mediaTool) Category() string { return MediaGenerationToolCategory }

func (t *mediaTool) Name() string {
	k := slug(t.kind())
	if k == "" {
		k = "media"
	}
	return "generate_" + k + "_" + slug(t.connector)
}

func (t *mediaTool) Desc() string {
	b, _ := t.binding()
	what := strings.TrimSpace(b.About)
	if what == "" {
		what = "Makes " + b.MediaKind() + " from a prompt."
	}
	takes := ""
	switch n := t.files(); {
	case n == 1:
		takes = " It takes one picture or file to work from."
	case n > 1:
		takes = fmt.Sprintf(" It takes up to %d pictures or files to work from, in order (a video's reference pictures; the first can be its opening frame).", n)
	}
	return what + " Uses the " + t.connector + " service." + takes + " What it makes is saved in your workspace and goes out with your reply."
}

func (t *mediaTool) Params() map[string]ToolParam {
	p := map[string]ToolParam{
		"prompt": {Type: "string", Description: "What to make, in words. Required."},
	}
	if t.files() > 0 {
		p["files"] = ToolParam{Type: "array", Items: &ToolParam{Type: "string"},
			Description: "What to work from, in order: a picture from this conversation (image#r.…), one someone sent (media#N), or a file in your workspace by its name there."}
	}
	return p
}

// mediaFileInputs are media.generate's file inputs, in order.
var mediaFileInputs = []string{"file", "file2", "file3", "file4", "file5", "file6"}

// files is how many files the connector's media.generate sends: file, then
// as many of file2 to file6, in order, as its operations use; 0 when it
// takes none.
func (t *mediaTool) files() int {
	c, ok := GetConnector(RootDB, t.connector)
	if !ok {
		return 0
	}
	s, err := Parse(c.Spec)
	if err != nil {
		return 0
	}
	b, ok := t.binding()
	if !ok {
		return 0
	}
	used := map[string]bool{}
	for _, st := range b.Steps {
		for _, tok := range opTokens(s.Provider.Operations[st.Call+st.Wait]) {
			used[tok] = true
		}
	}
	n := 0
	for _, in := range mediaFileInputs {
		if !used[in] {
			break
		}
		n++
	}
	return n
}

// sourceFile is the file ref names, as a File the call can send: a picture
// from this conversation or one someone sent (image#…, media#N), read as an
// image edit reads its sources into a file of its own (temp: the caller
// removes it), or any file in the workspace by its name.
func sourceFile(sess *ToolSession, ref string) (f *File, temp bool, err error) {
	ref = strings.TrimSpace(ref)
	low := strings.ToLower(ref)
	if strings.HasPrefix(low, RecentImageRefPrefix) || strings.HasPrefix(low, "media#") {
		data, mime, err := ResolveSourceImage(sess, ref)
		if err != nil {
			return nil, false, err
		}
		f, _, err := writeFile(bytes.NewReader(data), defaultMaxBytes)
		if err != nil {
			return nil, false, err
		}
		if mime != "" {
			f.Type = mime
		}
		return f, true, nil
	}
	abs, err := ResolveWorkspacePath(sess.WorkspaceDir, ref)
	if err != nil {
		return nil, false, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("no file %q in your workspace, and it is not a picture's id (image#r.…, media#N)", ref)
	}
	head := make([]byte, 512)
	if fh, err := os.Open(abs); err == nil {
		n, _ := fh.Read(head)
		fh.Close()
		head = head[:n]
	}
	return &File{Path: abs, Type: http.DetectContentType(head), Size: info.Size()}, false, nil
}

// Caps: a service on the network goes online; a local program runs.
func (t *mediaTool) Caps() []Capability {
	if c, ok := GetConnector(RootDB, t.connector); ok {
		if s, err := Parse(c.Spec); err == nil && s.Provider.Transport == "command" && !s.Provider.Network {
			return []Capability{CapExecute}
		}
	}
	return []Capability{CapNetwork}
}

// offered reports whether Media Generation offers this connector to agents,
// and no change waits on it.
func (t *mediaTool) offered() bool {
	if _, ok := GetConnector(RootDB, t.connector); !ok || held(t.connector) {
		return false
	}
	var agents []string
	RootDB.Get(MediaTable, "agents", &agents)
	for _, a := range agents {
		if a == t.connector {
			return true
		}
	}
	return false
}

// SchemaWithSession offers the tool only while Media Generation does, to an
// agent that may reach its connection.
func (t *mediaTool) SchemaWithSession(sess *ToolSession) (string, map[string]ToolParam) {
	if !t.offered() || sess.ConnectionBarred(t.connector) != nil {
		return "", nil
	}
	return t.Desc(), t.Params()
}

func (t *mediaTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("%s needs a session to save what it makes in", t.Name())
}

func (t *mediaTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if !t.offered() {
		return "", fmt.Errorf("%s is not offered: Media Generation no longer offers the connector %s to agents", t.Name(), t.connector)
	}
	if sess == nil || strings.TrimSpace(sess.WorkspaceDir) == "" {
		return "", fmt.Errorf("%s needs a workspace to save what it makes in", t.Name())
	}
	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("say what to make in prompt")
	}
	in := map[string]any{"prompt": strings.TrimSpace(prompt)}
	// files, or the one file an older call names.
	var refs []string
	if list, ok := args["files"].([]any); ok {
		for _, v := range list {
			if s, _ := v.(string); strings.TrimSpace(s) != "" {
				refs = append(refs, s)
			}
		}
	}
	if name, _ := args["file"].(string); strings.TrimSpace(name) != "" {
		refs = append([]string{name}, refs...)
	}
	if max := t.files(); len(refs) > max {
		if max == 0 {
			return "", fmt.Errorf("%s makes from a prompt alone and takes no pictures or files: leave files out, or use another generator. Do NOT drop them and present the result as made from them", t.Name())
		}
		return "", fmt.Errorf("%s takes at most %d pictures or files, and %d were given. Do NOT retry with fewer and present it as what was asked for: tell the user it takes %d", t.Name(), max, len(refs), max)
	}
	for i, ref := range refs {
		f, temp, err := sourceFile(sess, ref)
		if err != nil {
			return "", err
		}
		if temp {
			defer os.Remove(f.Path)
		}
		in[mediaFileInputs[i]] = f
	}
	res, err := Call(sess.Context(), sess, t.connector, "media.generate", in)
	if err != nil {
		return "", err
	}
	f := res["file"].File
	if f == nil {
		return "", fmt.Errorf("%s made nothing", t.connector)
	}
	defer os.Remove(f.Path)
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return "", err
	}
	kind := slug(t.kind())
	if kind == "" {
		kind = "media"
	}
	ext := filepath.Ext(f.Path)
	rel := kind + ext
	for i := 2; ; i++ {
		abs, err := ResolveWorkspacePath(sess.WorkspaceDir, rel)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			if err := os.WriteFile(abs, data, 0o644); err != nil {
				return "", err
			}
			break
		}
		rel = fmt.Sprintf("%s-%d%s", kind, i, ext)
	}
	sess.AddStagedFiles([]string{rel})
	b64 := base64.StdEncoding.EncodeToString(data)
	ctype := f.Type
	if ctype == "" {
		ctype = http.DetectContentType(data)
	}
	switch {
	case strings.HasPrefix(ctype, "image/"):
		sess.AppendImage(b64)
	case strings.HasPrefix(ctype, "video/"), strings.HasPrefix(ctype, "audio/"):
		sess.AppendVideo(b64)
	default:
		sess.AppendFile(FileAttachment{Name: rel, MimeType: ctype, Data: b64, Size: len(data)})
	}
	return fmt.Sprintf("Made it with %s, saved as %s in your workspace (%d KB). It is attached to your reply.", t.connector, rel, len(data)>>10), nil
}
