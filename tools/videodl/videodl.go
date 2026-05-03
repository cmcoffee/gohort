// Package videodl provides a chat tool that downloads videos from URLs
// using yt-dlp. yt-dlp handles signature-juggling, login redirects, CDN
// URL extraction for Instagram, TikTok, YouTube, Twitter/X, and ~1000
// other sites — the production-grade extractor that's actively maintained
// against site changes.
//
// On success the tool stuffs the full video bytes into sess.Videos so
// apps that deliver outbound attachments (phantom iMessage repost) can
// forward the clip back to the user. Container metadata (mime, duration,
// dimensions, recorded date, GPS-resolved location, camera) is rolled
// into the tool result text so the LLM can describe what it's sending
// without needing to see frames.
//
// Note: the tool deliberately does not stuff sampled frames into
// sess.Images. Sess.Images is the outbound-attachment channel; frames
// in there get delivered to the user alongside the video file, which
// is double-delivery noise. Visual analysis of an arbitrary URL
// (LLM actually watching the video) is a separate feature that needs
// frames injected into Message.Videos on a follow-up round, which is
// not built yet.
//
// Requires yt-dlp on PATH. Falls back to a clear error if absent.
package videodl

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	downloadTimeout  = 120 * time.Second
	downloadMaxBytes = 200 * 1024 * 1024 // 200 MB hard cap
)

func init() { RegisterChatTool(&DownloadVideoTool{}) }

// DownloadVideoTool wraps yt-dlp.
type DownloadVideoTool struct{}

func (t *DownloadVideoTool) Name() string { return "download_video" }

func (t *DownloadVideoTool) Caps() []Capability {
	// CapWrite is intentionally omitted: the tool writes to an ephemeral
	// temp dir that's torn down before the call returns. The writes are
	// implementation detail, not lasting state — same posture as
	// find_image / generate_image / screenshot_page. Requiring CapWrite
	// would force chat sessions to opt into the file-write tier just
	// to download a video, which doesn't match the actual blast radius.
	return []Capability{CapNetwork, CapRead}
}

func (t *DownloadVideoTool) Desc() string {
	return "Download a video from a URL via yt-dlp and attach the file to your reply. Supports Instagram, TikTok, YouTube, Twitter/X, Reddit, Vimeo, Facebook, and roughly 1000 other sites. Returns container metadata (mime, dimensions, duration, recorded date, GPS-resolved location, camera) the calling app can use to describe the file. DRM-protected sources (Netflix, paid YouTube, etc.) return an error."
}

// IsInternetTool ensures private-mode chat hides this tool — it makes
// outbound HTTP requests via yt-dlp.
func (t *DownloadVideoTool) IsInternetTool() bool { return true }

func (t *DownloadVideoTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"url": {Type: "string", Description: "The video URL. Any site yt-dlp supports (Instagram reel/post, TikTok, YouTube, X tweet with video, Reddit video, etc.)."},
	}
}

// Run is the no-session fallback. Without a session there's nowhere to
// hand the video — the bytes need to flow into sess.Videos / sess.Images
// for the calling app to use.
func (t *DownloadVideoTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("download_video requires a session capable of carrying image/video attachments")
}

// RunWithSession does the actual download.
func (t *DownloadVideoTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("download_video requires a session")
	}
	target := StringArg(args, "url")
	if target == "" {
		return "", fmt.Errorf("url is required")
	}

	data, err := downloadViaYtDlp(target)
	if err != nil {
		return "", err
	}

	// Carry the full video bytes for outbound delivery — phantom or any
	// future app that handles sess.Videos can attach the clip to its
	// reply. Apps that don't consume sess.Videos just ignore them.
	sess.AppendVideo(base64.StdEncoding.EncodeToString(data))

	// Sample frames into PendingViewImages so the LLM can actually
	// describe what it's sending in its reply ("here's the clip, it's
	// a 12s skateboard trick at sunset"). These frames go to the LLM
	// only — they are NOT delivered to the user. Frame failures are
	// non-fatal; the metadata fallback still gives the LLM something.
	frames, ferr := ExtractVideoFrames(data, viewFrameCount)
	if ferr == nil {
		for _, f := range frames {
			sess.AppendViewImage(f)
		}
	} else {
		Debug("[videodl] frame sampling failed for %s: %v", target, ferr)
	}

	// Container metadata (mime, dimensions, duration, GPS-resolved
	// location, camera) gets rolled into the tool result alongside the
	// frames so the LLM has both visual and structured context.
	meta := ExtractVideoMetadata(data)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Downloaded video from %s (%s). It will be attached to your reply.\n", target, humanSize(int64(len(data))))
	if len(frames) > 0 {
		fmt.Fprintf(&sb, "Sampled %d frames for visual analysis — they will be available to you on the next round so you can describe what you're sending.\n", len(frames))
	}
	if meta != "" {
		sb.WriteString("\n")
		sb.WriteString(meta)
		sb.WriteString("\n")
	}
	Debug("[videodl] %s → %d bytes, %d frames", target, len(data), len(frames))
	return sb.String(), nil
}

// humanSize formats a byte count compactly: "245 KB" / "12.4 MB".
func humanSize(n int64) string {
	const (
		_  = iota
		kb = 1 << (10 * iota)
		mb
		gb
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

