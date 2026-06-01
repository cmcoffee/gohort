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
	return "Manage videos end-to-end: find a URL by topic, download for delivery, view for analysis-only, transcribe spoken content, transcode to shrink a workspace file under a size cap. Single entry point — pick the action matching intent. " +
		"actions: find (search the web for a URL), download (fetch a URL AND prepare it for delivery to the user — use when the user shared a URL expecting the file back), view (fetch a URL and just look at frames; NO file delivery — use for research-style \"analyze this video about X\" when the user doesn't need the file), transcribe (workspace audio/video → text; AFTER download when user asks what was said), transcode (re-encode a workspace video to fit under a size cap — use when a previous delivery attempt failed because the file was too large for the transport, typically iMessage's ~20MB limit), help. " +
		"Decision: when a user PASTES a video URL (TikTok, YouTube, etc.) → action=download (they want the file). When a user asks you to RESEARCH or ANALYZE a video → action=view (they want your analysis, not the file). When delivery feedback says an [ATTACH:] was too large → action=transcode with max_size_mb=18, then re-emit [ATTACH:] with the smaller file. When unsure between download/view, prefer download — delivering a file the user didn't need is far better than failing to deliver a file they did."
}

func (t *VideoTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"action": {Type: "string", Description: "One of: find | download | view | transcribe | transcode | help."},
		// find params
		"query":  {Type: "string", Description: "(find) Description of the video to find (topic, creator, distinctive details). Phrase like a search query."},
		"prefer": {Type: "string", Description: "(find) Optional platform hint: youtube | tiktok | vimeo | twitter | reddit | instagram | twitch | facebook."},
		"count":  {Type: "integer", Description: "(find) Optional. Number of candidates to return (1-5, default 1)."},
		// download / view params
		"url": {Type: "string", Description: "(download, view) The video URL to fetch. Any site yt-dlp supports."},
		// transcribe / transcode params (both take a workspace-relative input path)
		"path": {Type: "string", Description: "(transcribe, transcode) Workspace-relative path to the file."},
		// transcode params
		"max_size_mb": {Type: "number", Description: "(transcode) Target maximum output size in megabytes. Default 18 (under iMessage's ~20MB cap with container-overhead headroom). Raise for other transports."},
		"output_path": {Type: "string", Description: "(transcode) Optional workspace-relative output path. Default: <input-basename>-small.mp4 in the same directory."},
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
	case "transcode":
		return transcodeVideoAction(args, sess)
	default:
		return "", fmt.Errorf("video: unknown action %q (expected find | download | view | transcribe | transcode | help)", action)
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

  transcode   Re-encode a workspace video to fit under a size cap.
              Args: path (required, workspace-relative)
                    max_size_mb (optional, default 18)
                    output_path (optional, default <basename>-small.mp4)
              Use when: a previous [ATTACH:] failed because the file
                        was too large for the transport (iMessage caps
                        attachments at ~20MB).
              Behavior: ffprobe duration → compute target bitrate from
                        max_size_mb / duration → ffmpeg encode at
                        target bitrate. If the result is still over
                        cap, retries with progressively smaller
                        resolutions (720p → 540 → 480). Returns the
                        new workspace path ready for [ATTACH:].

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
    → attaches; no transcribe needed.

  Delivery feedback says [ATTACH: clip.mp4] failed (file too large):
    → video(action="transcode", path="clip.mp4", max_size_mb=18)
    → [ATTACH: clip-small.mp4, cleanup=true]
    → user gets the smaller version.`
}
