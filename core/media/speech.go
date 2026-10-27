// Speech (TTS): the reverse of transcribe.go. Turns text into audio through an
// OpenAI-compatible /audio/speech endpoint, which the local speech servers
// people run (Kokoro-FastAPI, openedai-speech) and the real OpenAI API all
// serve, so a local deployment just needs the endpoint URL.
//
// Disabled by default: until the admin UI configures an endpoint, every call
// returns an error and callers send their text without a voice.

package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cmcoffee/gohort/core/textutil"
	"github.com/cmcoffee/snugforge/nfo"
)

// SpeechTable is the kvlite table holding the persisted speech config. Here
// rather than in core's tables.go, with the rest of speech, so core's
// namespace does not grow for a subsystem that lives in this package.
const SpeechTable = "speech_config"

// SpeechConfig holds the endpoint, model and voice for the OpenAI-compatible
// speech API. Model and Voice name what the server offers: Kokoro-FastAPI
// takes "kokoro" and a voice such as "af_heart", OpenAI "tts-1" and "alloy".
type SpeechConfig struct {
	Endpoint string `json:"endpoint"` // base URL, e.g. http://localhost:8880/v1
	Model    string `json:"model"`    // optional for servers with one model
	Voice    string `json:"voice"`    // optional for servers with a default voice
	APIKey   string `json:"api_key"`  // optional bearer token (real OpenAI / authenticated proxies)
	Enabled  bool   `json:"enabled"`  // false → calls return an error and callers send text only
}

var (
	speechCfgMu sync.RWMutex
	speechCfg   SpeechConfig
)

// SetSpeechConfig installs the process-wide speech config.
func SetSpeechConfig(cfg SpeechConfig) {
	speechCfgMu.Lock()
	speechCfg = cfg
	speechCfgMu.Unlock()
}

// GetSpeechConfig returns the current speech config.
func GetSpeechConfig() SpeechConfig {
	speechCfgMu.RLock()
	defer speechCfgMu.RUnlock()
	return speechCfg
}

// LoadSpeechConfigFromDB reads the persisted speech config and installs it.
// Silent no-op when the DB handle is nil or no record exists.
func LoadSpeechConfigFromDB(db ConfigStore) {
	if db == nil {
		return
	}
	var cfg SpeechConfig
	if !db.Get(SpeechTable, "current", &cfg) {
		return
	}
	SetSpeechConfig(cfg)
}

// speechMaxBytes bounds what a speech server may send back. A few minutes of
// mp3 is a few megabytes; anything near this is a server answering with
// something other than the clip that was asked for.
const speechMaxBytes = 32 << 20

// Speak turns text into audio with the configured speech endpoint, returning
// the audio and its file extension (".mp3", ".wav", ".ogg").
//
// Returns an error when speech is disabled or unreachable; callers treat that
// as "send the text without a voice", never as a failed reply.
func Speak(ctx context.Context, text string) ([]byte, string, error) {
	return SpeakWith(ctx, GetSpeechConfig(), text)
}

// SpeakWith is Speak against an explicit config, for the admin Test button,
// which tries the form as typed before it is saved.
func SpeakWith(ctx context.Context, cfg SpeechConfig, text string) ([]byte, string, error) {
	if !cfg.Enabled {
		return nil, "", fmt.Errorf("speech disabled")
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, "", fmt.Errorf("speech endpoint not configured")
	}
	if text = strings.TrimSpace(text); text == "" {
		return nil, "", fmt.Errorf("speech: no text to speak")
	}
	req := map[string]string{"input": text, "response_format": "mp3"}
	if cfg.Model != "" {
		req["model"] = cfg.Model
	}
	if cfg.Voice != "" {
		req["voice"] = cfg.Voice
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, "", err
	}
	url := strings.TrimRight(cfg.Endpoint, "/") + "/audio/speech"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	started := time.Now()
	// 2 minutes is the ceiling for a caller with no deadline of its own; a
	// long reply on a CPU-only server takes a while, and the request is
	// context-aware, so a caller's shorter budget wins.
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(httpReq)
	if err != nil {
		return nil, "", fmt.Errorf("speech request failed (%s): %w", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, speechMaxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("speech: reading the audio: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("speech: HTTP %d: %s", resp.StatusCode, clipSpeechError(body))
	}
	if len(body) > speechMaxBytes {
		return nil, "", fmt.Errorf("speech: the server sent more than %d MB", speechMaxBytes>>20)
	}
	ext, ok := speechExt(body)
	if !ok {
		// A 200 whose body is an error page or a JSON complaint. Played back,
		// it would be a voice note of silence; reported, it says what is wrong.
		return nil, "", fmt.Errorf("speech: the server answered without audio: %s", clipSpeechError(body))
	}
	nfo.Debug("[speech] %d chars -> %d bytes %s in %s", len(text), len(body), ext, time.Since(started).Round(time.Millisecond))
	return body, ext, nil
}

// speechExt names the audio a speech server sent back, and says whether it is
// audio at all. mp3 is what was asked for, and a bare mp3 stream (no ID3 tag)
// sniffs as octet-stream, so that is taken as mp3; text and JSON are not.
func speechExt(b []byte) (string, bool) {
	if len(b) == 0 {
		return "", false
	}
	ct := http.DetectContentType(b)
	switch {
	case ct == "audio/wave":
		return ".wav", true
	case ct == "application/ogg":
		return ".ogg", true
	case ct == "audio/mpeg", ct == "application/octet-stream":
		return ".mp3", true
	case strings.HasPrefix(ct, "audio/"):
		return ".mp3", true
	}
	return "", false
}

// clipSpeechError keeps a server's complaint short enough to be an error.
func clipSpeechError(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// SpeechMIME is the media type a browser plays a clip of this extension as.
func SpeechMIME(ext string) string {
	switch ext {
	case ".wav":
		return "audio/wav"
	case ".ogg":
		return "audio/ogg"
	}
	return "audio/mpeg"
}

// Read aloud: the web chat's 🔊 on an agent's reply (ReadAloudScript). The
// page posts the reply's text and plays the clip the speech server makes of
// it. core mounts ServeReadAloud at ReadAloudPath and hands it the signed-in
// user, since this package cannot read a session.

// ReadAloudPath is where the chat page posts a reply to hear it.
const ReadAloudPath = "/api/speak"

// readAloudMaxChars bounds one read-aloud: about seven minutes of speech. A
// longer reply is read to the end of the last sentence that fits.
const readAloudMaxChars = 6000

// ServeReadAloud answers POST {"text": ...} with the reply spoken. user is
// the signed-in user, empty when nobody is.
func ServeReadAloud(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if user == "" {
		http.Error(w, "sign in first", http.StatusForbidden)
		return
	}
	if !GetSpeechConfig().Enabled {
		http.Error(w, "speech is not set up: an admin turns it on under Capabilities, Speech (TTS)", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	text := readAloudText(req.Text)
	if text == "" {
		http.Error(w, "nothing in this reply can be read aloud", http.StatusBadRequest)
		return
	}
	audio, ext, err := Speak(r.Context(), text)
	if err != nil {
		if r.Context().Err() == nil {
			nfo.Log("[speech] read aloud for %s failed: %v", user, err)
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", SpeechMIME(ext))
	w.Header().Set("Cache-Control", "no-store")
	w.Write(audio)
}

// readAloudText is the part of a reply worth reading aloud, cut at the end of
// the last sentence that fits readAloudMaxChars, or at a word when none does.
func readAloudText(reply string) string {
	text := textutil.SpeakableText(reply)
	if utf8.RuneCountInString(text) <= readAloudMaxChars {
		return text
	}
	cut := string([]rune(text)[:readAloudMaxChars])
	if i := strings.LastIndexAny(cut, ".!?"); i > len(cut)/2 {
		return cut[:i+1]
	}
	if i := strings.LastIndexAny(cut, " \n"); i > 0 {
		return strings.TrimSpace(cut[:i])
	}
	return cut
}

// ReadAloudScript is the `<script>` that puts 🔊 on every agent reply in the
// web chat, for pages to embed in Page.ExtraHeadHTML the way they embed
// TranscribeRuntimeFlagScript. Empty while speech is off, so the button only
// exists where pressing it can work.
//
// Pressed, the button turns into a spinner with elapsed seconds while the clip
// is made (a long reply on a CPU-only server takes a while), then into a stop
// button while it plays. Pressing it again stops it, at either stage; pressing
// another reply's stops this one. A failure leaves a warning sign, with the
// reason on hover.
func ReadAloudScript() string {
	if !GetSpeechConfig().Enabled {
		return ""
	}
	return "<script>" + readAloudJS + "</script>"
}

// readAloudJS is ReadAloudScript's body, a variable of its own so a test can
// run it.
const readAloudJS = `(function(){
  var frames = '⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏';
  var idle = '🔊', stopGlyph = '⏹', failGlyph = '⚠';
  var playing = null;
  function stop() {
    var p = playing;
    if (!p) return;
    playing = null;
    if (p.timer) clearInterval(p.timer);
    if (p.ctl) p.ctl.abort();
    if (p.audio) { p.audio.pause(); p.audio.src = ''; }
    if (p.url) URL.revokeObjectURL(p.url);
    if (p.button) { p.button.textContent = idle; p.button.title = 'Read aloud'; }
  }
  function press(ctx) {
    var again = playing && playing.bubble === ctx.bubble;
    stop();
    if (again) return;
    var text = String(ctx.getText() || '');
    if (!text.trim()) return;
    var btn = ctx.button || null;
    var p = playing = {bubble: ctx.bubble, button: btn, ctl: new AbortController()};
    var started = Date.now(), frame = 0;
    if (btn) {
      btn.title = 'Making the audio. Press to cancel';
      p.timer = setInterval(function() {
        var secs = Math.round((Date.now() - started) / 1000);
        btn.textContent = frames.charAt(frame++ % frames.length) + (secs >= 3 ? ' ' + secs + 's' : '');
      }, 100);
    }
    fetch('` + ReadAloudPath + `', {method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({text: text}), signal: p.ctl.signal})
      .then(function(r) {
        if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
        return r.blob();
      })
      .then(function(blob) {
        if (playing !== p) return;
        clearInterval(p.timer); p.timer = null; p.ctl = null;
        p.url = URL.createObjectURL(blob);
        p.audio = new Audio(p.url);
        p.audio.onended = function() { if (playing === p) stop(); };
        if (btn) { btn.textContent = stopGlyph; btn.title = 'Stop reading'; }
        return p.audio.play();
      })
      .catch(function(e) {
        if (e && e.name === 'AbortError') return;
        if (playing === p) stop();
        if (btn) { btn.textContent = failGlyph; btn.title = 'Read aloud failed: ' + String(e && e.message || e).trim() + ' Press to try again.'; }
      });
  }
  function register() {
    if (!window.uiRegisterBubbleAction) { setTimeout(register, 50); return; }
    window.uiRegisterBubbleAction({role: 'assistant', label: idle, title: 'Read aloud', onclick: press});
  }
  register();
})();`
