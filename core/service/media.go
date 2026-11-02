package service

// Where a service reaches what gohort does itself: transcription, speech (and
// its voice list) and embeddings, when their settings name a service
// connector by its name, go through it (core/media's seams); a music
// service is a tool an agent can be given.

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/gohort/core/media"
)

func init() {
	media.TranscribeService = transcribeVia
	media.SpeechService = speakVia
	media.SpeechVoicesService = voicesVia
	media.EmbedService = embedVia
	media.ServiceAddress = serviceAddress
	media.PageService = renderVia
	media.MailService = mailVia
	flows.ConvertService = convertVia
}

// convertVia converts data, a file of format from (an extension), to format
// to with the service connector named name, and gives the converted file.
func convertVia(ctx context.Context, name string, data []byte, from, to string) ([]byte, error) {
	ext := ""
	if from != "" {
		ext = "." + from
	}
	f, err := fileNamed(data, ext, defaultMaxBytes)
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Path)
	in := map[string]any{"file": f, "to": to}
	if from != "" {
		in["from"] = from
	}
	res, err := Call(ctx, nil, name, "file.convert", in)
	if err != nil {
		return nil, err
	}
	out := res["file"].File
	defer os.Remove(out.Path)
	return os.ReadFile(out.Path)
}

// mailVia sends one email with the service connector named name: from the
// address given, or the connector's own when it is blank.
func mailVia(ctx context.Context, name, from, to, subject, body string) error {
	in := map[string]any{"to": to, "subject": subject, "body": body}
	if f := strings.TrimSpace(from); f != "" {
		in["from"] = f
	}
	_, err := Call(ctx, nil, name, "mail.send", in)
	return err
}

// renderVia renders the page at url with the service connector named name and
// gives at most maxChars of its text. Only a public web address is sent: the
// rule the browser keeps, so a call through the contract directly keeps it
// too.
func renderVia(ctx context.Context, name, url string, maxChars int) (string, error) {
	if err := RefuseNonPublicHost(url); err != nil {
		return "", err
	}
	res, err := Call(ctx, nil, name, "page.render", map[string]any{"url": url})
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(res["text"].Text)
	// The title goes first, unless the text already opens with it (a
	// Markdown heading, say).
	first, _, _ := strings.Cut(text, "\n")
	if t := strings.TrimSpace(res["title"].Text); t != "" && strings.TrimSpace(strings.TrimLeft(first, "# ")) != t {
		text = t + "\n\n" + text
	}
	if r := []rune(text); maxChars > 0 && len(r) > maxChars {
		text = string(r[:maxChars])
	}
	return text, nil
}

// embedVia embeds texts with the service connector named name, in model when
// one is set (the settings store the one the service declared, so a later
// change to its default cannot move the vectors into another space unseen).
func embedVia(ctx context.Context, name, model string, texts []string) ([][]float32, error) {
	in := map[string]any{"texts": texts}
	if m := strings.TrimSpace(model); m != "" {
		in["model"] = m
	}
	res, err := Call(ctx, nil, name, "embed.text", in)
	if err != nil {
		return nil, err
	}
	return Vectors(res)
}

// serviceAddress is the base URL of the service connector named name, or
// "".
func serviceAddress(name string) string {
	c, ok := GetConnector(RootDB, name)
	if !ok || c.Kind != Kind {
		return ""
	}
	s, err := specOf(c)
	if err == nil {
		s, err = settledFor(context.Background(), name, s)
	}
	if err != nil {
		return ""
	}
	return strings.TrimSpace(s.Provider.BaseURL)
}

// Search searches with the service connector named name and gives the
// results in the numbered shape every search provider gives the web_search
// tool ("1. Title\n   URL\n   Snippet"), or "No results found."
func Search(ctx context.Context, name, query string) (string, error) {
	res, err := Call(ctx, nil, name, "web.search", map[string]any{"query": query})
	if err != nil {
		return "", err
	}
	rs, err := SearchResults(res)
	if err != nil {
		return "", err
	}
	if len(rs) == 0 {
		return "No results found.", nil
	}
	// No more than the spec asks for: an engine that takes no count (SearXNG)
	// answers with every result it has.
	if c, ok := GetConnector(RootDB, name); ok {
		if n, _ := strconv.Atoi(Default(c, "web.search", "count")); n > 0 && len(rs) > n {
			rs = rs[:n]
		}
	}
	var b strings.Builder
	for i, x := range rs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%d. %s\n   %s", i+1, strings.TrimSpace(x.Title), x.URL)
		if s := PublishedTag(x.Published) + strings.TrimSpace(x.Snippet); s != "" {
			b.WriteString("\n   " + s)
		}
	}
	return MarkUnopenable(b.String()), nil
}

// ImageSearch finds pictures with the service connector named name, no more
// than the spec's count.
func ImageSearch(ctx context.Context, name, query string) ([]ImageResult, error) {
	res, err := Call(ctx, nil, name, "image.search", map[string]any{"query": query})
	if err != nil {
		return nil, err
	}
	rs, err := ImageResults(res)
	if err != nil {
		return nil, err
	}
	if c, ok := GetConnector(RootDB, name); ok {
		if n, _ := strconv.Atoi(Default(c, "image.search", "count")); n > 0 && len(rs) > n {
			rs = rs[:n]
		}
	}
	return rs, nil
}

// transcribeVia transcribes audio with the service connector named name. The
// file keeps the extension the caller gave it: a transcriber reads the format
// from the name.
func transcribeVia(ctx context.Context, name string, audio []byte, filename string) (string, error) {
	f, err := fileNamed(audio, filepath.Ext(filename), audioMaxBytes)
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Path)
	in := map[string]any{"audio": f}
	if p := media.TranscribePrompt(ctx); p != "" {
		in["prompt"] = p
	}
	res, err := Call(ctx, nil, name, "speech.transcribe", in)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res["text"].Text), nil
}

// speakVia speaks text with the service connector named name, in voice when
// one is set, and returns the audio and its extension.
func speakVia(ctx context.Context, name, text, voice string) ([]byte, string, error) {
	in := map[string]any{"text": text}
	if v := strings.TrimSpace(voice); v != "" {
		in["voice"] = v
	}
	res, err := Call(ctx, nil, name, "speech.synthesize", in)
	if err != nil {
		return nil, "", err
	}
	f := res["audio"].File
	defer os.Remove(f.Path)
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, "", err
	}
	ext := filepath.Ext(f.Path)
	if ext == "" {
		ext = ".mp3"
	}
	return data, ext, nil
}

// audioMaxBytes is the most audio given to a transcriber: the 100 MB the
// media jobs extract from a video at most. The connector's own limit still
// bounds what it sends.
const audioMaxBytes = 100 << 20

// voicesVia lists the voices the service connector named name offers, or
// none when it declares no list.
func voicesVia(ctx context.Context, name string) ([]media.Voice, error) {
	c, ok := GetConnector(RootDB, name)
	if !ok || !Provides(c, "speech.voices") {
		return nil, nil
	}
	res, err := Call(ctx, nil, name, "speech.voices", map[string]any{})
	if err != nil {
		return nil, err
	}
	return Voices(res)
}

// fileNamed writes data, at most limit bytes of it, to a new file with ext as
// its extension.
func fileNamed(data []byte, ext string, limit int64) (*File, error) {
	f, _, err := writeFile(bytes.NewReader(data), limit)
	if err != nil {
		return nil, err
	}
	if ext = strings.ToLower(strings.TrimSpace(ext)); ext != "" && filepath.Ext(f.Path) != ext {
		renamed := strings.TrimSuffix(f.Path, filepath.Ext(f.Path)) + ext
		if os.Rename(f.Path, renamed) == nil {
			f.Path = renamed
		}
	}
	return f, nil
}

var (
	musicToolsMu sync.Mutex
	musicTools   = map[string]bool{}
)

// registerMusicTool makes a music service a tool agents can be given. The
// registry keeps a tool once registered; one whose connector is gone or not
// approved says so when it is called.
func registerMusicTool(name string) {
	musicToolsMu.Lock()
	defer musicToolsMu.Unlock()
	if musicTools[name] {
		return
	}
	musicTools[name] = true
	RegisterChatTool(&musicTool{connector: name})
}

var unsafeToolChars = regexp.MustCompile(`[^a-z0-9_]+`)

// musicTool is generate_music_<connector>.
type musicTool struct{ connector string }

func (t *musicTool) Name() string {
	return "generate_music_" + strings.Trim(unsafeToolChars.ReplaceAllString(strings.ToLower(t.connector), "_"), "_")
}

func (t *musicTool) Desc() string {
	return "Make a song with the " + t.connector + " music service from a prompt, and lyrics, a style and a title when you have them. The song is saved in your workspace and goes out with your reply."
}

func (t *musicTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"prompt": {Type: "string", Description: "What the song is: its mood, instruments, subject. Required."},
		"lyrics": {Type: "string", Description: "The words to sing, when the song has them."},
		"style":  {Type: "string", Description: "A genre or style, e.g. lo-fi hip hop, folk ballad."},
		"title":  {Type: "string", Description: "The song's title; it names the file."},
	}
}

// Caps: a service on the network goes online; a local program runs.
func (t *musicTool) Caps() []Capability {
	if c, ok := GetConnector(RootDB, t.connector); ok {
		if s, err := Parse(c.Spec); err == nil && s.Provider.Transport == "command" && !s.Provider.Network {
			return []Capability{CapExecute}
		}
	}
	return []Capability{CapNetwork}
}

// offered reports whether the Music page offers this connector to agents,
// and no change waits on it.
func (t *musicTool) offered() bool {
	if _, ok := GetConnector(RootDB, t.connector); !ok || held(t.connector) {
		return false
	}
	var agents []string
	RootDB.Get(MusicTable, "agents", &agents)
	for _, a := range agents {
		if a == t.connector {
			return true
		}
	}
	return false
}

// SchemaWithSession offers the tool only while the Music page does, to an
// agent that may reach its connection.
func (t *musicTool) SchemaWithSession(sess *ToolSession) (string, map[string]ToolParam) {
	if !t.offered() || sess.ConnectionBarred(t.connector) != nil {
		return "", nil
	}
	return t.Desc(), t.Params()
}

func (t *musicTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("%s needs a session to save the song in", t.Name())
}

func (t *musicTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if !t.offered() {
		return "", fmt.Errorf("%s is not offered: the Music settings no longer offer the connector %s to agents", t.Name(), t.connector)
	}
	in := map[string]any{}
	for _, k := range []string{"prompt", "lyrics", "style", "title"} {
		if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
			in[k] = strings.TrimSpace(v)
		}
	}
	if in["prompt"] == nil {
		return "", fmt.Errorf("say what the song is in prompt")
	}
	ctx := context.Background()
	if sess != nil {
		ctx = sess.Context()
	}
	res, err := Call(ctx, sess, t.connector, "music.generate", in)
	if err != nil {
		return "", err
	}
	f := res["audio"].File
	defer os.Remove(f.Path)
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return "", err
	}
	if sess == nil || strings.TrimSpace(sess.WorkspaceDir) == "" {
		return "", fmt.Errorf("the song was made, but there is no workspace to save it in")
	}
	base := "song"
	if title, _ := in["title"].(string); title != "" {
		base = strings.Trim(unsafeToolChars.ReplaceAllString(strings.ToLower(title), "-"), "-_")
	}
	if base == "" {
		base = "song"
	}
	ext := filepath.Ext(f.Path)
	if ext == "" {
		ext = ".mp3"
	}
	rel := base + ext
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
		rel = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
	sess.AddStagedFiles([]string{rel})
	sess.AppendVideo(base64.StdEncoding.EncodeToString(data))
	return fmt.Sprintf("Made the song, saved as %s in your workspace (%d KB). It is attached to your reply.", rel, len(data)>>10), nil
}

// bedrockRuntimeRE is a region's Bedrock runtime host.
var bedrockRuntimeRE = regexp.MustCompile(`^https://bedrock-runtime\.([a-z0-9-]+)\.amazonaws\.com$`)

// bedrockHost is where a Bedrock model at a region's runtime host is asked
// over api: the Messages API answers on that region's bedrock-mantle host
// instead. Any other address (a private endpoint) is asked as it is.
func bedrockHost(base, api string) string {
	if api != "messages" {
		return base
	}
	if m := bedrockRuntimeRE.FindStringSubmatch(base); m != nil {
		return "https://bedrock-mantle." + m[1] + ".api.aws"
	}
	return base
}
