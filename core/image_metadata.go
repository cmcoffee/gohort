package core

import (
	"bytes"
	"fmt"
	"image"
	"net/http"
	"strings"

	"github.com/rwcarlsen/goexif/exif"
)

// extractImageMetadata returns a compact `[image_context] ...` text block
// describing what the image is (mime, size, dimensions) and any EXIF tags
// the file carries (date taken, GPS, camera, lens, exposure). Returns the
// empty string if nothing useful can be extracted. The text is meant to be
// prepended to the user's message content so the vision model has ambient
// grounding alongside the pixels.
//
// The EXIF block is best-effort: photos from phones and DSLRs typically
// carry rich metadata; screenshots and rendered images carry none. Missing
// fields are silently omitted rather than reported.
func extractImageMetadata(data []byte) string {
	if len(data) == 0 {
		return ""
	}

	var lines []string

	// File-level properties (cheap, work on every image).
	if mime := http.DetectContentType(data); mime != "" {
		lines = append(lines, "mime: "+mime)
	}
	lines = append(lines, "size: "+humanBytes(int64(len(data))))
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		lines = append(lines, fmt.Sprintf("dimensions: %dx%d", cfg.Width, cfg.Height))
	}

	// EXIF (silent no-op for images without it — screenshots, rendered art).
	if x, err := exif.Decode(bytes.NewReader(data)); err == nil {
		if t, err := x.DateTime(); err == nil {
			lines = append(lines, "taken: "+t.Format("2006-01-02 15:04:05 MST"))
		}
		if lat, lon, err := x.LatLong(); err == nil {
			// Resolve to a place name BEFORE flipping signs — reverseGeocode
			// expects standard signed decimal degrees.
			place := reverseGeocode(lat, lon)

			// Cardinal-direction form ("37.7749° N, 122.4194° W") is unambiguous
			// for LLMs — bare "lat, lon" with negative signs is easy to confuse
			// with GeoJSON's "lon, lat" convention or to misread the hemisphere.
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
				// Prepend "location" right after gps so the LLM never has to
				// translate coordinates itself — it confabulates landmarks
				// and country names from raw lat/lon when the location isn't
				// world-famous.
				lines = append(lines, "location: "+place)
			}
		}
		make := exifString(x, exif.Make)
		model := exifString(x, exif.Model)
		switch {
		case make != "" && model != "":
			lines = append(lines, "camera: "+make+" "+model)
		case model != "":
			lines = append(lines, "camera: "+model)
		}
		if lens := exifString(x, exif.LensModel); lens != "" {
			lines = append(lines, "lens: "+lens)
		}
		var exposureBits []string
		if shutter := exifString(x, exif.ExposureTime); shutter != "" {
			exposureBits = append(exposureBits, shutter+"s")
		}
		if aperture := exifString(x, exif.FNumber); aperture != "" {
			if v := parseRational(aperture); v != "" {
				exposureBits = append(exposureBits, "f/"+v)
			}
		}
		if iso := exifString(x, exif.ISOSpeedRatings); iso != "" {
			exposureBits = append(exposureBits, "ISO "+iso)
		}
		if len(exposureBits) > 0 {
			lines = append(lines, "exposure: "+strings.Join(exposureBits, ", "))
		}
	}

	if len(lines) == 0 {
		return ""
	}
	return "[image_context]\n" + strings.Join(lines, "\n")
}

// extractImagesMetadata builds the metadata block for one or more images.
// When there's exactly one image, the [image_context] header is used; with
// multiple images, each block is labeled [image N] so the model can refer
// to them unambiguously.
func extractImagesMetadata(images [][]byte) string {
	if len(images) == 0 {
		return ""
	}
	if len(images) == 1 {
		return extractImageMetadata(images[0])
	}
	var blocks []string
	for i, data := range images {
		body := extractImageMetadata(data)
		if body == "" {
			continue
		}
		// Replace the generic header with a numbered one.
		body = strings.Replace(body, "[image_context]", fmt.Sprintf("[image %d]", i+1), 1)
		blocks = append(blocks, body)
	}
	return strings.Join(blocks, "\n\n")
}

// exifString fetches an EXIF tag as a trimmed display string, or "" if
// the tag is missing or non-stringable.
func exifString(x *exif.Exif, name exif.FieldName) string {
	tag, err := x.Get(name)
	if err != nil || tag == nil {
		return ""
	}
	s := tag.String()
	// goexif returns strings already quoted ("foo"); strip surrounding quotes
	// so the output stays clean for prompt injection.
	s = strings.Trim(s, "\"")
	return strings.TrimSpace(s)
}

// parseRational converts a goexif rational string like "178/100" into a
// trimmed decimal "1.78". Returns the original input if it isn't a
// rational fraction.
func parseRational(s string) string {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return s
	}
	var num, den int
	if _, err := fmt.Sscanf(parts[0], "%d", &num); err != nil {
		return s
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &den); err != nil || den == 0 {
		return s
	}
	v := float64(num) / float64(den)
	// Two-decimal precision is plenty for f-stops, exposure times, etc.
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}

// humanBytes formats a byte count as "1.2 KB" / "3.4 MB" etc.
func humanBytes(n int64) string {
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
