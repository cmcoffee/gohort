package core

// Per-request cost attribution.
//
// The middleware's "Est. cost" line used to diff the PROCESS-WIDE tracker
// across the request's lifetime, so one request's line absorbed every
// concurrent user turn, scheduled run, and background pipeline that
// overlapped it. The lines over-counted individually, over-counted worse
// when summed, and never reconciled with the admin chart. Now a fresh
// tracker rides the request context and the instrumentation credits it
// alongside the global — the line reports the request's own spend.

import (
	"context"
	"testing"
)

func TestRequestTrackerSeesOnlyItsOwnRequest(t *testing.T) {
	ctx, reqUsage := WithRequestUsage(context.Background())
	app := &AppCore{}

	// This request's call.
	app.trackTokens(ctx, &Response{InputTokens: 100, OutputTokens: 10})
	// A concurrent call on a context NOT belonging to this request —
	// background pipeline, another user's turn.
	app.trackLeadTokens(context.Background(), &Response{InputTokens: 5_000, OutputTokens: 500})

	d := reqUsage.Snapshot()
	if d.WorkerInput != 100 || d.WorkerOutput != 10 {
		t.Errorf("request tracker worker = in %d / out %d, want 100 / 10", d.WorkerInput, d.WorkerOutput)
	}
	if d.LeadInput != 0 || d.LeadOutput != 0 {
		t.Errorf("request tracker lead = in %d / out %d, want 0 / 0 — the background call leaked in",
			d.LeadInput, d.LeadOutput)
	}
}

func TestGlobalTrackerStillCountsEverything(t *testing.T) {
	start := ProcessUsage().Snapshot()
	ctx, _ := WithRequestUsage(context.Background())
	app := &AppCore{}

	app.trackTokens(ctx, &Response{InputTokens: 100, OutputTokens: 10})
	app.trackTokens(context.Background(), &Response{InputTokens: 40, OutputTokens: 4})

	d := ProcessUsage().Diff(start)
	if d.WorkerInput != 140 || d.WorkerOutput != 14 {
		t.Errorf("global worker = in %d / out %d, want 140 / 14 — request scoping must not steal from the global",
			d.WorkerInput, d.WorkerOutput)
	}
}

func TestRequestUsageIsNilOffRequestPaths(t *testing.T) {
	if RequestUsage(context.Background()) != nil {
		t.Error("bare context should carry no request tracker")
	}
	if RequestUsage(nil) != nil {
		t.Error("nil context should be safe and return nil")
	}
}
