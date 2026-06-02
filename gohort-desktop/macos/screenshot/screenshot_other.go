//go:build !darwin

// screenshot.capture is macOS-only (screencapture). On other platforms
// this package registers nothing, so the tool simply isn't offered.
package screenshot
