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
	"github.com/cmcoffee/gohort/core/media"
)

func init() {
	media.TranscribeService = transcribeVia
	media.SpeechService = speakVia
	media.EmbedService = embedVia
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
