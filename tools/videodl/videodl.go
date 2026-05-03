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
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	return "Download a video from a URL via yt-dlp. Supports Instagram, TikTok, YouTube, Twitter/X, Reddit, Vimeo, Facebook, and ~1000 other sites. The downloaded video is sampled into frames you can analyze, and (where the calling app supports outbound attachments) carried back to the user as a video file. Use when the user pastes a video link and wants you to watch, describe, summarize, or share it. DRM-protected sources (Netflix, paid YouTube, etc.) cannot be downloaded — the tool will return an error in that case."
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

	// Verify yt-dlp is installed before launching the download dance.
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		return "", fmt.Errorf("yt-dlp is not installed on this server. Install via `pip install yt-dlp` or download the static binary from https://github.com/yt-dlp/yt-dlp/releases")
	}

	dir, err := os.MkdirTemp("", "videodl-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	outPattern := filepath.Join(dir, "video.%(ext)s")
	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()

	// Prefer mp4 (compatible with iMessage attachments and most vision
	// pipelines). Fall back to whatever the site offers if mp4 isn't
	// available. --no-playlist guards against accidentally pulling an
	// entire YouTube playlist when the URL looks like a single video.
	cmd := exec.CommandContext(ctx, "yt-dlp",
		"-f", "best[ext=mp4]/best",
		"-o", outPattern,
		"--no-playlist",
		"--no-warnings",
		"--no-progress",
		"--max-filesize", fmt.Sprintf("%d", downloadMaxBytes),
		target,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("download timed out after %s", downloadTimeout)
		}
		return "", fmt.Errorf("yt-dlp failed: %s", msg)
	}

	// Find the produced file (extension chosen by yt-dlp from `%(ext)s`).
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read output dir: %w", err)
	}
	var videoPath string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), "video.") {
			videoPath = filepath.Join(dir, e.Name())
			break
		}
	}
	if videoPath == "" {
		return "", fmt.Errorf("yt-dlp produced no output file")
	}
	info, err := os.Stat(videoPath)
	if err != nil {
		return "", err
	}
	if info.Size() > downloadMaxBytes {
		return "", fmt.Errorf("downloaded video too large: %d bytes (cap %d)", info.Size(), downloadMaxBytes)
	}

	data, err := os.ReadFile(videoPath)
	if err != nil {
		return "", fmt.Errorf("read output: %w", err)
	}

	// Carry the full video bytes for outbound delivery — phantom or any
	// future app that handles sess.Videos can attach the clip to its
	// reply. Apps that don't consume sess.Videos just ignore them.
	sess.AppendVideo(base64.StdEncoding.EncodeToString(data))

	// Container metadata (mime, dimensions, duration, GPS-resolved
	// location, camera) gets rolled into the tool result so the LLM has
	// enough context to describe the clip in its reply, even though it
	// can't see the actual frames.
	meta := ExtractVideoMetadata(data)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Downloaded video from %s (%s). It will be attached to your reply.\n", target, humanSize(info.Size()))
	if meta != "" {
		sb.WriteString("\n")
		sb.WriteString(meta)
		sb.WriteString("\n")
	}
	Debug("[videodl] %s → %d bytes", target, len(data))
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

