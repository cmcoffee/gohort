package core

// Centralized database table names. All packages should reference these
// constants rather than re-declaring or hard-coding the underlying strings,
// so a typo is a compile error rather than a silent miss in kvlite.
const (
	LLMTable         = "llm_config"
	LeadLLMTable     = "lead_llm_config"
	MailTable        = "mail_config"
	SearchTable      = "search_config"
	ImageTable       = "image_config"
	EmbeddingTable   = "embedding_config" // endpoint + model for vector-DB ingestion
	EmbeddedChunks   = "embedded_chunks"  // stored per-chunk vectors keyed by chunk ID
	RoutingTable     = "llm_routing"
	WebTable         = "web_config"
	AuthTable        = "auth_config"
	AuthSessionTable = "auth_sessions"
	AuthResetTable   = "auth_reset_tokens"
	NetworkTable     = "network_config"
	VoiceTable       = "voice_config"
)
