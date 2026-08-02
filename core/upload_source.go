// Resolving what a caller named into a file the framework will upload.
//
// The image path has resolveInputImage, which verifies the bytes decode as a
// picture. An upload tool takes anything — a PDF, a CSV, an audio clip — so
// this is the general form: same reference discipline, no format opinion.
package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxUploadBytes caps a declared file upload. Generous for a document or a
// recording, small enough that a mistaken reference to something enormous
// fails immediately instead of streaming for minutes.
const maxUploadBytes = 64 << 20 // 64 MB

// UploadSource is a file resolved and ready to send.
type UploadSource struct {
	Name string
	Data []byte
}

// ResolveUploadSource turns a caller reference into file bytes.
//
// Workspace-relative only, deliberately. An absolute path would let a tool
// definition exfiltrate anything the process can read, and a URL would make
// the framework fetch on the caller's behalf — SSRF through a credential
// scoped to somewhere else entirely. ResolveWorkspacePath already refuses
// absolute paths and `..`; this adds the size and existence checks so the
// failure names the file rather than surfacing from inside the HTTP layer.
func ResolveUploadSource(sess *ToolSession, ref string) (UploadSource, error) {
	var out UploadSource
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return out, fmt.Errorf("no file was given to upload")
	}
	if strings.HasPrefix(strings.ToLower(ref), "http://") || strings.HasPrefix(strings.ToLower(ref), "https://") {
		return out, fmt.Errorf("a URL can't be uploaded directly — download it into your workspace first, then pass the saved filename")
	}
	if sess == nil || strings.TrimSpace(sess.WorkspaceDir) == "" {
		return out, fmt.Errorf("no workspace available to read %q from", ref)
	}
	abs, err := ResolveWorkspacePath(sess.WorkspaceDir, ref)
	if err != nil {
		return out, fmt.Errorf("%q is not a readable workspace path: %w", ref, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return out, fmt.Errorf("no file %q in your workspace", ref)
	}
	if info.IsDir() {
		return out, fmt.Errorf("%q is a directory, not a file", ref)
	}
	if info.Size() == 0 {
		return out, fmt.Errorf("%q is empty", ref)
	}
	if info.Size() > maxUploadBytes {
		return out, fmt.Errorf("%q is %s — the upload limit is %s", ref, HumanSize(info.Size()), HumanSize(maxUploadBytes))
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return out, fmt.Errorf("read %q: %w", ref, err)
	}
	return UploadSource{Name: filepath.Base(ref), Data: data}, nil
}
