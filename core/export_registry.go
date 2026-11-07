// Export formats an agent defines at runtime, beyond the export jobs every
// deployment has (PDF, Word, OpenDocument, Excel, PowerPoint: core/flows,
// listed and adaptable under Admin > Services > System Hooks). A format is
// data + a sandboxed script generator that turns a JSON payload into file
// bytes: Interp + Script run under bwrap via RunSandboxedScript, with
// PyRequires provisioned host-side by EnsurePyDeps first. The formats
// themselves are stored per user by tools/export.
//
// The generator contract for script formats is deliberately tiny so it
// composes with anything: read the ExportInput JSON on stdin, write the
// file's bytes base64-encoded to stdout on a line carrying
// ExportB64Marker. stderr is free for diagnostics. RunExportFormat never
// needs to know what a .xlsx *is* — it pipes data in and reads bytes out.

package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// ExportB64Marker prefixes the stdout line carrying a script generator's
// base64 output. Kept distinctive so incidental library prints on stdout
// can't be mistaken for the payload.
const ExportB64Marker = "@@GOHORT_EXPORT_B64@@"

// exportRunTimeout caps a single generator run.
const exportRunTimeout = 90 * time.Second

// ExportInput is the JSON payload handed to a generator. Data is the
// format-specific content, opaque to the registry — each format
// documents its expected shape via ExportFormat.InputHint.
type ExportInput struct {
	Title string          `json:"title,omitempty"`
	Date  string          `json:"date,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// ExportFormat describes one output format and how to generate it.
type ExportFormat struct {
	Name        string   // registry key, e.g. "xlsx"
	Ext         string   // ".xlsx" (with leading dot)
	MIME        string   // advisory; delivery re-sniffs the bytes
	Desc        string   // one-line, shown to the LLM in the format list
	InputHint   string   // documents the expected `data` shape
	PyRequires  []string // pip specs provisioned before a script run
	Interp      string   // "python3" etc.
	Script      string   // generator body
	UserDefined bool     // true for formats an agent defined
}

// RunExportFormat executes a format against the given input and returns
// the generated file bytes: it provisions PyRequires host-side, runs the
// generator under the sandbox, and decodes the base64 marker line.
func RunExportFormat(ctx context.Context, f *ExportFormat, in ExportInput) ([]byte, error) {
	if f == nil {
		return nil, Error("export: nil format")
	}
	if strings.TrimSpace(f.Interp) == "" || strings.TrimSpace(f.Script) == "" {
		return nil, Error("export: format " + f.Name + " has no generator")
	}
	if len(f.PyRequires) > 0 {
		if err := EnsurePyDeps(ctx, f.PyRequires...); err != nil {
			return nil, err
		}
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, Error("export: marshal input: " + err.Error())
	}

	rctx, cancel := context.WithTimeout(ctx, exportRunTimeout)
	defer cancel()
	res := RunSandboxedScript(rctx, f.Interp, f.Script, string(stdin))
	if res.TimedOut {
		return nil, Error("export: " + f.Name + " generator timed out after " + exportRunTimeout.String())
	}
	// Scan stdout for the marker line even when Err is set — a generator
	// that produced output before a nonzero exit is rare, but the marker
	// is the source of truth. If no marker, surface stderr.
	for _, line := range strings.Split(res.Stdout, "\n") {
		if idx := strings.Index(line, ExportB64Marker); idx >= 0 {
			b64 := strings.TrimSpace(line[idx+len(ExportB64Marker):])
			data, derr := base64.StdEncoding.DecodeString(b64)
			if derr != nil {
				return nil, Error("export: " + f.Name + " produced an undecodable payload: " + derr.Error())
			}
			return data, nil
		}
	}
	stderr := strings.TrimSpace(res.Stderr)
	if res.Err != nil {
		return nil, Error("export: " + f.Name + " generator failed: " + tailLines(stderr, 12))
	}
	return nil, Error("export: " + f.Name + " generator produced no output marker. stderr: " + tailLines(stderr, 8))
}

// tailLines returns at most the last n lines of s, for compact error surfacing.
func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return "...\n" + strings.Join(lines[len(lines)-n:], "\n")
}
