// Package attach provides the `attach_file` chat tool, which lets the
// LLM deliver a file from its workspace to the user as an outbound
// attachment. Pairs with run_local / temp tools that produce files —
// the LLM creates the file in its workspace, then calls attach_file
// to deliver it.
//
// Currently delivered through chat (SSE `file` event → download link
// in the assistant bubble). Phantom doesn't yet have a delivery path
// for generic files; phantom-side calls to attach_file will land in
// sess.Files but not be sent. Bridge support is a future pass.
package attach

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// maxAttachmentBytes caps a single attachment to keep payloads sane.
// 20 MB is comfortable for chat SSE and well under iMessage limits
// when the bridge plumbs this through later.
const maxAttachmentBytes = 20 * 1024 * 1024

func init() { RegisterChatTool(&AttachFileTool{}) }

type AttachFileTool struct{}

func (t *AttachFileTool) Name() string         { return "attach_file" }
func (t *AttachFileTool) Caps() []Capability   { return []Capability{CapRead} }

func (t *AttachFileTool) Desc() string {
	return "Deliver a file from your workspace to the user as an attachment. Pair with run_local or temp tools that produce files: create the file in /workspace/, then call attach_file with its relative path. Works for any file type (PDF, CSV, ZIP, audio, generated image, etc.) — the user receives it as a download. Cap: 20MB per file. The path must be workspace-relative; absolute paths and `..` traversal are rejected."
}

func (t *AttachFileTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"path": {
			Type:        "string",
			Description: "Workspace-relative path to the file. Examples: \"report.pdf\", \"output/grid.png\", \"data.csv\".",
		},
		"name": {
			Type:        "string",
			Description: "Optional display name for the user (overrides the filename). Useful if the workspace path is cryptic. E.g. path=\"out_42.pdf\" name=\"Q3 Report.pdf\".",
		},
	}
}

func (t *AttachFileTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("attach_file requires a session with WorkspaceDir set")
}

func (t *AttachFileTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.WorkspaceDir == "" {
		return "", fmt.Errorf("attach_file requires a session with WorkspaceDir set")
	}
	rel := strings.TrimSpace(StringArg(args, "path"))
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := ResolveWorkspacePath(sess.WorkspaceDir, rel)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", rel, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory, not a file", rel)
	}
	if info.Size() > maxAttachmentBytes {
		return "", fmt.Errorf("file too large: %d bytes (cap %d)", info.Size(), maxAttachmentBytes)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", rel, err)
	}

	// Sniff MIME from content. Don't trust the file extension — the
	// LLM could have written anything in there. http.DetectContentType
	// reads the first 512 bytes and returns one of ~80 known types
	// or "application/octet-stream" for unknowns.
	mime := http.DetectContentType(data)

	// Display name: caller override > workspace basename. The
	// workspace path is preserved as Name in the attachment record
	// so the chat UI shows a meaningful filename.
	displayName := strings.TrimSpace(StringArg(args, "name"))
	if displayName == "" {
		displayName = filepath.Base(rel)
	}

	att := FileAttachment{
		Name:     displayName,
		MimeType: mime,
		Data:     base64.StdEncoding.EncodeToString(data),
		Size:     len(data),
	}
	sess.AppendFile(att)

	return fmt.Sprintf("Attached %q (%s, %s). The user will receive it as a download.", displayName, mime, humanSize(info.Size())), nil
}

// humanSize formats a byte count compactly for the LLM-visible result.
func humanSize(n int64) string {
	const (
		_  = iota
		kb = 1 << (10 * iota)
		mb
	)
	switch {
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
