package orchestrate

// An agent's own voice is a use of the speech voice (service.SpeechVoiceChoice,
// core/service/offered.go): a name the speech server offers, which the next
// server may not. Registered here so a swap of speech servers lists every
// agent whose voice it no longer offers beside the deployment's own, and one
// mapping changes them all, each saved as an ordinary edit of the agent.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/service"
)

func init() { service.RegisterChoiceUses(service.SpeechVoiceChoice, agentVoiceUses) }

// agentVoiceUses is every agent, of every user, with a voice of its own.
func agentVoiceUses() []service.ChoiceUse {
	base, adb := orchestrateBaseDB, AuthDB()
	if base == nil || adb == nil {
		return nil
	}
	var out []service.ChoiceUse
	for _, u := range AuthListUsers(adb) {
		udb := UserDB(base, u.Username)
		if udb == nil {
			continue
		}
		for _, rec := range listAgents(udb, u.Username) {
			voice := strings.TrimSpace(rec.Voice)
			if voice == "" {
				continue
			}
			id, user := rec.ID, u.Username
			out = append(out, service.ChoiceUse{
				Where: fmt.Sprintf("%s's agent %s", user, chFirst(rec.Name, rec.ID)),
				Value: voice,
				Set: func(to string) error {
					cur, ok := loadAgent(udb, id)
					if !ok {
						return fmt.Errorf("the agent is gone")
					}
					was := cur.Voice
					cur.Voice = to
					_, err := saveAgentAs(udb, cur, fmt.Sprintf("voice %s, which the speech server no longer offers, changed to %s", was, to))
					return err
				},
			})
		}
	}
	return out
}
