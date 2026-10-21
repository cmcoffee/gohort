package videodl

import (
	"context"
	"fmt"
	"strings"

	"github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
)

// downloadViaYtDlp fetches the video at url: the video download job in
// System Hooks (core/flows), whose native flow is yt-dlp in the sandbox with
// the network, and which an admin can adapt to another downloader. Shared by
// download_video and view_video.
//
// Hard caps: two minutes, 200 MB (the job's).
func downloadViaYtDlp(url string) ([]byte, error) {
	// The URL comes from the model, which can be steered by any page it read.
	// It must be a public http(s) URL: yt-dlp would otherwise read a value
	// starting with "-" as one of its own options (some of which run
	// commands), and fetch file:, loopback or metadata addresses for anyone
	// who asked. The job checks it again for every flow, and its native flow
	// puts it after "--".
	url = strings.TrimSpace(url)
	if err := core.RefuseNonPublicHost(url); err != nil {
		return nil, fmt.Errorf("video url refused: %w", err)
	}
	res, err := flows.Do(context.Background(), "media.download", flows.Input{Params: map[string]string{"url": url}})
	if err != nil {
		return nil, err
	}
	return res.Out, nil
}
