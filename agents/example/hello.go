package example

import (
	"context"
	"fmt"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterAgent(new(HelloAgent)) }

// HelloAgent is an example agent demonstrating the fuzz agent pattern.
type HelloAgent struct {
	input struct {
		name string
	}
	FuzzAgent
}

func (T HelloAgent) Name() string {
	return "hello"
}

func (T HelloAgent) Desc() string {
	return "Example agent that greets a user."
}

// Init parses flags for the hello agent.
func (T *HelloAgent) Init() (err error) {
	T.Flags.StringVar(&T.input.name, "name", "<your name>", "Name to greet.")
	T.Flags.InlineArgs("name")
	T.Flags.Order("name")
	err = T.Flags.Parse()
	if err != nil {
		return err
	}
	return nil
}

// Main runs the hello agent.
func (T *HelloAgent) Main() (err error) {
	greeted := T.Report.Tally("Greeted")

	if T.LLM == nil {
		Log("Hello, %s!", T.input.name)
		greeted.Add(1)
		return
	}

	resp, err := T.WorkerChat(
		context.Background(),
		[]Message{{Role: "user", Content: fmt.Sprintf("Write a short, creative greeting for someone named %s. Keep it to 1-2 sentences.", T.input.name)}},
		WithSystemPrompt("You are a friendly greeter. Respond with just the greeting, nothing else."),
		WithMaxTokens(256),
	)
	if err != nil {
		Err("LLM error: %s", err)
		Log("Hello, %s!", T.input.name)
		greeted.Add(1)
		return nil
	}

	Log(resp.Content)
	greeted.Add(1)

	return
}
