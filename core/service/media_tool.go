package service

// The agent tool of a media.generate connector: generate_<kind>_<connector>,
// shown while Media Generation offers the connector and the agent may reach
// its connection, as a music connector's tool is (media.go).

import (
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
	return what + " Uses the " + t.connector + " service. Give it a prompt, and a file from your workspace to work from when it takes one. What it makes is saved in your workspace and goes out with your reply."
}

func (t *mediaTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"prompt": {Type: "string", Description: "What to make, in words. Required."},
		"file":   {Type: "string", Description: "A file in your workspace to work from, by its name there, when the service takes one."},
	}
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
	if name, _ := args["file"].(string); strings.TrimSpace(name) != "" {
		abs, err := ResolveWorkspacePath(sess.WorkspaceDir, strings.TrimSpace(name))
		if err != nil {
			return "", err
		}
		info, err := os.Stat(abs)
		if err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("no file %q in your workspace", name)
		}
		head := make([]byte, 512)
		if fh, err := os.Open(abs); err == nil {
			n, _ := fh.Read(head)
			fh.Close()
			head = head[:n]
		}
		in["file"] = &File{Path: abs, Type: http.DetectContentType(head), Size: info.Size()}
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
