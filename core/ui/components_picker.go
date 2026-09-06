package ui

import (
	"encoding/json"
)

// ChipPicker is the framework's one multi-select-over-a-record-field
// picker. It binds an array selection to a record (or a dedicated
// endpoint) with options pulled from a GET. Two display modes cover the
// whole spectrum:
//
//   - "chips" (default): every option renders as a toggle chip, all
//     visible at once. Best for small, stable option sets (admin
//     app/group allowlists, per-conversation tool selection).
//   - "attach": the SELECTED options render as removable pills, and a
//     "+ Add <Noun>" control reveals the remaining options on demand,
//     each with a description, an optional meta line, and a "+" button.
//     Best for large catalogs — document collections, capabilities,
//     pipelines, reference sources.
//
// It is domain-agnostic: apps supply endpoints, field names, and copy.
// Composite-key or grouped domains adapt their *server* response to this
// generic contract (a flat option array with a group field and a scalar
// stored value) rather than teaching this component their shapes.
type ChipPicker struct {
	// Mode is "chips" (default) or "attach". See the type doc.
	Mode string `json:"mode,omitempty"`

	OptionsSource string `json:"options_source"` // GET — array, or a shaped object (see RecordsField)
	// RecordsField overrides the auto-detected array key when
	// OptionsSource returns a shaped object ({records|items|…: [...]}).
	RecordsField string `json:"records_field,omitempty"`

	// Current selection comes from ONE of two sources:
	//   (a) RecordSource + Field — GET a record, read its array Field.
	//       Saving replaces that record (or PATCHes just Field).
	RecordSource string `json:"record_source,omitempty"`
	Field        string `json:"field,omitempty"` // array field on the record
	//   (b) AttachedField — the OptionsSource response itself carries the
	//       current selection under this key (array of stored values).
	//       Pair with SaveKey to POST the selection to a dedicated
	//       endpoint. No separate record fetch happens.
	AttachedField string `json:"attached_field,omitempty"`

	PostTo string `json:"post_to"`          // save destination
	Method string `json:"method,omitempty"` // default POST; PATCH sends only the changed Field
	// SaveKey, when set, POSTs the selection as {SaveKey: [values]}
	// (dedicated-endpoint mode). Blank = full-record mode: the fetched
	// RecordSource record is patched at Field and posted whole.
	SaveKey string `json:"save_key,omitempty"`

	// NameField is the option key whose value gets STORED in the
	// selection array (e.g. "/phantom" path string). Default "name".
	NameField string `json:"name_field,omitempty"`
	// ValueField overrides the STORED key when it differs from the
	// display name (default = NameField).
	ValueField string `json:"value_field,omitempty"`
	// LabelField is the option key rendered as the chip/pill text. When
	// unset, shows NameField. Use when the stored value isn't
	// human-readable (store a URL path, display the app's friendly name).
	LabelField string `json:"label_field,omitempty"`
	// DescField is the option key for tooltip / attach-row description.
	// Default "desc".
	DescField string `json:"desc_field,omitempty"`
	// GroupByField groups the attach-mode option list under headers by
	// this option key (e.g. reference sources grouped by kind). Blank =
	// no grouping. Ignored in chips mode.
	GroupByField string `json:"group_by_field,omitempty"`
	// MetaFields render a compact "12 documents · 44 chunks" line beneath
	// each attach-row: for each listed key present on the option, the
	// value is shown followed by the key name. Ignored in chips mode.
	MetaFields []string `json:"meta_fields,omitempty"`

	// Attach-mode copy (all optional).
	Noun      string `json:"noun,omitempty"`       // "+ Add <Noun>". Default "item".
	Intro     string `json:"intro,omitempty"`      // help line above the picker
	EmptyText string `json:"empty_text,omitempty"` // shown when there are no options

	// Invalidate — data sources to refetch after a successful save, same
	// contract as FormPanel.Invalidate and RowAction.Invalidate. A picker
	// often writes a field some OTHER list on the page renders (filing tools
	// into a category changes the heading each tool sits under in the tools
	// table); without this that list stays stale until a manual reload, which
	// reads as "the picker didn't take".
	Invalidate []string `json:"invalidate,omitempty"`
}

func (ChipPicker) componentType() string { return "chip_picker" }

func (c ChipPicker) MarshalJSON() ([]byte, error) {
	type alias ChipPicker
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"chip_picker", alias(c)})
}

// UploadPanel is the framework's file-upload surface: pick or drop several
// files, watch each one's progress, retry the one that failed without
// re-sending the rest, then (optionally) kick off server-side processing and
// poll until it finishes.
//
// It exists because "post a file and wait" is not one interaction but four —
// select, transfer, process, report — and every app that needed it was
// otherwise going to hand-roll an <input type=file> plus a fetch plus a
// spinner, and get the middle two wrong. A browser gives no progress for a
// fetch() upload and no way to resume one, so a 400 MB POST behind a fetch is
// indistinguishable from a hang.
//
// Deliberately domain-agnostic: it knows about files, an endpoint, and a status
// field to watch. It knows nothing about what is being uploaded or what the
// server does with it. Sequencing is one request per file (so progress and
// retry are per file), then one POST to FinalizeURL, then polling StatusURL.
//
// Typical use is inside an app's own modal via window.uiMountComponent, with
// the app supplying its own endpoints and status vocabulary.
type UploadPanel struct {
	// URL receives one POST per file as multipart/form-data. It is called
	// once per file, not once per batch.
	URL string `json:"url"`
	// Field is the multipart field name each file is sent under. Default
	// "file".
	Field string `json:"field,omitempty"`
	// ResetParam, when set, is appended as "<ResetParam>=1" to the FIRST
	// file's URL of a batch. Gives the server a way to distinguish "start of
	// a new upload" from "another file in the current one" without the
	// panel needing a separate begin-transaction endpoint.
	ResetParam string `json:"reset_param,omitempty"`
	// Accept is the file input's accept attribute (e.g. ".tar.gz,.log,.txt").
	Accept string `json:"accept,omitempty"`
	// Multiple allows selecting more than one file at a time. Default true.
	Multiple *bool `json:"multiple,omitempty"`
	// MaxBytes rejects an oversized file in the browser, before any of it is
	// sent. 0 means no client-side limit — the server still has its own.
	MaxBytes int64 `json:"max_bytes,omitempty"`
	// FinalizeURL is POSTed once, after every file has uploaded
	// successfully. FinalizeBody is sent as its JSON body. Leave empty when
	// the per-file POST is the whole interaction.
	FinalizeURL  string         `json:"finalize_url,omitempty"`
	FinalizeBody map[string]any `json:"finalize_body,omitempty"`
	// StatusURL is GETed on a timer after finalize, to follow work that
	// outlives the request. StatusField names the key in that response
	// holding the state; StatusDone lists the terminal values;
	// StatusErrorField names the key holding a failure reason, shown to the
	// user when the state lands on one of StatusFailed.
	StatusURL        string   `json:"status_url,omitempty"`
	StatusField      string   `json:"status_field,omitempty"`
	StatusDone       []string `json:"status_done,omitempty"`
	StatusFailed     []string `json:"status_failed,omitempty"`
	StatusErrorField string   `json:"status_error_field,omitempty"`
	// StatusLabels maps a raw state value to what the user should read
	// ("ingesting" → "Expanding and indexing…"). A state with no entry is
	// shown as-is rather than hidden: an unlabelled state is still progress.
	StatusLabels map[string]string `json:"status_labels,omitempty"`
	// PollSeconds is the status poll interval. Default 3.
	PollSeconds int `json:"poll_seconds,omitempty"`
	// ButtonLabel overrides the upload button ("Upload" by default).
	ButtonLabel string `json:"button_label,omitempty"`
	// Note renders under the drop zone — the place to say what the server
	// will do with these files, which is the app's business and not the
	// panel's.
	Note string `json:"note,omitempty"`
	// ReloadOnDone reloads the page when the status reaches a done value.
	// For a surface whose surrounding chrome was rendered server-side and is
	// now stale.
	ReloadOnDone bool `json:"reload_on_done,omitempty"`
}

func (UploadPanel) componentType() string { return "upload_panel" }

func (c UploadPanel) MarshalJSON() ([]byte, error) {
	type alias UploadPanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"upload_panel", alias(c)})
}

// ACLPickerConfig configures ACLPicker. Always set OptionsSource (the candidate
// list) and PostTo (the save target). The current selection comes from ONE of two
// modes:
//   - Record mode (preferred when the ACL lives on an existing editable record —
//     a credential, an agent): set RecordSource + Field. The picker reads the
//     array Field from that record and saves by patching Field and POSTing the
//     whole record back to PostTo — exactly the App-Groups pattern.
//   - Attach mode (a standalone ACL with no owning form): leave RecordSource
//     empty. The OptionsSource response itself carries the current selection under
//     Attached, and the picker POSTs {SaveKey: [values]} to PostTo.
type ACLPickerConfig struct {
	// OptionsSource GETs the candidate rows [{value,label,desc?}]. Map your domain
	// onto {value,label} server-side — e.g. AuthListUsers → {value: username,
	// label: username}. (In attach mode it ALSO carries the current selection; see
	// Attached.)
	OptionsSource string
	// RecordSource + Field select record mode: GET this record, read its array
	// Field as the current selection; saving patches Field and POSTs the record.
	RecordSource string
	Field        string
	// Attached (attach mode only) names the OptionsSource response key holding the
	// current selection. Default "allowed_users". Ignored when RecordSource is set.
	Attached string
	// PostTo receives the save. SaveKey (attach mode) is the body key the selection
	// POSTs under; default = Attached.
	PostTo  string
	SaveKey string
	// Method overrides the save method (default POST; use PATCH for changed-field
	// saves).
	Method string
	// Noun fills "+ Add <Noun>". Default "user".
	Noun string
	// Intro is an optional help line above the picker.
	Intro string
	// EmptyText shows when there are no candidates.
	EmptyText string
	// Invalidate — sources to refetch after a save (see ChipPicker.Invalidate).
	Invalidate []string
}

// ACLPicker builds a ChipPicker preconfigured as an access-control editor: a
// dynamic multi-select over a candidate list whose selection is saved as a
// []string. It is the standard editor for an AllowedUsers-style ACL across
// credentials, tools, and shared agents, so those surfaces share one shape
// instead of each re-deriving the ChipPicker field mapping. It adds no new
// component or client code — it's pure configuration of the generic ChipPicker.
func ACLPicker(c ACLPickerConfig) ChipPicker {
	noun := c.Noun
	if noun == "" {
		noun = "user"
	}
	cp := ChipPicker{
		Mode:          "attach",
		OptionsSource: c.OptionsSource,
		PostTo:        c.PostTo,
		Method:        c.Method,
		NameField:     "value",
		LabelField:    "label",
		DescField:     "desc",
		Noun:          noun,
		Intro:         c.Intro,
		EmptyText:     c.EmptyText,
		Invalidate:    c.Invalidate,
	}
	if c.RecordSource != "" {
		// Record mode: current selection + save both go through the owning record.
		cp.RecordSource = c.RecordSource
		cp.Field = c.Field
		return cp
	}
	// Attach mode: selection rides on the OptionsSource response; save to SaveKey.
	attached := c.Attached
	if attached == "" {
		attached = "allowed_users"
	}
	saveKey := c.SaveKey
	if saveKey == "" {
		saveKey = attached
	}
	cp.AttachedField = attached
	cp.SaveKey = saveKey
	return cp
}
