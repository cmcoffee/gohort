// Package transcribe provides the LLM-facing `transcribe` tool —
// on-demand speech-to-text for any audio or video file in the
// session workspace. Separate from download_video / paperclip
// audio handling (both of which used to auto-transcribe) so the
// LLM only spends an STT round when the user explicitly asks for
// the transcript.
//
// For audio files (mp3/wav/m4a/etc.) the bytes are sent to the
// configured STT endpoint directly. For video files, the audio
// stream is extracted via ffmpeg first, then sent.

package transcribe

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// TranscribeTool's standalone registration is dropped — surfaces via
// the grouped `video` tool (video(action="transcribe")). Type +
// RunWithSession stay public for dispatch. Note: although routed
// under `video`, the tool itself accepts any audio OR video file —
// path semantics are the same (workspace-relative).

// TranscribeTool reads an audio or video file from the session
// workspace and returns the transcribed text. Workspace-scoped: paths
// must resolve inside the session's WorkspaceDir; rejects absolute
// paths that escape it. Same path semantics as workspace(read/attach).
type TranscribeTool struct{}

func (t *TranscribeTool) Name() string { return "transcribe" }

func (t *TranscribeTool) Caps() []Capability {
	return []Capability{CapNetwork, CapRead}
}

func (t *TranscribeTool) Desc() string {
	return "Transcribe an audio or video file in your workspace to text via the configured STT endpoint (whisper). " +
		"Use AFTER a file lands in the workspace (e.g. via download_video, fetch_url, or user attachment) when the user asks what was said / for a transcript / for the spoken content. " +
		"For video files, the audio stream is extracted via ffmpeg first. For audio files (mp3/wav/m4a/etc.), the bytes go directly to STT. " +
		"Returns the recognized text. Don't call without a clear ask — STT costs a round trip and the user often only wants the file itself."
}

func (t *TranscribeTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"path": {Type: "string", Description: "Workspace-relative path to the audio/video file (e.g. 'video-XYZ.mp4' or 'clip.mp3'). Must be inside the session workspace."},
	}
}

// IsInternetTool: STT endpoint is reached over HTTP. Hidden in
// Private mode.
func (t *TranscribeTool) IsInternetTool() bool { return true }

func (t *TranscribeTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("transcribe requires a session with a workspace")
}

func (t *TranscribeTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.WorkspaceDir == "" {
		return "", fmt.Errorf("transcribe requires a session with a workspace")
	}
	if !GetTranscribeConfig().Enabled {
		return "", fmt.Errorf("transcription endpoint is not configured — set it via `gohort --setup` (Audio transcription section)")
	}
	relPath := strings.TrimSpace(StringArg(args, "path"))
	if relPath == "" {
		return "", fmt.Errorf("path is required")
	}
	// Reject absolute paths and any traversal that escapes the
	// workspace. Same posture as workspace(read).
	cleaned := filepath.Clean(relPath)
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, string(filepath.Separator)+"..") {
		return "", fmt.Errorf("path must be inside the workspace; got %q", relPath)
	}
	full := filepath.Join(sess.WorkspaceDir, cleaned)
	st, err := os.Stat(full)
	if err != nil {
		return "", fmt.Errorf("file not found: %w", err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("path is a directory, not a file")
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("file is empty")
	}

	// Decide whether we need ffmpeg-extract: audio types go direct,
	// video types extract first.
	mime := http.DetectContentType(data)
	ext := strings.ToLower(filepath.Ext(cleaned))
	isVideo := strings.HasPrefix(mime, "video/") ||
		ext == ".mp4" || ext == ".mov" || ext == ".mkv" || ext == ".webm" || ext == ".avi" || ext == ".m4v" || ext == ".3gp"

	audioBytes := data
	audioName := filepath.Base(cleaned)
	if isVideo {
		extracted, eerr := ExtractVideoAudio(data)
		if eerr != nil {
			return "", fmt.Errorf("extract audio from video: %w", eerr)
		}
		audioBytes = extracted
		audioName = "audio.mp3"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	text, err := Transcribe(ctx, audioBytes, audioName)
	if err != nil {
		return "", fmt.Errorf("transcribe: %w", err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("transcription returned empty text (clip may have no speech)")
	}
	Log("[transcribe.tool] %s → %d chars", cleaned, len(text))
	// Frame the transcript with guidance so the LLM uses it
	// correctly. The default failure mode without this: the model
	// dumps the entire transcript verbatim into the reply ("Transcript
	// from the clip: <full text>") even when the user only wanted a
	// short description of the video. Make it explicit: this is
	// reference content for the LLM's own understanding; only echo it
	// verbatim when the user explicitly asked for the transcript.
	return "[transcript for your reference — use to understand / describe the video; " +
		"do NOT echo this verbatim to the user unless they explicitly asked for the transcript " +
		"or to see what was said]\n\n" + text, nil
}
