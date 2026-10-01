package bridges

// Wake budget: how often inbound traffic may wake an agent.
//
// The loop guard bounds replies into ONE conversation. It says nothing about a
// sender who writes from many conversations, or a connector whose chat ids the
// sender chooses (a generic webhook), where every message is a new
// conversation and a fresh agent run. Each wake is a model call the owner pays
// for, so a stranger could spend the owner's budget by writing quickly. Past
// these limits a message is still recorded; it just does not wake anything
// until the minute rolls over. The owner is never limited.

import (
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	senderWakesDefault    = 10
	connectorWakesDefault = 60
	wakeWindow            = time.Minute
)

func init() {
	RegisterTunable(TunableSpec{
		App: "/bridges",
		Key: "tune_bridge_sender_wakes_per_min", Category: "Limits",
		Label:   "Wakes per sender per minute",
		Help:    "How many messages from one sender (not the owner) may wake an agent in a minute.",
		Detail:  "Counted across every conversation the sender writes in. Past it, their messages are recorded but wake nothing until the minute rolls over, so a stranger writing quickly cannot spend the owner's model budget.",
		Kind:    KindInt,
		Default: senderWakesDefault, Min: 1, Max: 600,
	})
	RegisterTunable(TunableSpec{
		App: "/bridges",
		Key: "tune_bridge_connector_wakes_per_min", Category: "Limits",
		Label:   "Wakes per connector per minute",
		Help:    "How many messages arriving through one connector (not from the owner) may wake agents in a minute.",
		Detail:  "The backstop for senders the per-sender limit cannot tell apart, such as a webhook whose sender and chat ids are whatever the caller says. Past it, messages are recorded but wake nothing until the minute rolls over.",
		Kind:    KindInt,
		Default: connectorWakesDefault, Min: 1, Max: 6000,
	})
}

func senderWakesFor() int {
	if n := TuneInt("tune_bridge_sender_wakes_per_min"); n > 0 {
		return n
	}
	return senderWakesDefault
}

func connectorWakesFor() int {
	if n := TuneInt("tune_bridge_connector_wakes_per_min"); n > 0 {
		return n
	}
	return connectorWakesDefault
}

// wakeBudget counts recent wakes per key over a sliding window.
type wakeBudget struct {
	mu    sync.Mutex
	wakes map[string][]time.Time
	swept time.Time
}

var inboundWakes = &wakeBudget{wakes: map[string][]time.Time{}}

// take spends one wake from each key's budget, or none when any of them is
// spent, and reports which one was. Spending all or nothing keeps a refused
// message from using up the other budgets.
func (b *wakeBudget) take(now time.Time, keys []string, limits []int) (spent string, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if now.Sub(b.swept) > 10*wakeWindow {
		for k, ts := range b.wakes {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= wakeWindow {
				delete(b.wakes, k)
			}
		}
		b.swept = now
	}
	for i, k := range keys {
		kept := b.wakes[k][:0]
		for _, at := range b.wakes[k] {
			if now.Sub(at) < wakeWindow {
				kept = append(kept, at)
			}
		}
		b.wakes[k] = kept
		if len(kept) >= limits[i] {
			return k, false
		}
	}
	for _, k := range keys {
		b.wakes[k] = append(b.wakes[k], now)
	}
	return "", true
}

// mayWake reports whether a message from someone other than the owner may wake
// an agent now, spending from its sender's and its connector's budgets. A
// sender the transport could not name is counted by conversation instead.
func (T *Bridges) mayWake(key BridgeKey, owner, svc, chatID, handle string, fromOwner bool) (bool, string) {
	if fromOwner {
		return true, ""
	}
	sender := normalizeIdentity(handle)
	if sender == "" {
		sender = "chat:" + chatID
	}
	connector := firstNonEmpty(key.ID, svc)
	who, ok := inboundWakes.take(time.Now(),
		[]string{"sender\x00" + owner + "\x00" + svc + "\x00" + sender, "connector\x00" + owner + "\x00" + connector},
		[]int{senderWakesFor(), connectorWakesFor()})
	if ok {
		return true, ""
	}
	if strings.HasPrefix(who, "sender") {
		return false, "this sender's"
	}
	return false, "this connector's"
}
