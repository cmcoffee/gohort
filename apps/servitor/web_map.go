package servitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	. "github.com/cmcoffee/gohort/core"
)

// --- Map (full scan) ---

func (T *Servitor) handleMap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		ApplianceID string `json:"appliance_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ApplianceID == "" {
		http.Error(w, "appliance_id required", http.StatusBadRequest)
		return
	}
	if udb == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}
	appliance, ownerUser, _, found := T.resolveAppliance(userID, udb, req.ApplianceID)
	if !found {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}
	if appliance.Type == "workspace" {
		// A workspace owns no system to map — its knowledge IS its members'.
		// Refreshing it means refreshing them, individually.
		http.Error(w, "a workspace has nothing of its own to map — refresh its member appliances instead", http.StatusBadRequest)
		return
	}

	sid := UUIDv4()
	ctx, cancel := context.WithCancel(AppContext())
	probeSessions.Register(sid, "Refreshing "+appliance.Name, cancel).SetOwner(userID)
	sessionAppliances.Store(sid, appliance.ID)
	ch := make(chan bool, 1)
	confirmChans.Store(sid, pendingConfirm{ch: ch, owner: userID, interactive: true})

	if appliance.Type == "command" {
		// Command-type appliances: map the command's CLI structure. The
		// resulting reference doubles as the appliance profile (saveProfile).
		go T.runMapAppSession(ctx, sid, userID, ownerUser, appliance, appliance.Command, ch, udb, true)
	} else {
		// Every Map System press does a FULL re-mapping — fresh
		// reconnaissance regardless of whether a profile already
		// exists. The previous "update and extend" path on re-runs
		// produced incremental drift; users hit Map System when they
		// want a clean re-derivation. Facts (stored separately in
		// the appliance facts table) survive the re-map; only the
		// profile blob is overwritten.
		mapMsg := fmt.Sprintf(
			"Perform a complete reconnaissance and profile of the Linux appliance at %s (connected as %s). Be systematic and thorough. Discover all services, configurations, and log file locations.",
			appliance.Host, appliance.User,
		)
		if appliance.Type == "repo" {
			mapMsg = fmt.Sprintf(
				"Map the codebase in the repository %s. Be systematic and thorough: identify the language and framework, trace the architecture and major subsystems, find the data model and entry points, and record how the parts connect as a code map.",
				repoDisplayTarget(appliance),
			)
		}
		if appliance.Type == "bundle" {
			mapMsg = fmt.Sprintf(
				"Map the uploaded evidence bundle %s. Be systematic and thorough: establish what period it covers, which hosts and services appear in it, which file carries which kind of event, and the shape of any failure it captured. Record what the bundle does NOT contain as carefully as what it does.",
				bundleDisplayTarget(appliance),
			)
		}
		hist := []Message{{Role: "user", Content: mapMsg}}
		// Persist the map as a session so it shows in the left rail and its
		// reconnaissance summary stays reviewable, not just streamed live.
		// The run id IS the session id; runSession appends the transcript on
		// done. Each Map System run is its own rail entry (timestamped).
		saveSession(udb, appliance.ID, chatSession{ID: sid, Name: "Refresh: " + appliance.Name})
		// runSession re-clones + re-ingests repos internally when
		// saveProfile=true (the repo analogue of SSH reconnaissance), so a
		// Refresh already picks up new code without a separate pull here.
		go T.runSession(ctx, sid, userID, ownerUser, appliance, ch, hist, udb, true)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"session_id": sid})
}

func (T *Servitor) handleMapApp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		ApplianceID string `json:"appliance_id"`
		Command     string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ApplianceID == "" || req.Command == "" {
		http.Error(w, "appliance_id and command required", http.StatusBadRequest)
		return
	}
	if udb == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}
	appliance, _, _, found := T.resolveAppliance(userID, udb, req.ApplianceID)
	if !found {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}

	sid := UUIDv4()
	ctx, cancel := context.WithCancel(AppContext())
	probeSessions.Register(sid, "Mapping "+req.Command+" on "+appliance.Name, cancel).SetOwner(userID)
	sessionAppliances.Store(sid, appliance.ID)
	ch := make(chan bool, 1)
	confirmChans.Store(sid, pendingConfirm{ch: ch, owner: userID, interactive: true})

	// saveProfile=false: an SSH appliance's profile is the system
	// reconnaissance — a single CLI's reference must not replace it.
	go T.runMapAppSession(ctx, sid, userID, "", appliance, req.Command, ch, udb, false)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"session_id": sid})
}
