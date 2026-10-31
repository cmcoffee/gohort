package service

// Where a service reaches what gohort does itself: transcription, speech and
// embeddings, when their settings name a service connector
// ("service:<name>"), go through it (core/media's seams); a music
// service is a tool an agent can be given.

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/gohort/core/media"
)

func init() {
	media.TranscribeService = transcribeVia
	media.SpeechService = speakVia
	media.EmbedService = embedVia
	media.PageService = renderVia
	media.MailService = mailVia
	flows.ConvertService = convertVia
	media.ModelService = modelTarget
	media.ModelCallNote = noteModelCall
}

// modelTarget reads the approved model connector named name for an LLM tier:
// its dialect, address, default model and context, its token prices, and with
// withKey its credential's key, which the store gives only for an address the
// credential allows (SecureAPI.ModelKey).
func modelTarget(name string, withKey bool) (media.ModelTarget, error) {
	var t media.ModelTarget
	c, ok := GetConnector(RootDB, name)
	if !ok || c.Kind != Kind {
		return t, fmt.Errorf("no model connector named %q", name)
	}
	if !c.Approved {
		return t, fmt.Errorf("the model connector %s is not approved: approve it under Admin > Extensions > Connectors", name)
	}
	s, err := specOf(c)
	if err != nil {
		return t, err
	}
	b, _, err := s.binding("llm.chat")
	if err != nil {
		return t, fmt.Errorf("the connector %s is not a model: %w", name, err)
	}
	t.Dialect, t.Endpoint = b.Dialect, strings.TrimRight(strings.TrimSpace(s.Provider.BaseURL), "/")
	if m := b.Defaults["model"]; m != nil {
		t.Model = fmt.Sprint(m)
	}
	t.Context = number(b.Defaults["context"], 0)
	if cost := b.Cost; cost != nil && (cost.PerMillionInput > 0 || cost.PerMillionOutput > 0) {
		t.InPerMillion, t.OutPerMillion, t.Priced = cost.PerMillionInput, cost.PerMillionOutput, true
	}
	if withKey && s.Provider.Auth.Credential != "" {
		key, err := Secure().ModelKey(s.Provider.Auth.Credential, t.Endpoint+"/")
		if err != nil {
			return t, err
		}
		t.Key = key
	}
	return t, nil
}

// noteModelCall records one model call on the connector's row: whether it
// failed, and what its tokens cost at the connector's prices.
func noteModelCall(name string, in, out int, err error) {
	t, terr := modelTarget(name, false)
	var o outcome
	if terr == nil && t.Priced {
		o.cost, o.priced = float64(in)/1e6*t.InPerMillion+float64(out)/1e6*t.OutPerMillion, true
	}
	noteCall(context.Background(), name, "llm.chat", o, err)
}

// convertVia converts data, a file of format from (an extension), to format
// to with the service connector named name, and gives the converted file.
func convertVia(ctx context.Context, name string, data []byte, from, to string) ([]byte, error) {
	ext := ""
	if from != "" {
		ext = "." + from
	}
	f, err := fileNamed(data, ext)
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
// rule the browser keeps, so a flow calling the contract directly keeps it
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
	var b strings.Builder
	for i, x := range rs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%d. %s\n   %s", i+1, strings.TrimSpace(x.Title), x.URL)
		if s := strings.TrimSpace(x.Snippet); s != "" {
			b.WriteString("\n   " + s)
		}
	}
	return b.String(), nil
}

// transcribeVia transcribes audio with the service connector named name. The
// file keeps the extension the caller gave it: a transcriber reads the format
// from the name.
func transcribeVia(ctx context.Context, name string, audio []byte, filename string) (string, error) {
	f, err := fileNamed(audio, filepath.Ext(filename))
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

// fileNamed writes data to a new file with ext as its extension.
func fileNamed(data []byte, ext string) (*File, error) {
	f, _, err := writeFile(bytes.NewReader(data), defaultMaxBytes)
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

func (t *musicTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("%s needs a session to save the song in", t.Name())
}

func (t *musicTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
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
