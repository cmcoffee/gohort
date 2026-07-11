package core

import (
	"time"

	"github.com/cmcoffee/gohort/core/deps"
	"github.com/cmcoffee/gohort/core/factcheck"
	"github.com/cmcoffee/gohort/core/geo"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/textutil"
)

// core.go — the seam that assembles the core namespace from gohort's extracted
// leaf packages, re-exporting their surface as core symbols. Each leaf (media,
// textutil, factcheck, …) holds a pure,
// independently-testable slice of functionality and must NOT import core; this
// file re-exports their surface so every `import . "core"` file keeps calling
// MarkdownToPDF / VerifyFacts / StripMetaTags / Transcribe / ExtractDocument
// unqualified — no sub-package import at the call site.
//
// When you extract a new leaf or add a symbol to one, add ONE line here
// (a type alias / const / func-value) and it becomes a core symbol. The init()
// at the bottom does the reverse edge: it injects core-side implementations back
// into the leaves that need a hub service they can't import (see each leaf's
// hooks.go).
//
// Caveat: a mutable var (e.g. media.PDFBranding) can't be re-exported — a var
// alias copies the value rather than sharing it — so those are set on the leaf
// directly at their few write-sites.

// --- media -------------------------------------------------------------------

type (
	TranscribeConfig   = media.TranscribeConfig
	DocumentAttachment = media.DocumentAttachment
)

const VideoFrameSampleCount = media.VideoFrameSampleCount

var (
	// PDF / HTML→PDF.
	MarkdownToPDF       = media.MarkdownToPDF
	MarkdownToPDFScaled = media.MarkdownToPDFScaled
	MarkdownToPDFBytes  = media.MarkdownToPDFBytes
	HTMLToPDF           = media.HTMLToPDF
	RegisterHTMLToPDF   = media.RegisterHTMLToPDF
	HTMLToPDFAvailable  = media.HTMLToPDFAvailable

	// Image / video metadata + frames.
	ExtractVideoFrames    = media.ExtractVideoFrames
	ExtractVideoMetadata  = media.ExtractVideoMetadata
	ExtractVideoAudio     = media.ExtractVideoAudio
	TranscodeAudioToWAV   = media.TranscodeAudioToWAV
	ExtractVideosFrames   = media.ExtractVideosFrames
	ExtractImagesMetadata = media.ExtractImagesMetadata
	ExtractVideosMetadata = media.ExtractVideosMetadata

	// Transcription (STT).
	Transcribe                  = media.Transcribe
	SetTranscribeConfig         = media.SetTranscribeConfig
	GetTranscribeConfig         = media.GetTranscribeConfig
	LoadTranscribeConfigFromDB  = media.LoadTranscribeConfigFromDB
	SaveTranscribeConfigToDB    = media.SaveTranscribeConfigToDB
	TranscribeRuntimeFlagScript = media.TranscribeRuntimeFlagScript

	// Document extraction.
	ExtractDocument          = media.ExtractDocument
	ExtractHTMLByKind        = media.ExtractHTMLByKind
	FormatDocumentPreamble   = media.FormatDocumentPreamble
	FormatAttachmentPreamble = media.FormatAttachmentPreamble
	DocumentExtractTimeout   = media.DocumentExtractTimeout
)

// --- textutil ----------------------------------------------------------------

const (
	BannedWordsRule   = textutil.BannedWordsRule
	TimeAwarenessRule = textutil.TimeAwarenessRule
)

var (
	MarkdownToPlain             = textutil.MarkdownToPlain
	StripMetaTags               = textutil.StripMetaTags
	StripToolCallTags           = textutil.StripToolCallTags
	StripEmDashes               = textutil.StripEmDashes
	StripPromptSectionsForTools = textutil.StripPromptSectionsForTools
	SnakeFromDisplay            = textutil.SnakeFromDisplay
)

// --- factcheck ---------------------------------------------------------------

type (
	Fact          = factcheck.Fact
	RejectedFact  = factcheck.RejectedFact
	FactExtractor = factcheck.FactExtractor
	Claim         = factcheck.Claim
)

var VerifyFacts = factcheck.VerifyFacts

// --- deps (python env + external dependency provisioning) --------------------

type DependencyStatus = deps.DependencyStatus

const SandboxPyDepsMountPath = deps.SandboxPyDepsMountPath

var (
	EnsurePyDeps               = deps.EnsurePyDeps
	EnsurePyDepsDir            = deps.EnsurePyDepsDir
	PyDepsAvailable            = deps.PyDepsAvailable
	PrependPythonPath          = deps.PrependPythonPath
	CheckDependencies          = deps.CheckDependencies
	LogDependencyHealth        = deps.LogDependencyHealth
	SandboxPythonVersion       = deps.SandboxPythonVersion
	SandboxPythonAuthoringNote = deps.SandboxPythonAuthoringNote
)

// --- geo (reverse geocoding: offline GeoNames DB + Nominatim) ----------------

var (
	ReverseGeocode = geo.ReverseGeocode
	SetGeocodeDir  = geo.SetGeocodeDir
)

// --- wiring: inject core-side implementations into the leaves ----------------

func init() {
	// media: EXIF/GPS → place name, resolved by the geo leaf.
	media.ReverseGeocode = geo.ReverseGeocode

	// geo: permanent result cache (RootDB bucket) + the four download/HTTP
	// tunables. The cache also gates whether geocoding runs at all — a nil
	// bucket (no RootDB, e.g. CLI/test) makes ReverseGeocode skip.
	geo.Cache = func() geo.CacheBucket {
		if RootDB == nil {
			return nil
		}
		return RootDB.Bucket("geocode_cache")
	}
	geo.HTTPTimeout = func() time.Duration { return TuneDuration("tune_geocode_timeout") }
	geo.IdleTimeout = func() time.Duration { return TuneDuration("tune_geocode_idle_timeout") }
	geo.ConnectTimeout = func() time.Duration { return TuneDuration("tune_geocode_connect_timeout") }
	geo.MaxAttempts = func() int { return TuneInt("tune_geocode_max_attempts") }
	RegisterTunable(TunableSpec{Key: "tune_geocode_timeout", Category: "Timeouts", Label: "Geocode HTTP timeout", Help: "Per-request timeout for online (Nominatim) reverse-geocoding lookups.", Kind: KindSeconds, Default: 5, Min: 1, Max: 30})
	RegisterTunable(TunableSpec{Key: "tune_geocode_idle_timeout", Category: "Timeouts", Label: "Geocode download idle timeout", Help: "Moving-window no-bytes timeout for offline geocode-database downloads before falling to the next mirror.", Kind: KindSeconds, Default: 30, Min: 5, Max: 180})
	RegisterTunable(TunableSpec{Key: "tune_geocode_connect_timeout", Category: "Timeouts", Label: "Geocode connect timeout", Help: "Dial + TLS handshake cap for offline geocode-database downloads.", Kind: KindSeconds, Default: 10, Min: 2, Max: 60})
	RegisterTunable(TunableSpec{Key: "tune_geocode_max_attempts", Category: "Concurrency", Label: "Geocode download attempts", Help: "Number of mirror attempts when downloading the offline geocode database.", Kind: KindInt, Default: 3, Min: 1, Max: 10})

	// media: per-document extraction timeout, read from the tunables registry.
	media.ExtractTimeout = func() time.Duration { return TuneDuration("tune_document_extract_timeout") }
	RegisterTunable(TunableSpec{
		Key: "tune_document_extract_timeout", Category: "Timeouts",
		Label: "Document extraction timeout",
		Help:  "Per-document cap for text extraction (PDFs, office files) on a user send.",
		Kind:  KindSeconds, Default: 30, Min: 5, Max: 300,
	})

	// deps: the sandbox workspaces root (the managed python-deps dir is a sibling).
	deps.WorkspacesDir = WorkspacesDir
}
