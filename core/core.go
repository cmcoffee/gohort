package core

import (
	"context"
	"net/http"
	"time"

	"github.com/cmcoffee/gohort/core/appassets"
	"github.com/cmcoffee/gohort/core/appgroups"
	"github.com/cmcoffee/gohort/core/costledger"
	"github.com/cmcoffee/gohort/core/deps"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/gohort/core/extrasessions"
	"github.com/cmcoffee/gohort/core/factcheck"
	"github.com/cmcoffee/gohort/core/geo"
	"github.com/cmcoffee/gohort/core/injection"
	"github.com/cmcoffee/gohort/core/looptune"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/messaging"
	"github.com/cmcoffee/gohort/core/migrate"
	"github.com/cmcoffee/gohort/core/netgate"
	"github.com/cmcoffee/gohort/core/notes"
	"github.com/cmcoffee/gohort/core/ollama"
	"github.com/cmcoffee/gohort/core/promotion"
	"github.com/cmcoffee/gohort/core/prompts"
	"github.com/cmcoffee/gohort/core/provenance"
	"github.com/cmcoffee/gohort/core/pushsub"
	"github.com/cmcoffee/gohort/core/sections"
	"github.com/cmcoffee/gohort/core/sourcehooks"
	"github.com/cmcoffee/gohort/core/sources"
	"github.com/cmcoffee/gohort/core/sse"
	"github.com/cmcoffee/gohort/core/subsession"
	"github.com/cmcoffee/gohort/core/textutil"
	"github.com/cmcoffee/gohort/core/tlsconf"
	"github.com/cmcoffee/gohort/core/toolgroups"
	"github.com/cmcoffee/gohort/core/toolrules"
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
	BannedWordsRule          = textutil.BannedWordsRule
	TimeAwarenessRule        = textutil.TimeAwarenessRule
	UntrustedDataRule        = textutil.UntrustedDataRule
	UntrustedToolResultFence = textutil.UntrustedToolResultFence
	DraftFence               = textutil.DraftFence
	DraftReplyContract       = textutil.DraftReplyContract
)

var (
	MarkdownToPlain             = textutil.MarkdownToPlain
	StripMetaTags               = textutil.StripMetaTags
	StripToolCallTags           = textutil.StripToolCallTags
	StripEmDashes               = textutil.StripEmDashes
	StripPromptSectionsForTools = textutil.StripPromptSectionsForTools
	SnakeFromDisplay            = textutil.SnakeFromDisplay
	UntrustedData               = textutil.UntrustedData
	UntrustedFence              = textutil.UntrustedFence
	SplitDraftReply             = textutil.SplitDraftReply
	StripLeadingHeading         = textutil.StripLeadingHeading
)

// Markdown/HTML rendering, URL harvesting, XML field extraction, and the
// browser-routing heuristics — all pure text work, all stdlib-only.

type (
	ExtractSpec  = textutil.ExtractSpec
	ExtractWhere = textutil.ExtractWhere
	ExtractMatch = textutil.ExtractMatch
)

const ThinResultMinChars = textutil.ThinResultMinChars

var (
	// Link/citation patterns shared by anything that parses model output.
	BareURLPattern   = textutil.BareURLPattern
	MDLinkPattern    = textutil.MDLinkPattern
	TaggedSrcPattern = textutil.TaggedSrcPattern
	CiteRefPattern   = textutil.CiteRefPattern
	DomainPattern    = textutil.DomainPattern

	// Markdown ⇄ HTML.
	HTMLEscape            = textutil.HTMLEscape
	HTMLUnescape          = textutil.HTMLUnescape
	InlineMarkdownToHTML  = textutil.InlineMarkdownToHTML
	MarkdownToHTML        = textutil.MarkdownToHTML
	HTMLToMarkdown        = textutil.HTMLToMarkdown
	MarkdownToConfluence  = textutil.MarkdownToConfluence
	HeadingAnchor         = textutil.HeadingAnchor
	NormalizeHeadingLinks = textutil.NormalizeHeadingLinks

	// URL + section harvesting from a body of text.
	ExtractURLs         = textutil.ExtractURLs
	ExtractDomains      = textutil.ExtractDomains
	CountURLs           = textutil.CountURLs
	SplitSourcesSection = textutil.SplitSourcesSection
	StripSourcesSection = textutil.StripSourcesSection
	StripSection        = textutil.StripSection
	StripUncitedSources = textutil.StripUncitedSources
	StripInternalLabels = textutil.StripInternalLabels

	// XML field extraction (CalDAV/WebDAV responses and friends).
	ParseExtractSpec = textutil.ParseExtractSpec
	ExtractXML       = textutil.ExtractXML

	// When a plain fetch won't do and the browser has to drive.
	IsJSHeavyDomain          = textutil.IsJSHeavyDomain
	ShouldAutoBrowseURL      = textutil.ShouldAutoBrowseURL
	ShouldBrowserRetryResult = textutil.ShouldBrowserRetryResult

	// Same-origin path handed to a network tool → the hint that names it.
	SameOriginURLHint = textutil.SameOriginURLHint
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

// --- docs (writer-app document plumbing: rules, templates, doc targets) ------

type (
	DocItem                   = docs.DocItem
	DocumentTarget            = docs.DocumentTarget
	ReferencingDocumentTarget = docs.ReferencingDocumentTarget
)

var (
	// Per-user standing rules for a writer app, namespaced per app.
	DocRulesTable   = docs.DocRulesTable
	LoadDocRules    = docs.LoadDocRules
	FormatDocRules  = docs.FormatDocRules
	DocRulesSection = docs.DocRulesSection
	HandleDocRules  = docs.HandleDocRules

	// Assist prompt + the fence that keeps a draft out of the model's prose.
	DocFence             = docs.DocFence
	BuildDocAssistPrompt = docs.BuildDocAssistPrompt
	MarkdownDocTemplates = docs.MarkdownDocTemplates

	// Document targets — where an app can push a finished section.
	RegisterDocumentTarget   = docs.RegisterDocumentTarget
	DocumentTargetKinds      = docs.DocumentTargetKinds
	ListDocuments            = docs.ListDocuments
	ListDocumentsReferencing = docs.ListDocumentsReferencing
	AppendToDocument         = docs.AppendToDocument
	HasDocumentTarget        = docs.HasDocumentTarget
)

// --- messaging (seams the transport registers so apps needn't import it) -----

type (
	MessagingChatSummary = messaging.MessagingChatSummary
	MessagingChatMessage = messaging.MessagingChatMessage
	MessagingLink        = messaging.MessagingLink
	ChannelThreadInfo    = messaging.ChannelThreadInfo
	ChannelLine          = messaging.ChannelLine
	ChannelMember        = messaging.ChannelMember
	ChannelThreads       = messaging.ChannelThreads
	ChannelSearcher      = messaging.ChannelSearcher
)

var (
	RegisterMessagingLink  = messaging.RegisterMessagingLink
	ActiveMessagingLink    = messaging.ActiveMessagingLink
	RegisterChannelThreads = messaging.RegisterChannelThreads
	ActiveChannelThreads   = messaging.ActiveChannelThreads
)

// Channel records — an agent's bindings onto a messaging service.

type (
	Channel                   = messaging.Channel
	BridgeService             = messaging.BridgeService
	ChannelInbound            = messaging.ChannelInbound
	ChannelReply              = messaging.ChannelReply
	ChannelAgentRunnerFunc    = messaging.ChannelAgentRunnerFunc
	ChannelGatekeeperFunc     = messaging.ChannelGatekeeperFunc
	ChannelSilentRecorderFunc = messaging.ChannelSilentRecorderFunc
	ChannelOverflowFunc       = messaging.ChannelOverflowFunc
)

const (
	DefaultDMGatekeeperRule = messaging.DefaultDMGatekeeperRule
	DirectionInbound        = messaging.DirectionInbound
	DirectionOutbound       = messaging.DirectionOutbound
	DirectionBidirectional  = messaging.DirectionBidirectional
)

var (
	ServiceDisplayName    = messaging.ServiceDisplayName
	ServiceRendersMarkdown = messaging.ServiceRendersMarkdown
	LooksLikeHandle       = messaging.LooksLikeHandle
	ChannelDirection      = messaging.ChannelDirection
	NewChannelID          = messaging.NewChannelID
	SaveChannel           = messaging.SaveChannel
	GetChannel            = messaging.GetChannel
	ListChannels          = messaging.ListChannels
	ListChannelsForAgent  = messaging.ListChannelsForAgent
	ChannelAllowsSender   = messaging.ChannelAllowsSender
	DeleteChannel         = messaging.DeleteChannel
	ChannelSessionKey     = messaging.ChannelSessionKey
	ChannelForInbound     = messaging.ChannelForInbound

	RegisterChannelAgentRunner    = messaging.RegisterChannelAgentRunner
	ChannelAgentRunnerReady       = messaging.ChannelAgentRunnerReady
	RunChannelAgent               = messaging.RunChannelAgent
	RegisterChannelGatekeeper     = messaging.RegisterChannelGatekeeper
	ChannelGatekeeperAllow        = messaging.ChannelGatekeeperAllow
	RegisterChannelSilentRecorder = messaging.RegisterChannelSilentRecorder
	RecordChannelSilent           = messaging.RecordChannelSilent
	RegisterChannelOverflow       = messaging.RegisterChannelOverflow
	OverflowChannelReply          = messaging.OverflowChannelReply
)

// --- sections (admin/account page surfaces an app contributes) ---------------

type (
	AdminSectionEntry   = sections.AdminSectionEntry
	AccountSectionEntry = sections.AccountSectionEntry
)

var (
	RegisterAdminSection   = sections.RegisterAdminSection
	AdminSectionEntries    = sections.AdminSectionEntries
	RegisterAccountSection = sections.RegisterAccountSection
	AccountSectionEntries  = sections.AccountSectionEntries
)

// --- toolrules (what a tool may be named, and how much prose it may cost) ----

var (
	RegisterReservedToolName = toolrules.RegisterReservedToolName
	IsReservedToolName       = toolrules.IsReservedToolName
	CheckDescriptionBudget   = toolrules.CheckDescriptionBudget
	CheckAuthoredToolText    = toolrules.CheckAuthoredToolText
)

// --- netgate (inbound request trust + outbound network capability) -----------

type NetworkConnector = netgate.NetworkConnector

var (
	IsAdminAllowed        = netgate.IsAdminAllowed
	IsLoopbackRequest     = netgate.IsLoopbackRequest
	IsGenuineLocalRequest = netgate.IsGenuineLocalRequest
	IsBrowserRequest      = netgate.IsBrowserRequest
	// Exported on the way out: core's auth + webapp call it 8 times.
	ClientIP = netgate.ClientIP

	NewNetworkConnector         = netgate.NewNetworkConnector
	WithNetworkConnector        = netgate.WithNetworkConnector
	NetworkConnectorFromContext = netgate.NetworkConnectorFromContext
	NetworkAllowedFromContext   = netgate.NetworkAllowedFromContext
)

// netgate.LoadAdminAllowedIPsFunc is deliberately NOT re-exported: it is a
// mutable func var, and a var alias would copy it — the assignment in
// gohort.go must land on the leaf's own var to be seen by IsAdminAllowed.

// --- appassets (per-app static files an app can reference) -------------------

const (
	MaxAppAssetBytes = appassets.MaxAppAssetBytes
	MaxAppAssets     = appassets.MaxAppAssets
)

var (
	SetAppAssetsDir     = appassets.SetAppAssetsDir
	AppAssetsDir        = appassets.AppAssetsDir
	AppAssetContentType = appassets.AppAssetContentType
	ValidAppAssetName   = appassets.ValidAppAssetName
	SaveAppAsset        = appassets.SaveAppAsset
	ReadAppAsset        = appassets.ReadAppAsset
	ListAppAssets       = appassets.ListAppAssets
	DeleteAppAsset      = appassets.DeleteAppAsset
	DeleteAppAssets     = appassets.DeleteAppAssets
)

// --- sse (server-sent-events response writer) --------------------------------

type SSEWriter = sse.SSEWriter

var NewSSEWriter = sse.NewSSEWriter

// --- notes (per-namespace operating notes an agent may rewrite) --------------

type OperatingNotes = notes.OperatingNotes

const (
	OperatingNotesTable = notes.OperatingNotesTable
	OperatingNotesCap   = notes.OperatingNotesCap
)

var (
	LoadOperatingNotes        = notes.LoadOperatingNotes
	SaveOperatingNotes        = notes.SaveOperatingNotes
	ResolveOperatingNotes     = notes.ResolveOperatingNotes
	RenderOperatingNotesBlock = notes.RenderOperatingNotesBlock
)

// --- prompts (registered prompt blocks + deployment-level overrides) ---------

type PromptBlock = prompts.PromptBlock

var (
	RegisterPromptBlock  = prompts.RegisterPromptBlock
	AllPromptBlocks      = prompts.AllPromptBlocks
	SetPromptOverrideDB  = prompts.SetPromptOverrideDB
	PromptOverride       = prompts.PromptOverride
	SetPromptOverride    = prompts.SetPromptOverride
	ClearPromptOverride  = prompts.ClearPromptOverride
	EffectivePromptText  = prompts.EffectivePromptText
)

// --- costledger (metered per-source external spend) --------------------------

type (
	CostLedgerRow     = costledger.CostLedgerRow
	CostSourceSummary = costledger.CostSourceSummary
)

var (
	SetCostLedgerDB    = costledger.SetCostLedgerDB
	RecordExternalCost = costledger.RecordExternalCost
	CostExternalDaily  = costledger.CostExternalDaily
	CostBySource       = costledger.CostBySource
)

// --- pushsub (web-push subscription lifecycle) -------------------------------

type PushSubHandler = pushsub.PushSubHandler

var (
	RegisterPushSubHandler  = pushsub.RegisterPushSubHandler
	EnsurePushSubscription  = pushsub.EnsurePushSubscription
	RemovePushSubscription  = pushsub.RemovePushSubscription
)

// --- migrate (one-shot data migrations with a done-marker) -------------------

type (
	MigrationMarker = migrate.MigrationMarker
	MigrationRunner = migrate.MigrationRunner
)

const MigrationsTable = migrate.MigrationsTable

var (
	NewMigrationRunner   = migrate.NewMigrationRunner
	ListMigrationMarkers = migrate.ListMigrationMarkers
)

// --- tlsconf (self-signed cert generation + TLS listener) --------------------

var (
	TLSEnabled           = tlsconf.TLSEnabled
	TLSSelfSignedEnabled = tlsconf.SelfSigned
	ListenAndServeTLS = tlsconf.ListenAndServeTLS
)

// --- ollama (local-model slot schedulers: ollama + llama.cpp) ---------------

type (
	OllamaScheduler  = ollama.OllamaScheduler
	OllamaSchedStats = ollama.OllamaSchedStats
)

var (
	ErrOllamaSchedulerDisabled = ollama.ErrOllamaSchedulerDisabled
	StartOllamaScheduler       = ollama.StartOllamaScheduler
	AcquireOllamaSlot          = ollama.AcquireOllamaSlot
	ReleaseOllamaSlot          = ollama.ReleaseOllamaSlot
	OllamaSchedulerStats       = ollama.OllamaSchedulerStats
	StartLlamacppScheduler     = ollama.StartLlamacppScheduler
	AcquireLlamacppSlot        = ollama.AcquireLlamacppSlot
	ReleaseLlamacppSlot        = ollama.ReleaseLlamacppSlot
)

// --- injection (queued notes pushed into a running turn) --------------------

type (
	InjectionNote  = injection.InjectionNote
	InjectionQueue = injection.InjectionQueue
)

const MaxInjectionQueueDepth = injection.MaxInjectionQueueDepth

var (
	RegisterInjectionQueue           = injection.RegisterInjectionQueue
	LookupInjectionQueue             = injection.LookupInjectionQueue
	ReleaseInjectionQueue            = injection.ReleaseInjectionQueue
	SubSessionInjectionQueueKey      = injection.SubSessionInjectionQueueKey
	RegisterSubSessionInjectionQueue = injection.RegisterSubSessionInjectionQueue
	LookupSubSessionInjectionQueue   = injection.LookupSubSessionInjectionQueue
	ReleaseSubSessionInjectionQueue  = injection.ReleaseSubSessionInjectionQueue
)

// --- provenance (where a memory came from, and how stale it is) -------------

type (
	MemSource        = provenance.MemSource
	Volatility       = provenance.Volatility
	RetireReason     = provenance.RetireReason
	MemoryProvenance = provenance.MemoryProvenance
	Staleness        = provenance.Staleness
	ClaimDomain      = provenance.ClaimDomain
)

const (
	TunableRecencyWeight     = provenance.TunableRecencyWeight
	TunableStaleSlowDays     = provenance.TunableStaleSlowDays
	TunableStaleVolatileDays = provenance.TunableStaleVolatileDays

	// Where a memory came from.
	MemSourceUnknown    = provenance.MemSourceUnknown
	MemSourceUserStated = provenance.MemSourceUserStated
	MemSourceObserved   = provenance.MemSourceObserved
	MemSourceRetrieved  = provenance.MemSourceRetrieved
	MemSourceInferred   = provenance.MemSourceInferred
	MemSourceImported   = provenance.MemSourceImported

	// Whether the source's word SETTLES the claim.
	ClaimDomainUnknown = provenance.ClaimDomainUnknown
	ClaimSelf          = provenance.ClaimSelf
	ClaimWorld         = provenance.ClaimWorld

	// How fast it goes out of date.
	VolStable   = provenance.VolStable
	VolSlow     = provenance.VolSlow
	VolVolatile = provenance.VolVolatile

	// Why it was retired.
	RetireLive       = provenance.RetireLive
	RetireMerged     = provenance.RetireMerged
	RetireSuperseded = provenance.RetireSuperseded
	RetireEvicted    = provenance.RetireEvicted

	// How stale it reads right now.
	Fresh = provenance.Fresh
	Aging = provenance.Aging
	Stale = provenance.Stale
)

var (
	RetireReasonLabel    = provenance.RetireReasonLabel
	RecencyWeight        = provenance.RecencyWeight
	SourceTrust          = provenance.SourceTrust
	SpeakerAuthoritative = provenance.SpeakerAuthoritative
	NeedsAttribution     = provenance.NeedsAttribution
	AttributionPhrase    = provenance.AttributionPhrase
	SpeakerLabel         = provenance.SpeakerLabel
	ClaimAuthority       = provenance.ClaimAuthority
)

// --- subsession (a delegated turn's lifecycle record) -----------------------

type (
	SubSessionStatus          = subsession.SubSessionStatus
	SubSessionMode            = subsession.SubSessionMode
	SubSessionKind            = subsession.SubSessionKind
	SubSession                = subsession.SubSession
	SubSessionLivenessChecker = subsession.SubSessionLivenessChecker
)

const (
	SubSessionsTable = subsession.SubSessionsTable

	SubSessionActive  = subsession.SubSessionActive
	SubSessionIdle    = subsession.SubSessionIdle
	SubSessionRetired = subsession.SubSessionRetired

	SubSessionModeSync  = subsession.SubSessionModeSync
	SubSessionModeAsync = subsession.SubSessionModeAsync

	SubSessionKindNormal     = subsession.SubSessionKindNormal
	SubSessionKindAutonomous = subsession.SubSessionKindAutonomous
)

var (
	MintSubSession                    = subsession.MintSubSession
	MarkSubSessionIdle                = subsession.MarkSubSessionIdle
	MarkSubSessionActive              = subsession.MarkSubSessionActive
	RetireSubSession                  = subsession.RetireSubSession
	GetSubSession                     = subsession.GetSubSession
	IdleSubSessionsFor                = subsession.IdleSubSessionsFor
	ActiveSubSessionsFor              = subsession.ActiveSubSessionsFor
	RegisterSubSessionLivenessChecker = subsession.RegisterSubSessionLivenessChecker
	RetireOrphanedActiveSubSessions   = subsession.RetireOrphanedActiveSubSessions
	MostRecentActiveSubSession        = subsession.MostRecentActiveSubSession
	SubSessionIsLive                  = subsession.SubSessionIsLive
)

// --- toolgroups (admin-curated groupings of tools) --------------------------

type ToolGroup = toolgroups.ToolGroup

var (
	LoadToolGroups       = toolgroups.LoadToolGroups
	LoadToolGroup        = toolgroups.LoadToolGroup
	SaveToolGroup        = toolgroups.SaveToolGroup
	DeleteToolGroup      = toolgroups.DeleteToolGroup
	ToolGroupForMember   = toolgroups.ToolGroupForMember
	MemberSet            = toolgroups.MemberSet
	IsBuiltinToolGroupID  = toolgroups.IsBuiltinToolGroupID
	DedupeAndCleanMembers = toolgroups.DedupeAndCleanMembers
)

// --- sources (citation records + which sources are worth citing) ------------

type (
	SourceRef      = sources.SourceRef
	NumberedSource = sources.NumberedSource
	SourceRegistry = sources.SourceRegistry
)

var (
	RewriteCitations = sources.RewriteCitations
	NormalizeURL     = sources.NormalizeURL
	ParseSourceIndex = sources.ParseSourceIndex
	IsWeakSource     = sources.IsWeakSource
)

// --- extrasessions (rail entries another app contributes to an agent) -------

type (
	ExtraSessionItem    = extrasessions.ExtraSessionItem
	ExtraSessionsSource = extrasessions.ExtraSessionsSource
)

var (
	RegisterExtraSessionsSource = extrasessions.RegisterExtraSessionsSource
	LookupExtraSessionsSource   = extrasessions.LookupExtraSessionsSource
	CollectExtraSessions        = extrasessions.CollectExtraSessions
)

// --- appgroups (named bundles of apps a user can be granted at once) --------

type AppGroup = appgroups.AppGroup

var (
	LoadAppGroups   = appgroups.LoadAppGroups
	LoadAppGroup    = appgroups.LoadAppGroup
	SaveAppGroup    = appgroups.SaveAppGroup
	DeleteAppGroup  = appgroups.DeleteAppGroup
	ExpandAppGroups = appgroups.ExpandAppGroups
)

// --- looptune (agent-loop round/spend limits) -------------------------------

type AgentLoopTuning = looptune.AgentLoopTuning

var (
	SetAgentLoopTuning       = looptune.SetAgentLoopTuning
	GetAgentLoopTuning       = looptune.GetAgentLoopTuning
	LoadAgentLoopTuningFromDB = looptune.LoadAgentLoopTuningFromDB
	SaveAgentLoopTuningToDB  = looptune.SaveAgentLoopTuningToDB
	InitAgentLoopTuning      = looptune.InitAgentLoopTuning
)

// --- promotion (which sub-session the next user turn should join) -----------

type (
	RouteAction      = promotion.RouteAction
	PromotionRequest = promotion.PromotionRequest
)

var (
	StripPromotionEscape    = promotion.StripPromotionEscape
	PromotionWindow         = promotion.PromotionWindow
	PromotionTurnCap        = promotion.PromotionTurnCap
	ResolveDispatchRoute    = promotion.ResolveDispatchRoute
	ResolvePromotion        = promotion.ResolvePromotion
	CreatePromotionRequest  = promotion.CreatePromotionRequest
	ListPromotionRequests   = promotion.ListPromotionRequests
	GetPromotionRequest     = promotion.GetPromotionRequest
	SetPromotionRequestState = promotion.SetPromotionRequestState
	PendingPromotion        = promotion.PendingPromotion
	PromotionRequestKey     = promotion.RequestKey
)

const (
	PromotionPendingState  = promotion.PromotionPendingState
	PromotionApprovedState = promotion.PromotionApprovedState
	PromotionDeniedState   = promotion.PromotionDeniedState

	RouteNone    = promotion.RouteNone
	RoutePromote = promotion.RoutePromote
	RouteInject  = promotion.RouteInject
	RouteGoal    = promotion.RouteGoal
)

// --- sourcehooks (per-domain fetch/index/search engine + its cache) ---------

type (
	SourceHook         = sourcehooks.SourceHook
	SourceHookAuth     = sourcehooks.SourceHookAuth
	SourceHookType     = sourcehooks.SourceHookType
	SourceHookTemplate = sourcehooks.SourceHookTemplate
)

const (
	HookAuthNone   = sourcehooks.HookAuthNone
	HookAuthAPIKey = sourcehooks.HookAuthAPIKey
	HookAuthBearer = sourcehooks.HookAuthBearer

	HookTypeAPI     = sourcehooks.HookTypeAPI
	HookTypeRAG     = sourcehooks.HookTypeRAG
	HookTypePaywall = sourcehooks.HookTypePaywall
)

var (
	LoadSourceHooks       = sourcehooks.LoadSourceHooks
	SaveSourceHook        = sourcehooks.SaveSourceHook
	DeleteSourceHook      = sourcehooks.DeleteSourceHook
	RegisteredSourceHooks = sourcehooks.RegisteredSourceHooks
	ActiveSourceHooks     = sourcehooks.ActiveSourceHooks
	HookForDomain         = sourcehooks.HookForDomain
	HooksForTopic         = sourcehooks.HooksForTopic
	SourceHookTemplates   = sourcehooks.SourceHookTemplates
	QuerySourceHook       = sourcehooks.QuerySourceHook
	ApplyPaywallAuth      = sourcehooks.ApplyPaywallAuth
	ApplyHTTPTimeouts     = sourcehooks.ApplyHTTPTimeouts
	NewBoundedHTTPClient  = sourcehooks.NewBoundedHTTPClient
	SetHookCacheDB        = sourcehooks.SetHookCacheDB
	StartHookCacheSweeper = sourcehooks.StartHookCacheSweeper
	SweepHookCache        = sourcehooks.SweepHookCache
	StoreAuthDomainCache  = sourcehooks.StoreAuthDomainCache
	LookupAuthDomainCache = sourcehooks.LookupAuthDomainCache

	// Accessors, not the vars: ApplyHTTPTimeouts rewrites those at runtime.
	HTTPRequestTimeout = sourcehooks.RequestTimeout
	HTTPConnectTimeout = sourcehooks.ConnectTimeout
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

	// prompts: keep the override table name single-sourced from tables.go.
	prompts.OverrideTable = WebTable

	// pushsub + migrate: the root store, resolved late — RootDB is nil until
	// the database opens, and both leaves already treat a nil store as "not
	// ready" exactly as they did when they read RootDB directly.
	pushsub.DB = func() pushsub.Store {
		if RootDB == nil {
			return nil
		}
		return RootDB
	}
	migrate.DB = func() migrate.Store {
		if RootDB == nil {
			return nil
		}
		return RootDB
	}

	// docs: request authentication. The leaf works in its own Store terms, so
	// the wire adapts rather than assigns — Go function types are invariant,
	// and core's RequireUser is written against Database.
	docs.RequireUser = func(w http.ResponseWriter, r *http.Request, base docs.Store) (string, docs.Store, bool) {
		db, _ := base.(Database)
		user, udb, ok := RequireUser(w, r, db)
		if udb == nil {
			return user, nil, ok
		}
		return user, udb, ok
	}
	// subsession: the root store, resolved late (see pushsub above).
	subsession.DB = func() subsession.Store {
		if RootDB == nil {
			return nil
		}
		return RootDB
	}

	// appgroups + injection + toolgroups: framework-shaped IDs.
	appgroups.NewID = UUIDv4
	injection.NewID = UUIDv4
	toolgroups.NewID = UUIDv4

	// sources: the classifier stays with the search pipeline whose domain
	// tables it shares; the sources leaf reads it through these.
	sources.ClassifySourceFunc = ClassifySource
	sources.CleanSourceTitleFunc = CleanSourceTitle
	sources.IsNonArticleURLFunc = IsNonArticleURL

	// sourcehooks: the deployment facts the engine can't carry with it.
	sourcehooks.ContactEmail = func() string { return LoadMailConfig().From }
	sourcehooks.AppVersion = func() string { return AppVersion }
	sourcehooks.NetworkConfigTable = NetworkTable
	// Registered HERE rather than from the leaf: a leaf's init runs before
	// this one, so a hook-based registration lands while the hook is still nil
	// and the action never appears. core owns the registry, so core makes the
	// call — same rule as the tunables above.
	RegisterMaintenanceFunc(
		"sweep_expired_caches",
		"Sweep expired caches",
		"Remove expired entries from the source-hook result cache and the "+
			"authoritative-domain cache (past their 30-day / 7-day TTLs). Lazy "+
			"delete-on-read already reclaims entries that get re-queried after "+
			"expiry; this reclaims the long tail that is never queried again.",
		func(ctx context.Context) int { return sourcehooks.SweepHookCache() },
	)

	// promotion: reads two tunables; the registry (and so the admin surface
	// and the defaults) stays here, which is why core makes the calls.
	promotion.TuneDurationFunc = TuneDuration
	promotion.TuneIntFunc = TuneInt
	RegisterTunable(TunableSpec{Key: "tune_promotion_window", Category: "Timeouts", Label: "Sub-session stickiness window", Help: "How long an idle sub-session stays joinable after its last reply before the next turn falls through to the host LLM.", Kind: KindMinutes, Default: 5, Min: 1, Max: 60})
	RegisterTunable(TunableSpec{Key: "tune_promotion_turn_cap", Category: "Limits", Label: "Sub-session turn cap", Help: "Maximum number of follow-up turns a single promoted sub-session will serve.", Kind: KindInt, Default: 8, Min: 1, Max: 50})

	// provenance: recency/staleness weights come from the tunables registry.
	provenance.TuneFloatFunc = TuneFloat
	provenance.TuneIntFunc = TuneInt
}