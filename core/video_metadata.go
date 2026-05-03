package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Video metadata + frame extraction via ffmpeg/ffprobe. ffmpeg is required
// at runtime — if not on PATH, video handling silently no-ops and the
// caller falls back to "send raw bytes" (which the LLM stack rejects). We
// log a startup warning so operators know.
//
// Supported tags come primarily from Apple QuickTime atoms (`com.apple.*`)
// and standard ISO MP4 metadata, which together cover iOS recordings,
// most Android phones, and the major editors. Less-common containers
// degrade gracefully: missing fields are omitted.

const videoFrameSampleCount = 8 // frames per video, evenly distributed

// VideoFrameSampleCount is exposed for callers that want to size buffers
// or set expectations; not configurable per-call yet.
const VideoFrameSampleCount = videoFrameSampleCount

// ffprobeOutput is the subset of ffprobe -show_format -show_streams that we use.
type ffprobeOutput struct {
	Format struct {
		FormatName string            `json:"format_name"`
		Duration   string            `json:"duration"`
		Size       string            `json:"size"`
		BitRate    string            `json:"bit_rate"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
	Streams []struct {
		CodecType  string            `json:"codec_type"`
		CodecName  string            `json:"codec_name"`
		Width      int               `json:"width"`
		Height     int               `json:"height"`
		RFrameRate string            `json:"r_frame_rate"`
		Tags       map[string]string `json:"tags"`
	} `json:"streams"`
}

// extractVideoMetadata runs ffprobe on the given bytes and returns a
// formatted `[video_context]` block ready for prepending to a message. If
// ffprobe is unavailable or the bytes don't parse, returns "".
func extractVideoMetadata(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	tmp, err := writeTempFile(data, "*.mp4")
	if err != nil {
		Debug("[video] tempfile failed: %v", err)
		return ""
	}
	defer os.Remove(tmp)

	probe, err := runFfprobe(tmp)
	if err != nil {
		Debug("[video] ffprobe failed: %v", err)
		return ""
	}

	var lines []string
	if mime := http_DetectVideoMime(probe.Format.FormatName); mime != "" {
		lines = append(lines, "mime: "+mime)
	}
	lines = append(lines, "size: "+humanBytes(int64(len(data))))

	// Pick the first video stream for dimensions / codec / fps.
	var videoStream *struct {
		CodecType  string            `json:"codec_type"`
		CodecName  string            `json:"codec_name"`
		Width      int               `json:"width"`
		Height     int               `json:"height"`
		RFrameRate string            `json:"r_frame_rate"`
		Tags       map[string]string `json:"tags"`
	}
	var codecs []string
	for i := range probe.Streams {
		s := probe.Streams[i]
		if s.CodecType == "video" && videoStream == nil {
			videoStream = &probe.Streams[i]
		}
		if s.CodecName != "" {
			codecs = append(codecs, s.CodecName)
		}
	}
	if videoStream != nil && videoStream.Width > 0 && videoStream.Height > 0 {
		lines = append(lines, fmt.Sprintf("dimensions: %dx%d", videoStream.Width, videoStream.Height))
	}

	if probe.Format.Duration != "" {
		if d, err := strconv.ParseFloat(probe.Format.Duration, 64); err == nil && d > 0 {
			fps := ""
			if videoStream != nil {
				fps = parseFPS(videoStream.RFrameRate)
			}
			if fps != "" {
				lines = append(lines, fmt.Sprintf("duration: %s, %s fps", humanDuration(d), fps))
			} else {
				lines = append(lines, "duration: "+humanDuration(d))
			}
		}
	}
	if len(codecs) > 0 {
		lines = append(lines, "codec: "+strings.Join(uniqueStrings(codecs), ", "))
	}

	// Apple QuickTime + standard tags. Apple uses creation_time and a
	// custom location atom; non-Apple cameras typically only set creation_time.
	tags := mergeTags(probe.Format.Tags, videoStreamTags(videoStream))
	if t := parseVideoTime(tags["creation_time"]); t != "" {
		lines = append(lines, "recorded: "+t)
	}
	if lat, lon, ok := parseISO6709(tags["com.apple.quicktime.location.ISO6709"]); ok {
		place := reverseGeocode(lat, lon)
		latDir := "N"
		if lat < 0 {
			latDir = "S"
			lat = -lat
		}
		lonDir := "E"
		if lon < 0 {
			lonDir = "W"
			lon = -lon
		}
		lines = append(lines, fmt.Sprintf("gps: %.4f° %s, %.4f° %s", lat, latDir, lon, lonDir))
		if place != "" {
			lines = append(lines, "location: "+place)
		}
	}
	make := firstNonEmpty(tags["com.apple.quicktime.make"], tags["make"])
	model := firstNonEmpty(tags["com.apple.quicktime.model"], tags["model"])
	switch {
	case make != "" && model != "":
		lines = append(lines, "camera: "+make+" "+model)
	case model != "":
		lines = append(lines, "camera: "+model)
	}
	if sw := firstNonEmpty(tags["com.apple.quicktime.software"], tags["software"]); sw != "" {
		lines = append(lines, "software: "+sw)
	}

	if len(lines) == 0 {
		return ""
	}
	return "[video_context]\n" + strings.Join(lines, "\n")
}

// extractVideosMetadata builds the metadata block for one or more videos.
// Multi-video messages get numbered headers like image multi-extraction.
func extractVideosMetadata(videos [][]byte) string {
	if len(videos) == 0 {
		return ""
	}
	if len(videos) == 1 {
		return extractVideoMetadata(videos[0])
	}
	var blocks []string
	for i, data := range videos {
		body := extractVideoMetadata(data)
		if body == "" {
			continue
		}
		body = strings.Replace(body, "[video_context]", fmt.Sprintf("[video %d]", i+1), 1)
		blocks = append(blocks, body)
	}
	return strings.Join(blocks, "\n\n")
}

// runFfprobe invokes `ffprobe -show_format -show_streams` on path and parses JSON.
func runFfprobe(path string) (*ffprobeOutput, error) {
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var probe ffprobeOutput
	if err := json.Unmarshal(out.Bytes(), &probe); err != nil {
		return nil, err
	}
	return &probe, nil
}

// writeTempFile writes data to a new temp file with the given pattern and
// returns the absolute path. Caller is responsible for os.Remove.
func writeTempFile(data []byte, pattern string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// videoStreamTags returns the tags map of a video stream pointer or nil-safe empty.
func videoStreamTags(s *struct {
	CodecType  string            `json:"codec_type"`
	CodecName  string            `json:"codec_name"`
	Width      int               `json:"width"`
	Height     int               `json:"height"`
	RFrameRate string            `json:"r_frame_rate"`
	Tags       map[string]string `json:"tags"`
}) map[string]string {
	if s == nil {
		return nil
	}
	return s.Tags
}

// mergeTags layers two tag maps; right-hand wins on conflict.
func mergeTags(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// parseFPS converts ffprobe's r_frame_rate (e.g. "30000/1001") into a
// printable string ("29.97"). Returns "" on malformed input.
func parseFPS(s string) string {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return ""
	}
	num, err1 := strconv.ParseFloat(parts[0], 64)
	den, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || den == 0 {
		return ""
	}
	v := num / den
	if math.Abs(v-math.Round(v)) < 0.05 {
		return strconv.Itoa(int(math.Round(v)))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}

// parseVideoTime accepts the ISO-8601 timestamps ffprobe surfaces for
// `creation_time` and reformats to "2026-03-18 19:06:46 PDT" using local
// time. Returns "" on parse failure.
func parseVideoTime(s string) string {
	if s == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Local().Format("2006-01-02 15:04:05 MST")
		}
	}
	return ""
}

// parseISO6709 parses Apple's location atom format like
// "+45.7576+021.2290+167.000/" or shorter "+45.7576+021.2290/". Returns
// (lat, lon, true) on success. Only the decimal-degree variant is
// supported; older sexagesimal forms (DDMM.MM, DDMMSS.S) fall through
// as unparseable, which is fine for iPhone footage post-iOS 6.
func parseISO6709(s string) (float64, float64, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	if s == "" {
		return 0, 0, false
	}
	// Find the second sign (start of longitude). The first character is
	// always the latitude's sign; longitude starts at the next +/-.
	if len(s) < 4 {
		return 0, 0, false
	}
	lonStart := -1
	for i := 1; i < len(s); i++ {
		if s[i] == '+' || s[i] == '-' {
			lonStart = i
			break
		}
	}
	if lonStart < 0 {
		return 0, 0, false
	}
	latStr := s[:lonStart]
	rest := s[lonStart:]
	// Find optional altitude sign after longitude.
	altStart := -1
	for i := 1; i < len(rest); i++ {
		if rest[i] == '+' || rest[i] == '-' {
			altStart = i
			break
		}
	}
	lonStr := rest
	if altStart >= 0 {
		lonStr = rest[:altStart]
	}
	lat, err1 := strconv.ParseFloat(latStr, 64)
	lon, err2 := strconv.ParseFloat(lonStr, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	// Sanity check: decimal degrees fit in [-90,90] x [-180,180]. If the
	// values look like packed sexagesimal (e.g. 4545.456 for 45°45.456'),
	// reject; we don't decode that variant.
	if math.Abs(lat) > 90 || math.Abs(lon) > 180 {
		return 0, 0, false
	}
	return lat, lon, true
}

// http_DetectVideoMime maps ffprobe's format_name (often a comma-joined
// list like "mov,mp4,m4a,3gp,3g2,mj2") to a single representative MIME.
func http_DetectVideoMime(formatName string) string {
	if formatName == "" {
		return "video/octet-stream"
	}
	first := strings.SplitN(formatName, ",", 2)[0]
	switch first {
	case "mov", "mp4", "m4a", "3gp", "3g2":
		return "video/quicktime"
	case "matroska", "webm":
		return "video/webm"
	case "avi":
		return "video/x-msvideo"
	case "flv":
		return "video/x-flv"
	case "mpegts":
		return "video/mp2t"
	case "mpeg":
		return "video/mpeg"
	}
	return "video/" + first
}

// humanDuration formats seconds as "28.4s" / "1m 23s" / "1h 5m 23s".
func humanDuration(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%.1fs", seconds)
	}
	total := int(math.Round(seconds))
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	return fmt.Sprintf("%dm %ds", m, s)
}

// uniqueStrings preserves order, dropping duplicates.
func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
