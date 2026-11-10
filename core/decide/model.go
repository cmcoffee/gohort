package decide

import (
	"context"
	"fmt"
	"strconv"
	"strings"

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
// when it is off or its choice cannot answer (For).
func Model() LLM { return For(Chosen()) }

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

// Privacy is whether provider may answer decisions (ok), and whether it is
// private in its own right (own) rather than allowed by the deployment's
// declaration that every model is (Admin > Models), with why. A decision
// sees what gohort's checks see, messages and replies, so a model or
// connection that sends them to a third party is refused unless the
// operator has said everything stays under their control.
func Privacy(provider string) (ok, own bool, why string) {
	switch provider {
	case ProviderJudge:
		own, why = true, "the Judge model, which is local or a peer's"
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
