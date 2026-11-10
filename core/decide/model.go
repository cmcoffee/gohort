package decide

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/service"
)

// ConfigTable is where Services > System One (apps/admin) keeps what answers
// gohort's decisions: "provider" is one of the Provider choices, a service
// connector's name, or "" when System One is off.
const ConfigTable = "system_one_config"

// The models System One can be answered by, beside a connection.
const (
	ProviderJudge  = "judge"
	ProviderWorker = "worker"
	ProviderLead   = "lead"
)

// Chosen is what Services > System One has chosen: a provider, a connection,
// or "" (off).
func Chosen() string {
	var p string
	if RootDB != nil {
		RootDB.Get(ConfigTable, "provider", &p)
	}
	return strings.TrimSpace(p)
}

// Model is what answers decisions: Services > System One's choice, or nil
// when it is off, its choice cannot answer (For), or it is resting after a
// failure (Rest).
func Model() LLM {
	if _, _, resting := Resting(); resting {
		return nil
	}
	return For(Chosen())
}

// restFor is how long System One is passed over after a failed answer: a
// server that is down fails every ask, and one that hangs held each check
// for its whole timeout before it went on.
const restFor = 3 * time.Minute

var rest struct {
	sync.Mutex
	until time.Time
	why   string
}

// Rest sets System One aside for restFor after err, an answer that failed
// (an error, or no answer in time), with one warning for the whole rest.
// A stop from the caller is not a failure of System One's.
func Rest(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	rest.Lock()
	already := time.Now().Before(rest.until)
	if !already {
		rest.until, rest.why = time.Now().Add(restFor), err.Error()
	}
	rest.Unlock()
	if !already {
		Warn("[decide] System One failed (%v): checks skip it for %s", err, restFor)
	}
}

// Resting reports whether System One is set aside after a failure, until
// when, and why.
func Resting() (until time.Time, why string, resting bool) {
	rest.Lock()
	defer rest.Unlock()
	return rest.until, rest.why, time.Now().Before(rest.until)
}

// For is what provider answers decisions with, or nil: off, a model that is
// not set, a connection that gives no decisions, or one that may not be used
// because what it sends would leave this deployment (Privacy).
func For(provider string) LLM {
	p := strings.TrimSpace(provider)
	if p == "" {
		return nil
	}
	if ok, _, _ := Privacy(p); !ok {
		return nil
	}
	var m LLM
	switch p {
	case ProviderJudge:
		m = JudgeLLM()
	case ProviderWorker:
		m = SharedWorkerLLM()
	case ProviderLead:
		m = SharedLeadLLM()
	default:
		if c, ok := GetConnector(RootDB, p); ok && service.Provides(c, service.DecideChoice) {
			return connection{name: p}
		}
	}
	return m
}

// Identity is what a choice is: the model or connection behind it, so a
// result kept for one (a status, the judge map) is known to be stale when
// the choice now means something else.
func Identity(choice string) string {
	switch choice {
	case "":
		return ""
	case ProviderJudge:
		live, _, _ := LiveJudgeLLM()
		return "judge:" + live
	case ProviderWorker:
		return "worker:" + tierIdentity(LLMTable)
	case ProviderLead:
		return "lead:" + tierIdentity(LeadLLMTable)
	}
	return "connection:" + choice + "@" + service.Address(choice)
}

// tierIdentity is a model tier's provider, address and model, as stored.
func tierIdentity(table string) string {
	var p, e, m string
	if AuthDB != nil {
		if db := AuthDB(); db != nil {
			db.Get(table, "provider", &p)
			db.Get(table, "endpoint", &e)
			db.Get(table, "model", &m)
		}
	}
	return p + "|" + e + "|" + m
}

// Privacy is whether provider may answer decisions (ok), and whether it is
// private in its own right (own) rather than allowed by the deployment's
// declaration that every model is (Admin > Models), with why. A decision
// sees what gohort's checks see, messages and replies, so a model or
// connection that sends them to a third party is refused unless the
// operator has said everything stays under their control.
func Privacy(provider string) (ok, own bool, why string) {
	switch provider {
	case ProviderJudge:
		own, why = true, "the Offload model, which is local or a peer's"
	case ProviderWorker, ProviderLead:
		tier := "Worker"
		if provider == ProviderLead {
			tier = "Lead"
		}
		_, verdicts := RecommendAllLLMsPrivate()
		for _, v := range verdicts {
			if v.Tier == tier {
				own, why = v.Private, v.Reason
			}
		}
	default:
		// An address that cannot be read is not a local default: it is
		// unknown, and unknown is not private.
		if addr := service.Address(provider); addr == "" {
			own, why = false, "its address could not be read"
		} else {
			own, why = ProviderLooksPrivate("vllm", addr)
		}
	}
	if !own && AllLLMsPrivate() {
		return true, false, why + "; allowed because every model is declared private"
	}
	return own, own, why
}

// connection is a service connector answering decisions (decide.choice): a
// System One server that is not one of gohort's models. It answers nothing
// else.
type connection struct{ name string }

func (c connection) Chat(context.Context, []Message, ...ChatOption) (*Response, error) {
	return nil, fmt.Errorf("%s answers System One decisions only", c.name)
}

func (c connection) ChatStream(context.Context, []Message, StreamHandler, ...ChatOption) (*Response, error) {
	return nil, fmt.Errorf("%s answers System One decisions only", c.name)
}

// choose asks the connection with the options in order (order[k] is the
// option shown at label k) and returns each option's probability, in
// Question.Options order, and the label mass. The options are keyed by where
// they are shown, so their order survives a template that writes them as an
// object.
func (c connection) choose(ctx context.Context, q Question, order []int) ([]float64, float64, error) {
	crit := map[string]any{}
	for k, i := range order {
		crit[fmt.Sprintf("o%02d", k)] = q.Options[i]
	}
	res, err := service.Call(ctx, nil, c.name, service.DecideChoice, map[string]any{
		"instructions": strings.TrimSpace(q.System),
		"state":        strings.TrimSpace(q.State),
		"ask":          strings.TrimSpace(q.Ask),
		"criteria":     crit,
	})
	if err != nil {
		return nil, 0, err
	}
	p, err := service.Probabilities(res)
	if err != nil {
		return nil, 0, err
	}
	probs := make([]float64, len(q.Options))
	for k, i := range order {
		probs[i] = p[fmt.Sprintf("o%02d", k)]
	}
	mass, _ := strconv.ParseFloat(strings.TrimSpace(res["label_mass"].Text), 64)
	return probs, mass, nil
}
