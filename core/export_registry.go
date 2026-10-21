// Export format registry: the formats an app or an agent defines at
// runtime, beyond the export jobs every deployment has (PDF, Word,
// OpenDocument, Excel, PowerPoint: core/flows, listed and adaptable under
// Admin > Capabilities > System Hooks). A format here is data + a
// generator; the registry maps its name to the generator that turns a JSON
// payload into file bytes.
//
// Two generator kinds share one execution path (RunExportFormat):
//
//   - Go (GoGen != nil): in-process.
//   - Sandboxed script (Interp + Script): runs under bwrap via
//     RunSandboxedScript, with PyRequires provisioned host-side by
//     EnsurePyDeps first. Every format an agent defines is one.
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
	"sync"
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
	Interp      string   // "python3" etc.; "" => Go built-in (GoGen)
	Script      string   // generator body for script formats
	UserDefined bool     // true for app/LLM-registered formats

	// GoGen is the in-process generator for built-in Go formats. When
	// set, Interp/Script/PyRequires are ignored.
	GoGen func(in ExportInput) ([]byte, error) `json:"-"`
}

var (
	exportRegistryMu sync.RWMutex
	exportRegistry   = map[string]*ExportFormat{}
)

// RegisterExportFormat adds (or replaces) a format in the process-global
// registry. Apps call this from init() to grow the catalog; built-ins
// register the same way below. Runtime per-user formats are NOT stored
// here — those live in a per-user DB table and are passed to
// RunExportFormat directly (see the export tool's define/create).
func RegisterExportFormat(f *ExportFormat) {
	if f == nil || f.Name == "" {
		return
	}
	exportRegistryMu.Lock()
	defer exportRegistryMu.Unlock()
	exportRegistry[f.Name] = f
}

// LookupExportFormat returns a registered built-in/app format by name.
func LookupExportFormat(name string) (*ExportFormat, bool) {
	exportRegistryMu.RLock()
	defer exportRegistryMu.RUnlock()
	f, ok := exportRegistry[strings.TrimSpace(name)]
	return f, ok
}

// ListExportFormats returns all registered built-in/app formats.
func ListExportFormats() []*ExportFormat {
	exportRegistryMu.RLock()
	defer exportRegistryMu.RUnlock()
	out := make([]*ExportFormat, 0, len(exportRegistry))
	for _, f := range exportRegistry {
		out = append(out, f)
	}
	return out
}

// RunExportFormat executes a format (built-in OR user-defined — the
// caller supplies the resolved *ExportFormat) against the given input
// and returns the generated file bytes. For script formats it
// provisions PyRequires host-side, runs the generator under the sandbox,
// and decodes the base64 marker line.
func RunExportFormat(ctx context.Context, f *ExportFormat, in ExportInput) ([]byte, error) {
	if f == nil {
		return nil, Error("export: nil format")
	}
	if f.GoGen != nil {
		return f.GoGen(in)
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
