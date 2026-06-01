// Video transcode action — shrinks a workspace video file to fit
// under a size cap. Built for phantom's iMessage delivery: the
// transport caps attachments at ~20MB, and any clip longer than ~30s
// at default bitrates blows past that. When an [ATTACH:] fails for
// size, the LLM's recovery path is video(action="transcode",
// path=..., max_size_mb=18) → [ATTACH: <output>].
//
// Two-pass strategy: ffprobe duration, compute target bitrate from
// (max_size_mb / duration), ffmpeg encode at that bitrate. If the
// output is still over cap (high-motion content tends to exceed its
// own bitrate budget), retry with progressively smaller resolutions
// (720p → 540 → 480). Returns an error if all passes fail — caller
// can drop max_size_mb further or give up.
//
// Audio is re-encoded to AAC; bitrate scales down to 64k when the
// total budget is tight (under ~250kbps). Container is MP4 with
// faststart for messaging-app compatibility.
//
// Uses the standard sandbox shell path so it inherits the bwrap
// isolation + network gating + tool-call timeout. ffmpeg + ffprobe
// must be on PATH (already required by the existing download/view
// flows in tools/videodl).

package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// transcodeTimeout caps the total ffmpeg work. A 3-minute clip at
// 720p single-pass encode runs in ~30s on modest hardware; the cap
// is set well above so the worst case (long clip + multiple scale
// retries) finishes or fails cleanly rather than hanging the turn.
const transcodeTimeout = 5 * time.Minute

// defaultMaxSizeMB targets ~18MB so the resulting file slips under
// iMessage's ~20MB attachment cap with container overhead headroom.
// Callers can override via the max_size_mb arg.
const defaultMaxSizeMB = 18.0

// scaleSteps is the resolution-shrink ladder. First entry is "no
// downscale" (keep input resolution but at the target bitrate). Each
// subsequent step further constrains video height when an earlier
// pass still overshot — useful for high-motion content where the
// bitrate alone can't hit the size cap without artifacts.
var scaleSteps = []string{"", "scale=-2:720", "scale=-2:540", "scale=-2:480"}

// transcodeVideoAction implements video(action="transcode"). Returns
// a human-readable summary on success, or an error explaining what
// blocked the shrink so the LLM can adapt.
func transcodeVideoAction(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.WorkspaceDir == "" {
		return "", errors.New("video(action=transcode) requires a session with a workspace")
	}
	path := strings.TrimSpace(StringArg(args, "path"))
	if path == "" {
		return "", errors.New("path is required (workspace-relative)")
	}
	maxSizeMB := defaultMaxSizeMB
	if v, ok := args["max_size_mb"].(float64); ok && v > 0 {
		maxSizeMB = v
	}
	// Optional explicit output path. Default: <basename>-small.mp4 in
	// the same directory as the input.
	outRel := strings.TrimSpace(StringArg(args, "output_path"))
	if outRel == "" {
		ext := filepath.Ext(path)
		base := strings.TrimSuffix(path, ext)
		outRel = base + "-small.mp4"
	}
	inputAbs := filepath.Join(sess.WorkspaceDir, path)
	outputAbs := filepath.Join(sess.WorkspaceDir, outRel)
	// Refuse if input doesn't exist — give the LLM a clear error to
	// retry against (typo'd path is the most common failure mode).
	if st, err := os.Stat(inputAbs); err != nil {
		return "", fmt.Errorf("input file %q not found in workspace: %v", path, err)
	} else if st.IsDir() {
		return "", fmt.Errorf("input %q is a directory, not a file", path)
	}
	inputBytes := fileSize(inputAbs)
	maxBytes := int64(maxSizeMB * 1024 * 1024)

	ctx, cancel := context.WithTimeout(context.Background(), transcodeTimeout)
	defer cancel()

	durSec, err := probeDuration(ctx, sess.WorkspaceDir, inputAbs)
	if err != nil {
		return "", fmt.Errorf("ffprobe failed on %q: %v", path, err)
	}
	if durSec <= 0 {
		return "", fmt.Errorf("ffprobe returned non-positive duration for %q; not a valid video?", path)
	}

	// Total bitrate target. Multiply by 0.95 to leave 5% headroom for
	// container overhead — actual files routinely run ~3-5% over the
	// nominal bitrate * duration calculation.
	totalKbpsBudget := (maxSizeMB * 8 * 1024) / durSec * 0.95
	// Audio carve-out. 96kbps AAC is clean for speech + music in
	// messaging contexts; drop to 64 when the overall budget is tight.
	audioKbps := 96.0
	if totalKbpsBudget < 250 {
		audioKbps = 64
	}
	videoKbps := totalKbpsBudget - audioKbps
	if videoKbps < 80 {
		return "", fmt.Errorf("file too long (%.1fs) for %.1fMB cap — minimum encoder would need ~80kbps video and you've got %.0fkbps to work with. Either raise max_size_mb or trim the clip first", durSec, maxSizeMB, videoKbps)
	}

	// Encode + verify loop. Each pass tries one scale step; if the
	// output is still over cap, retry the next step. The first step
	// is no-scale (keep input dimensions); subsequent steps drop
	// resolution. Newest output always overwrites the previous.
	for _, scale := range scaleSteps {
		if err := runFfmpegEncode(ctx, sess.WorkspaceDir, inputAbs, outputAbs, scale, videoKbps, audioKbps); err != nil {
			return "", fmt.Errorf("ffmpeg encode failed (%s): %v", scaleLabel(scale), err)
		}
		size := fileSize(outputAbs)
		if size <= maxBytes {
			return formatTranscodeSummary(path, outRel, inputBytes, size, durSec, scale), nil
		}
	}
	finalSize := fileSize(outputAbs)
	return "", fmt.Errorf("could not shrink %q under %.1fMB cap even at 480p — final pass was %.1fMB. The clip is too high-motion for this bitrate target; trim its length or accept a higher cap",
		path, maxSizeMB, float64(finalSize)/1024/1024)
}

// probeDuration runs `ffprobe -show_entries format=duration -of
// default=nw=1:nk=1 <input>` and parses the float result. Returns
// (duration_in_seconds, err).
func probeDuration(ctx context.Context, wsDir, inputAbs string) (float64, error) {
	cmd := fmt.Sprintf(
		"ffprobe -v error -show_entries format=duration -of default=nw=1:nk=1 %s",
		shellQuote(inputAbs),
	)
	res := RunSandboxedShell(ctx, cmd, wsDir)
	if res.Err != nil {
		return 0, fmt.Errorf("%v: %s", res.Err, strings.TrimSpace(res.Output))
	}
	durStr := strings.TrimSpace(res.Output)
	dur, err := strconv.ParseFloat(durStr, 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe output %q wasn't a float: %v", durStr, err)
	}
	return dur, nil
}

// runFfmpegEncode runs one re-encode pass at the specified bitrates,
// optionally constraining video filter (scale). -movflags faststart
// puts the moov atom at the front so messaging clients can begin
// playback before the whole file is fetched.
func runFfmpegEncode(ctx context.Context, wsDir, inputAbs, outputAbs, vf string, videoKbps, audioKbps float64) error {
	parts := []string{
		"ffmpeg", "-y", "-i", shellQuote(inputAbs),
		"-c:v", "libx264",
		"-preset", "fast",
		"-b:v", fmt.Sprintf("%.0fk", videoKbps),
		"-maxrate", fmt.Sprintf("%.0fk", videoKbps*1.2),
		"-bufsize", fmt.Sprintf("%.0fk", videoKbps*2),
	}
	if vf != "" {
		parts = append(parts, "-vf", shellQuote(vf))
	}
	parts = append(parts,
		"-c:a", "aac",
		"-b:a", fmt.Sprintf("%.0fk", audioKbps),
		"-movflags", "+faststart",
		shellQuote(outputAbs),
	)
	cmd := strings.Join(parts, " ")
	res := RunSandboxedShell(ctx, cmd, wsDir)
	if res.Err != nil {
		// Tail the combined output so the LLM sees what ffmpeg said —
		// "Unknown encoder" / "moov atom not found" etc. are
		// actionable hints.
		tail := strings.TrimSpace(res.Output)
		if len(tail) > 600 {
			tail = "…" + tail[len(tail)-600:]
		}
		return fmt.Errorf("%v: %s", res.Err, tail)
	}
	return nil
}

// formatTranscodeSummary turns a successful transcode into a brief
// human-readable summary the LLM can integrate into its reply.
func formatTranscodeSummary(inPath, outPath string, inBytes, outBytes int64, durSec float64, scale string) string {
	inMB := float64(inBytes) / 1024 / 1024
	outMB := float64(outBytes) / 1024 / 1024
	ratio := 0.0
	if inMB > 0 {
		ratio = (1 - outMB/inMB) * 100
	}
	res := scaleLabel(scale)
	return fmt.Sprintf(
		"Transcoded %s → %s. %.1fMB → %.1fMB (%.0f%% smaller) at %s, %.1fs runtime. Ready to attach: [ATTACH: %s, cleanup=true]",
		inPath, outPath, inMB, outMB, ratio, res, durSec, outPath,
	)
}

func scaleLabel(vf string) string {
	if vf == "" {
		return "input resolution"
	}
	// vf is like "scale=-2:720" — surface the target height.
	parts := strings.Split(vf, ":")
	if len(parts) == 2 {
		return parts[1] + "p"
	}
	return vf
}

// fileSize returns the byte size of a file, or 0 if it can't be
// stat'd. Used to verify the encode landed under the cap.
func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// shellQuote single-quotes a path/argument for safe shell interpolation.
// ffmpeg arg list goes through `sh -c "ffmpeg ..."` via RunSandboxedShell,
// so the standard POSIX single-quote escape applies.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
