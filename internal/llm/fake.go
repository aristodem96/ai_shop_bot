package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Fake is a scripted Client for tests. Each Chat call takes the next scripted
// step and records the request, so a test can play a whole dialogue -- tool
// call, tool result, answer -- without a network or a bill, and then assert on
// exactly what the model was shown.
type Fake struct {
	mu       sync.Mutex
	steps    []FakeStep
	Requests []Request
}

// FakeStep is one scripted model turn: either a response or an error.
type FakeStep struct {
	Response *Response
	Err      error
}

// NewFake returns a Fake that plays steps in order.
func NewFake(steps ...FakeStep) *Fake {
	return &Fake{steps: steps}
}

// Chat implements Client.
func (f *Fake) Chat(_ context.Context, req Request) (*Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// A copy, because the agent keeps appending to its slice after the call.
	req.Messages = append([]Message(nil), req.Messages...)
	f.Requests = append(f.Requests, req)

	if len(f.steps) == 0 {
		return nil, fmt.Errorf("llm.Fake: unexpected call %d, script exhausted", len(f.Requests))
	}
	step := f.steps[0]
	f.steps = f.steps[1:]
	return step.Response, step.Err
}

// Remaining reports how many scripted steps were not used.
func (f *Fake) Remaining() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.steps)
}

// Say scripts a final text answer.
func Say(text string) FakeStep {
	return FakeStep{Response: &Response{
		Message:    Message{Role: RoleAssistant, Text: text},
		StopReason: StopEnd,
	}}
}

// Call scripts a turn that calls one tool with input marshalled to JSON.
func Call(id, name string, input any) FakeStep {
	raw, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	return FakeStep{Response: &Response{
		Message: Message{
			Role:      RoleAssistant,
			ToolCalls: []ToolCall{{ID: id, Name: name, Input: raw}},
		},
		StopReason: StopToolUse,
	}}
}
