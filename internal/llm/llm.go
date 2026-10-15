// Package llm is the boundary between the agent and a language model.
//
// The types here are deliberately provider-neutral: the agent loop, the tools
// and the conversation store speak only this vocabulary, and each provider
// package (claude, later gemini and ollama) translates it to its own API. That
// is what lets the provider be a configuration value and the tests run against
// a scripted fake.
package llm

import (
	"context"
	"encoding/json"
	"errors"
)

// Role says who wrote a message.
type Role string

const (
	// RoleUser is the buyer, or tool results going back to the model.
	RoleUser Role = "user"
	// RoleAssistant is the model.
	RoleAssistant Role = "assistant"
)

// Message is one turn of a conversation.
//
// A user turn carries Text, ToolResults or both; an assistant turn carries
// Text, ToolCalls or both.
type Message struct {
	Role        Role         `json:"role"`
	Text        string       `json:"text,omitempty"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	ToolResults []ToolResult `json:"tool_results,omitempty"`

	// Native is the provider's own form of an assistant turn, exactly as it
	// came back. A provider that finds it replays it verbatim instead of
	// rebuilding the turn from the fields above.
	//
	// It matters for Claude: a thinking block is signed together with the
	// conversation before it, and within one tool loop it must go back
	// byte-for-byte. Native is never persisted (json:"-"), so a stored
	// conversation holds no thinking blocks at all -- see agent.Agent for why
	// that is the safe choice.
	Native any `json:"-"`
}

// ToolCall is the model asking for a tool to run.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult is a tool's answer to one call.
type ToolResult struct {
	CallID  string `json:"call_id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

// Tool describes a tool the model may call. Schema is a JSON Schema object.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
}

// Request is one call to the model.
type Request struct {
	// System and Tools must not change within a conversation: providers cache
	// them as a prefix, and Claude binds thinking to them.
	System   string
	Tools    []Tool
	Messages []Message
}

// StopReason says why the model stopped.
type StopReason string

const (
	// StopEnd is a finished answer.
	StopEnd StopReason = "end"
	// StopToolUse means the model is waiting for tool results.
	StopToolUse StopReason = "tool_use"
	// StopMaxTokens means the answer was cut off.
	StopMaxTokens StopReason = "max_tokens"
	// StopRefusal means the provider's safety system declined the request.
	StopRefusal StopReason = "refusal"
)

// Usage is the token accounting of one call, for logs and cost tracking.
type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

// Response is the model's turn.
type Response struct {
	Message    Message
	StopReason StopReason
	Usage      Usage
}

// Client is a language model.
type Client interface {
	Chat(ctx context.Context, req Request) (*Response, error)
}

// ErrUnavailable wraps provider failures worth retrying later: rate limits,
// overload, timeouts. The bot tells the buyer to try again instead of
// apologising for a bug.
var ErrUnavailable = errors.New("llm: provider unavailable")
