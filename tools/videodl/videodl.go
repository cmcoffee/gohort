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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	downloadTimeout   = 120 * time.Second
	downloadMaxBytes  = 200 * 1024 * 1024 // 200 MB hard cap

	// videodlCacheTable holds per-(scope, urlHash) records of
	// already-downloaded videos so the LLM can't make the user redownload
	// + re-receive the same clip on a later turn just because the URL
	// drifted back into context. Scope is the chat session id when set
	// (phantom + chat), the username otherwise; both empty disables the
	// cache (best-effort, no harm). Entry survives until the workspace
	// file is reaped — on lookup we re-verify the file exists so stale
	// entries self-heal into fresh downloads.
	videodlCacheTable = "videodl_url_cache"
)

// videodlCacheEntry is the persisted record. Filename is the basename
// inside the session workspace (we don't store absolute paths — the
// workspace root can change between deploys); Bytes + Mime + FetchedAt
// are repeated to the LLM on cache hit so it can still describe what
// it's pointing at.
type videodlCacheEntry struct {
	Filename    string    `json:"filename"`
	Bytes       int64     `json:"bytes"`
	FetchedAt   time.Time `json:"fetched_at"`
	OriginalURL string    `json:"original_url"`
}

// videodlCacheScope returns the scope prefix for cache keys. Prefers
// the chat session id (per-conversation isolation: same URL in two
// different chats caches independently), falls back to username, then
// to "" which disables the cache.
func videodlCacheScope(sess *ToolSession) string {
	if sess == nil {
		return ""
	}
	if sess.ChatSessionID != "" {
		return "chat:" + sess.ChatSessionID
	}
	if sess.Username != "" {
		return "user:" + sess.Username
	}
	return ""
}

// videodlCacheKey hashes the URL into a stable cache key prefixed by
// the scope. Same URL under different scopes → different keys.
func videodlCacheKey(scope, url string) string {
	sum := sha256.Sum256([]byte(url))
	return scope + ":" + hex.EncodeToString(sum[:])
}

// DownloadVideoTool's standalone registration is dropped — the
// LLM-facing surface is now the grouped `video` tool. The type +
// RunWithSession method stay public so the grouped dispatcher
// (tools/video/) imports + invokes them.

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

	// Per-(scope, URL) cache check — short-circuits a redownload when
	// the same URL has already been fetched + delivered earlier in
	// this conversation. The user already has the file in their
	// message history; calling yt-dlp again would re-spend bandwidth
	// and (if the LLM then attaches) duplicate-deliver the same clip.
	scope := videodlCacheScope(sess)
	cacheKey := ""
	if sess.DB != nil && scope != "" {
		cacheKey = videodlCacheKey(scope, target)
		var entry videodlCacheEntry
		if sess.DB.Get(videodlCacheTable, cacheKey, &entry) {
			// Stale-entry self-heal: if the workspace file has been
			// reaped (workspace lifecycle, manual cleanup), drop the
			// cache row and fall through to a fresh download.
			wsDir, wsErr := EnsureSessionWorkspace(sess)
			if wsErr == nil {
				abs := filepath.Join(wsDir, entry.Filename)
				if st, statErr := os.Stat(abs); statErr == nil && st.Size() > 0 {
					Debug("[videodl] cache hit for %s → %s (%d bytes)", target, entry.Filename, entry.Bytes)
					return fmt.Sprintf(
						"Already fetched %q (%s) earlier in this conversation — the user already has this clip in their message history. NO redownload performed. Do NOT call workspace(attach) for this; it would duplicate-deliver the same file. If the user is asking about the video they already received, answer from context.",
						entry.Filename, humanSize(entry.Bytes),
					), nil
				}
				sess.DB.Unset(videodlCacheTable, cacheKey)
				Debug("[videodl] cache entry stale (file missing): %s", entry.Filename)
			}
		}
	}

	data, err := downloadViaYtDlp(target)
	if err != nil {
		return "", err
	}

	// Save to the session workspace and return the path. NO auto-
	// attach; the LLM calls workspace(action="attach", path=...,
	// cleanup=true) when ready to deliver — same pattern as
	// find_image / fetch_image / generate_image. Surfaces that want
	// "always deliver on download" behavior (notably phantom, where
	// pasting a URL implies the user wants the file back) enforce
	// that at the system-prompt layer, not in the tool itself.
	wsDir, err := EnsureSessionWorkspace(sess)
	if err != nil {
		return "", fmt.Errorf("session workspace unavailable: %w", err)
	}
	name := "video-" + strconv.FormatInt(time.Now().UnixNano(), 36) + ".mp4"
	savePath := filepath.Join(wsDir, name)
	if err := os.MkdirAll(filepath.Dir(savePath), 0700); err != nil {
		return "", fmt.Errorf("create parent dir: %w", err)
	}
	if err := os.WriteFile(savePath, data, 0600); err != nil {
		return "", fmt.Errorf("save video: %w", err)
	}

	// Record the cache entry so a later turn that sees the same URL
	// short-circuits before yt-dlp runs.
	if sess.DB != nil && cacheKey != "" {
		sess.DB.Set(videodlCacheTable, cacheKey, videodlCacheEntry{
			Filename:    name,
			Bytes:       int64(len(data)),
			FetchedAt:   time.Now(),
			OriginalURL: target,
		})
	}

	// Sample frames into PendingViewImages so the LLM can describe
	// what it's about to send. These frames go to the LLM only —
	// they are NOT delivered to the user. Frame failures are non-fatal.
	frames, ferr := ExtractVideoFrames(data, viewFrameCount)
	if ferr != nil {
		Debug("[videodl] frame sampling failed for %s: %v", target, ferr)
	}
	for _, f := range frames {
		sess.AppendViewImage(f)
	}

	// Container metadata (mime, dimensions, duration, GPS-resolved
	// location, camera) gets rolled into the tool result alongside the
	// frames so the LLM has both visual and structured context.
	meta := ExtractVideoMetadata(data)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Stored at %q (%s). To deliver the file to the user, call workspace(action=\"attach\", path=%q, cleanup=true) — most URL pastes imply the user wants the file back. If you're only analyzing the video (user asked \"what's in this\", not \"send this\"), skip the attach.\n", name, humanSize(int64(len(data))), name)
	if len(frames) > 0 {
		fmt.Fprintf(&sb, "Sampled %d frames for visual analysis — they will be available to you on the next round so you can describe what you're sending.\n", len(frames))
	}
	if meta != "" {
		sb.WriteString("\n")
		sb.WriteString(meta)
		sb.WriteString("\n")
	}
	// Spoken audio: transcribe so the agent knows what was SAID before it posts,
	// not just what the frames show. Automatic now (was a separate opt-in tool).
	sb.WriteString(transcribeVideoAudio(data, name))
	Debug("[videodl] %s → %s (%d bytes, %d frames)", target, name, len(data), len(frames))
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

