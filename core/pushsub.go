// Expiring push subscriptions — the shared lifecycle for provider webhooks that
// STOP delivering unless renewed (Microsoft Graph change notifications; Google
// Calendar / Gmail `watch`). The provider-specific part is just three HTTP calls
// (create / renew / delete); the fiddly part — persisting each subscription's
// expiry, renewing the ones nearing it from ONE sweeper, and recovering them
// across a restart — is identical for every provider and lives here.
//
// Mirrors the connector-kind / trigger-engine split: core owns the lifecycle, the
// app supplies what the subscription IS via a registered PushSubHandler. An app
// calls EnsurePushSubscription when it wants a subscription kept alive and
// RemovePushSubscription when it's done; core does the rest.
package core

import (
	"fmt"
	"sync"
	"time"
)

const pushSubTable = "push_subscriptions" // pushSubID(kind,key) → pushSubRecord

// PushSubHandler is the per-kind provider backend. Registered once at startup.
type PushSubHandler interface {
	// Ensure creates the subscription for key, or renews it if it already exists,
	// and returns its new expiry. Called at registration time and by the sweeper
	// before the current expiry lapses. Must be idempotent.
	Ensure(key string) (expiresAt time.Time, err error)
	// Delete tears the subscription down at the provider.
	Delete(key string) error
}

// pushSubRecord is the persisted state the sweeper renews from. Flat + gob-safe.
type pushSubRecord struct {
	Kind          string    `json:"kind"`
	Key           string    `json:"key"`
	ExpiresAt     time.Time `json:"expires_at"`
	RenewBeforeMS int64     `json:"renew_before_ms"` // renew once this much time remains
}

var (
	pushSubMu       sync.RWMutex
	pushSubHandlers = map[string]PushSubHandler{}
	pushSweeperOnce sync.Once
)

// RegisterPushSubHandler installs the backend for a kind. Call once at startup.
func RegisterPushSubHandler(kind string, h PushSubHandler) {
	pushSubMu.Lock()
	pushSubHandlers[kind] = h
	pushSubMu.Unlock()
}

func pushSubHandlerFor(kind string) (PushSubHandler, bool) {
	pushSubMu.RLock()
	defer pushSubMu.RUnlock()
	h, ok := pushSubHandlers[kind]
	return h, ok
}

func pushSubID(kind, key string) string { return kind + "\x00" + key }

// EnsurePushSubscription creates/renews a subscription NOW (synchronously, so a
// failure surfaces to the caller) and registers it for automatic renewal. Calling
// it again for the same (kind,key) just renews — safe on restart. renewBefore is
// how much time-to-expiry triggers a renewal; it's clamped to a sane minimum.
func EnsurePushSubscription(kind, key string, renewBefore time.Duration) error {
	h, ok := pushSubHandlerFor(kind)
	if !ok {
		return fmt.Errorf("no push-sub handler registered for kind %q", kind)
	}
	exp, err := h.Ensure(key)
	if err != nil {
		return err
	}
	if renewBefore < time.Minute {
		renewBefore = time.Minute
	}
	RootDB.Set(pushSubTable, pushSubID(kind, key), pushSubRecord{
		Kind: kind, Key: key, ExpiresAt: exp, RenewBeforeMS: renewBefore.Milliseconds(),
	})
	pushSweeperOnce.Do(startPushSubSweeper)
	return nil
}

// RemovePushSubscription deletes the subscription at the provider and stops
// renewing it. Best-effort on the provider call; the record is always dropped.
func RemovePushSubscription(kind, key string) {
	if h, ok := pushSubHandlerFor(kind); ok {
		if err := h.Delete(key); err != nil {
			Warn("[pushsub] delete %s/%s: %v", kind, key, err)
		}
	}
	RootDB.Unset(pushSubTable, pushSubID(kind, key))
}

// startPushSubSweeper launches the single renewal loop. Started lazily on the
// first EnsurePushSubscription (guarded by pushSweeperOnce).
func startPushSubSweeper() {
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			sweepPushSubs()
		}
	}()
}

// sweepPushSubs renews every persisted subscription that's within its renewBefore
// window. A kind whose handler isn't loaded yet is skipped (picked up next sweep).
func sweepPushSubs() {
	for _, id := range RootDB.Keys(pushSubTable) {
		var r pushSubRecord
		if !RootDB.Get(pushSubTable, id, &r) {
			continue
		}
		if time.Until(r.ExpiresAt) > time.Duration(r.RenewBeforeMS)*time.Millisecond {
			continue // not near expiry yet
		}
		h, ok := pushSubHandlerFor(r.Kind)
		if !ok {
			continue
		}
		exp, err := h.Ensure(r.Key)
		if err != nil {
			Warn("[pushsub] renew %s/%s failed: %v", r.Kind, r.Key, err)
			continue
		}
		r.ExpiresAt = exp
		RootDB.Set(pushSubTable, id, r)
	}
}
