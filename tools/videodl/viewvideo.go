package videodl

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// frame count to sample for visual analysis. Same default as the
// vision pipeline uses for inbound user-attached videos.
const viewFrameCount = 8

func init() { RegisterChatTool(&ViewVideoTool{}) }

// ViewVideoTool downloads a video via yt-dlp, samples frames with
// ffmpeg, and feeds those frames to the LLM on its next round via
// sess.PendingViewImages. The video bytes themselves are NOT carried
// into sess.Videos — this tool is for the LLM's own visual analysis,
// not user-facing delivery. Use download_video for that.
type ViewVideoTool struct{}

func (t *ViewVideoTool) Name() string { return "view_video" }

func (t *ViewVideoTool) Caps() []Capability {
	// Same posture as download_video: ephemeral writes only.
	return []Capability{CapNetwork, CapRead}
}

func (t *ViewVideoTool) Desc() string {
	return "Watch a video at a URL: downloads via yt-dlp, samples frames evenly across the clip, and feeds those frames to you on the next round so you can visually describe / analyze it. Returns container metadata only — no file is attached to your reply. For attaching the actual video file to a reply, use download_video instead."
}

// IsInternetTool hides the tool when private-mode chat strips outbound HTTP.
func (t *ViewVideoTool) IsInternetTool() bool { return true }

func (t *ViewVideoTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"url": {Type: "string", Description: "The video URL. Any site yt-dlp supports (Instagram reel/post, TikTok, YouTube, X tweet with video, Reddit video, etc.)."},
	}
}

func (t *ViewVideoTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("view_video requires a session capable of carrying view-images")
}

func (t *ViewVideoTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("view_video requires a session")
	}
	target := StringArg(args, "url")
	if target == "" {
		return "", fmt.Errorf("url is required")
	}

	data, err := downloadViaYtDlp(target)
	if err != nil {
		return "", err
	}

	frames, err := ExtractVideoFrames(data, viewFrameCount)
	if err != nil {
		return "", fmt.Errorf("sample frames: %w", err)
	}
	if len(frames) == 0 {
		return "", fmt.Errorf("no frames extracted from video")
	}
	for _, f := range frames {
		sess.AppendViewImage(f)
	}

	meta := ExtractVideoMetadata(data)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Sampled %d frames from %s (%s) for visual analysis. The frames will be available to you on the next round; describe what you see.\n", len(frames), target, humanSize(int64(len(data))))
	if meta != "" {
		sb.WriteString("\n")
		sb.WriteString(meta)
		sb.WriteString("\n")
	}
	Debug("[videodl] view_video %s → %d bytes, %d frames", target, len(data), len(frames))
	return sb.String(), nil
}
