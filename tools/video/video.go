// Package video provides the grouped LLM-facing `video` tool —
// a single dispatch surface that covers the full video lifecycle:
// find a URL by topic, download a known URL, transcribe a downloaded
// file. Replaces the three standalone tools (find_video,
// download_video, transcribe) so the LLM sees one consolidated tool
// instead of three similar names.
//
// Action map:
//
//	video(action="find", query=..., prefer=?, count=?)
//	video(action="download", url=...)
//	video(action="transcribe", path=...)
//	video(action="help")
//
// Mirrors the agents / workspace grouped-tool pattern in the rest of
// the codebase. The underlying implementation logic lives in the
// per-action packages (videofind, videodl, transcribe); this file is
// the thin dispatcher.

package video

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/transcribe"
	"github.com/cmcoffee/gohort/tools/videodl"
	"github.com/cmcoffee/gohort/tools/videofind"
)

func init() { RegisterChatTool(&VideoTool{}) }

// VideoTool routes calls to find / download / transcribe based on
// the `action` parameter. Capabilities are the UNION of the
// underlying tools' caps (network for find + download, network + read
// for transcribe).
type VideoTool struct{}

func (t *VideoTool) Name() string { return "video" }

func (t *VideoTool) Caps() []Capability {
	// Union of underlying caps. CapNetwork covers find/download/
	// transcribe; CapRead covers reading workspace files in
	// transcribe.
	return []Capability{CapNetwork, CapRead}
}

// IsInternetTool: every action touches outbound HTTP (search, yt-dlp,
// or STT endpoint). Hidden in Private mode.
func (t *VideoTool) IsInternetTool() bool { return true }

func (t *VideoTool) Desc() string {
	return "Manage videos end-to-end: find a URL by topic, download for delivery, view for analysis-only, transcribe spoken content. Single entry point — pick the action matching intent. " +
		"actions: find (search the web for a URL), download (fetch a URL AND prepare it for delivery to the user — use when the user shared a URL expecting the file back), view (fetch a URL and just look at frames; NO file delivery — use for research-style \"analyze this video about X\" when the user doesn't need the file), transcribe (workspace audio/video → text; AFTER download when user asks what was said), help. " +
		"Decision: when a user PASTES a video URL (TikTok, YouTube, etc.) → action=download (they want the file). When a user asks you to RESEARCH or ANALYZE a video → action=view (they want your analysis, not the file). When unsure, prefer download — delivering a file the user didn't need is far better than failing to deliver a file they did."
}

func (t *VideoTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"action": {Type: "string", Description: "One of: find | download | transcribe | help."},
		// find params
		"query": {Type: "string", Description: "(find) Description of the video to find (topic, creator, distinctive details). Phrase like a search query."},
		"prefer": {Type: "string", Description: "(find) Optional platform hint: youtube | tiktok | vimeo | twitter | reddit | instagram | twitch | facebook."},
		"count": {Type: "integer", Description: "(find) Optional. Number of candidates to return (1-5, default 1)."},
		// download params
		"url": {Type: "string", Description: "(download) The video URL to fetch. Any site yt-dlp supports."},
		// transcribe params
		"path": {Type: "string", Description: "(transcribe) Workspace-relative path to the audio/video file to transcribe."},
	}
}

func (t *VideoTool) Run(args map[string]any) (string, error) {
	// Without a session, only `find` (which doesn't need workspace
	// or session-bound delivery) and `help` can run. download and
	// transcribe both need sess.WorkspaceDir.
	action := strings.TrimSpace(StringArg(args, "action"))
	switch action {
	case "find":
		var ft videofind.FindVideoTool
		return ft.Run(args)
	case "", "help":
		return videoToolHelp(), nil
	}
	return "", fmt.Errorf("video(action=%q) requires a session with a workspace — only `find` and `help` work without one", action)
}

func (t *VideoTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	action := strings.TrimSpace(StringArg(args, "action"))
	switch action {
	case "", "help":
		return videoToolHelp(), nil
	case "find":
		var ft videofind.FindVideoTool
		return ft.Run(args)
	case "download":
		var dt videodl.DownloadVideoTool
		return dt.RunWithSession(args, sess)
	case "view":
		var vt videodl.ViewVideoTool
		return vt.RunWithSession(args, sess)
	case "transcribe":
		var tt transcribe.TranscribeTool
		return tt.RunWithSession(args, sess)
	default:
		return "", fmt.Errorf("video: unknown action %q (expected find | download | transcribe | help)", action)
	}
}

func videoToolHelp() string {
	return `video — manage videos end-to-end. Single tool covering the full lifecycle.

ACTIONS:

  find        Search the web for a video URL by topic.
              Args: query (required), prefer (optional platform hint),
                    count (optional, return N candidates, default 1)
              Returns: best URL with title + source.

  download    Fetch a known video URL via yt-dlp FOR DELIVERY.
              Args: url (required)
              Use when: user shared a URL and expects the file back.
              Behavior: stores in workspace, samples frames + metadata,
                        attaches the file via follow-up workspace(attach).

  view        Fetch a known video URL just for analysis — NO delivery.
              Args: url (required)
              Use when: research-style "look at this video about X and
                        tell me what's argued"; user wants your insight,
                        not the file. Phantom rule of thumb: if the
                        user pasted a URL in a casual reply, prefer
                        download — pasting implies they want the file.
              Behavior: samples frames into your view queue, returns
                        metadata; no workspace file, no attach.

  transcribe  Convert an audio/video file's speech to text.
              Args: path (required, workspace-relative)
              Behavior: extracts audio if path is a video, sends to
                        whisper STT endpoint, returns text. Only call
                        when user explicitly asked what was said.

  help        Show this message.

TYPICAL FLOWS:

  User: "Find me the bear-cooking-pasta TikTok"
    → video(action="find", query="bear cooking pasta tiktok", prefer="tiktok")
    → video(action="download", url=<from find result>)
    → file attached + brief description

  User: "Here's a clip <YouTube URL> — what does she say?"
    → video(action="download", url=<URL>)         (auto-attaches the clip)
    → video(action="transcribe", path=<from download>)
    → reply includes the transcript + the attached file

  User: "Send me this <URL>"
    → video(action="download", url=<URL>)
    → attaches; no transcribe needed.`
}
