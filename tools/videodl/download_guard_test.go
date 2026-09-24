package videodl

// A video URL is refused before yt-dlp ever runs unless it is a public
// http(s) URL: a value starting with "-" would be read as yt-dlp's own option.

import (
	"strings"
	"testing"
)

func TestAVideoURLMustBeAPublicWebAddress(t *testing.T) {
	for _, bad := range []string{
		"--exec=touch /tmp/pwned",
		"-o/etc/cron.d/x",
		"file:///etc/passwd",
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:8080/",
	} {
		_, err := downloadViaYtDlp(bad)
		if err == nil || !strings.Contains(err.Error(), "refused") {
			t.Errorf("%q should be refused before yt-dlp runs, got %v", bad, err)
		}
	}
}
