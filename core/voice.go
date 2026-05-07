// Package core voice integration.
//
// Two backends, each with two transports:
//
//   STT (whisper.cpp):
//     - HTTP server mode (preferred): POST multipart to <WhisperServerURL>/inference
//       with field "file"; whisper-server returns JSON {"text": "..."}.
//     - Shell-out fallback: invoke `whisper-cli --model X --file Y` and read
//       transcript from stdout.
//
//   TTS (Piper):
//     - HTTP server mode (preferred): POST text body to <PiperServerURL>/;
//       piper.http_server returns audio/wav.
//     - Shell-out fallback: pipe text on stdin to `piper --model X
//       --output_file -`, read WAV from stdout.
//
// Server URL takes precedence per backend when set. The /voice/status
// endpoint reports which transport is active so the UI can label/debug.
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// VoiceMaxAudioBytes caps inbound audio uploads (~10 MB ≈ a couple minutes
// of 16 kHz mono).
const VoiceMaxAudioBytes = 10 * 1024 * 1024

// VoiceMaxTextBytes caps text sent to Piper in one shot. Long content
// should be chunked client-side.
const VoiceMaxTextBytes = 4 * 1024

// VoiceTimeout bounds a single STT or TTS call (shell or HTTP). Generous
// because cold whisper invocations can take a while on slower CPUs.
const VoiceTimeout = 60 * time.Second

// transcribeTransport / speakTransport report which path actually executed
// the request, surfaced in /voice/status for UI/debug clarity.
const (
	transportNone  = "none"
	transportHTTP  = "http"
	transportShell = "shell"
)

// Transcribe runs whisper on audio bytes. Uses WhisperServerURL when set,
// shell-out to whisper-cli otherwise. Returns transcript text.
func Transcribe(ctx context.Context, audio []byte) (string, error) {
	cfg := LoadVoiceConfig()
	if !cfg.Enabled {
		return "", fmt.Errorf("voice is disabled in configuration")
	}
	if cfg.WhisperServerURL != "" {
		return transcribeViaServer(ctx, cfg, audio)
	}
	return transcribeViaShell(ctx, cfg, audio)
}

// Speak runs Piper on text and returns a WAV byte stream. Uses
// PiperServerURL when set, shell-out to piper otherwise.
func Speak(ctx context.Context, text string) ([]byte, error) {
	cfg := LoadVoiceConfig()
	if !cfg.Enabled {
		return nil, fmt.Errorf("voice is disabled in configuration")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("voice: empty text")
	}
	if len(text) > VoiceMaxTextBytes {
		return nil, fmt.Errorf("voice: text too long (%d bytes; max %d)", len(text), VoiceMaxTextBytes)
	}
	if cfg.PiperServerURL != "" {
		return speakViaServer(ctx, cfg, text)
	}
	return speakViaShell(ctx, cfg, text)
}

// --- HTTP server transports ------------------------------------------------

func transcribeViaServer(ctx context.Context, cfg VoiceConfig, audio []byte) (string, error) {
	endpoint := strings.TrimRight(cfg.WhisperServerURL, "/") + "/inference"
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "audio")
	if err != nil {
		return "", fmt.Errorf("voice: multipart: %w", err)
	}
	if _, err := fw.Write(audio); err != nil {
		return "", fmt.Errorf("voice: multipart write: %w", err)
	}
	// whisper-server reads "response_format" too; default is "json" which
	// returns {"text": "..."}, exactly what we want.
	mw.WriteField("response_format", "json")
	mw.Close()

	cctx, cancel := context.WithTimeout(ctx, VoiceTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", fmt.Errorf("voice: build request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("voice: whisper server %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("voice: whisper server %s returned %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		// Some whisper-server builds return raw text; fall back gracefully.
		return strings.TrimSpace(string(respBody)), nil
	}
	return strings.TrimSpace(parsed.Text), nil
}

func speakViaServer(ctx context.Context, cfg VoiceConfig, text string) ([]byte, error) {
	endpoint := strings.TrimRight(cfg.PiperServerURL, "/") + "/"
	cctx, cancel := context.WithTimeout(ctx, VoiceTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, endpoint, strings.NewReader(text))
	if err != nil {
		return nil, fmt.Errorf("voice: build request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voice: piper server %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("voice: piper server %s returned %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	wav, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("voice: read piper response: %w", err)
	}
	if len(wav) < 44 {
		return nil, fmt.Errorf("voice: piper server returned no audio")
	}
	return wav, nil
}

// --- Shell-out transports --------------------------------------------------

func transcribeViaShell(ctx context.Context, cfg VoiceConfig, audio []byte) (string, error) {
	bin := cfg.WhisperBin
	if bin == "" {
		bin = "whisper-cli"
	}
	if cfg.WhisperModel == "" {
		return "", fmt.Errorf("voice: WhisperModel is not configured")
	}
	if _, err := exec.LookPath(bin); err != nil {
		return "", fmt.Errorf("voice: whisper binary %q not found in PATH: %w", bin, err)
	}
	if _, err := os.Stat(cfg.WhisperModel); err != nil {
		return "", fmt.Errorf("voice: whisper model %q not readable: %w", cfg.WhisperModel, err)
	}

	tmp, err := os.CreateTemp("", "gohort-stt-*.audio")
	if err != nil {
		return "", fmt.Errorf("voice: temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(audio); err != nil {
		tmp.Close()
		return "", fmt.Errorf("voice: write temp: %w", err)
	}
	tmp.Close()

	cctx, cancel := context.WithTimeout(ctx, VoiceTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx,
		bin,
		"--model", cfg.WhisperModel,
		"--file", tmpPath,
		"--no-prints",
		"--no-timestamps",
		"--language", "auto",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("voice: whisper-cli failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func speakViaShell(ctx context.Context, cfg VoiceConfig, text string) ([]byte, error) {
	bin := cfg.PiperBin
	if bin == "" {
		bin = "piper"
	}
	if cfg.PiperVoice == "" {
		return nil, fmt.Errorf("voice: PiperVoice is not configured")
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("voice: piper binary %q not found in PATH: %w", bin, err)
	}
	if _, err := os.Stat(cfg.PiperVoice); err != nil {
		return nil, fmt.Errorf("voice: piper voice %q not readable: %w", cfg.PiperVoice, err)
	}

	cctx, cancel := context.WithTimeout(ctx, VoiceTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx,
		bin,
		"--model", cfg.PiperVoice,
		"--output_file", "-",
	)
	cmd.Stdin = strings.NewReader(text)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("voice: piper failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() < 44 {
		return nil, fmt.Errorf("voice: piper produced no audio (stderr: %s)", strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// --- HTTP handlers ---------------------------------------------------------

func VoiceTranscribeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := LoadVoiceConfig()
	if !cfg.Enabled {
		http.Error(w, "voice is disabled", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseMultipartForm(VoiceMaxAudioBytes + 1024); err != nil {
		http.Error(w, "bad multipart upload: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, hdr, err := r.FormFile("audio")
	if err != nil {
		http.Error(w, "missing audio field: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	if hdr.Size > VoiceMaxAudioBytes {
		http.Error(w, fmt.Sprintf("audio too large (%d bytes; max %d)", hdr.Size, VoiceMaxAudioBytes), http.StatusRequestEntityTooLarge)
		return
	}
	audio, err := io.ReadAll(io.LimitReader(file, VoiceMaxAudioBytes+1))
	if err != nil {
		http.Error(w, "read audio: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(audio) > VoiceMaxAudioBytes {
		http.Error(w, "audio too large", http.StatusRequestEntityTooLarge)
		return
	}
	text, err := Transcribe(r.Context(), audio)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"text": text})
}

func VoiceSpeakHandler(w http.ResponseWriter, r *http.Request) {
	cfg := LoadVoiceConfig()
	if !cfg.Enabled {
		http.Error(w, "voice is disabled", http.StatusServiceUnavailable)
		return
	}
	var text string
	switch r.Method {
	case http.MethodGet:
		text = r.URL.Query().Get("text")
	case http.MethodPost:
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, VoiceMaxTextBytes+1024)).Decode(&body); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		text = body.Text
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	wav, err := Speak(r.Context(), text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(wav)))
	w.Write(wav)
}

// VoiceStatusHandler reports per-backend readiness and which transport
// would be used so the UI can label things accurately.
func VoiceStatusHandler(w http.ResponseWriter, r *http.Request) {
	cfg := LoadVoiceConfig()
	transcribeReady, transcribeTransport := transcribeReadiness(cfg)
	speakReady, speakTransport := speakReadiness(cfg)
	resp := map[string]any{
		"enabled":              cfg.Enabled,
		"transcribe":           cfg.Enabled && transcribeReady,
		"transcribe_transport": transcribeTransport,
		"speak":                cfg.Enabled && speakReady,
		"speak_transport":      speakTransport,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func transcribeReadiness(cfg VoiceConfig) (bool, string) {
	if cfg.WhisperServerURL != "" {
		return true, transportHTTP
	}
	if cfg.WhisperModel != "" {
		return true, transportShell
	}
	return false, transportNone
}

func speakReadiness(cfg VoiceConfig) (bool, string) {
	if cfg.PiperServerURL != "" {
		return true, transportHTTP
	}
	if cfg.PiperVoice != "" {
		return true, transportShell
	}
	return false, transportNone
}
