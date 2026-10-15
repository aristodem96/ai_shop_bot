// Package claude implements llm.Client on the Anthropic Messages API through
// the official Go SDK.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/aristodem96/ai_shop_bot/internal/llm"
)

// DefaultModel is fast and cheap, which is what a shop chat needs: short
// turns, a handful of tool calls, many buyers.
const DefaultModel = "claude-haiku-5-5"

// maxTokens bounds one model turn. Thinking counts towards it, so it is sized
// well above the short replies the prompt asks for; the effort level, not this
// cap, is what keeps turns short.
const maxTokens = 16000

// Config configures the client.
type Config struct {
	APIKey string
	Model  string
	// Effort is low, medium or high. Low suits chat and short tool tasks:
	// the model thinks less and may skip thinking on simple turns.
	Effort string
}

// Client is an llm.Client backed by Claude.
type Client struct {
	api    anthropic.Client
	model  string
	effort anthropic.OutputConfigEffort
}

var _ llm.Client = (*Client)(nil)

// New returns a Client.
func New(cfg Config, opts ...option.RequestOption) *Client {
	opts = append([]option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		// The SDK retries 429, 5xx and connection errors with backoff. Two
		// retries fit inside the agent's deadline; more would only make the
		// buyer wait longer for the same "try again later".
		option.WithMaxRetries(2),
	}, opts...)
	return &Client{
		api:    anthropic.NewClient(opts...),
		model:  cfg.Model,
		effort: anthropic.OutputConfigEffort(cfg.Effort),
	}
}

// Chat implements llm.Client.
func (c *Client) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: maxTokens,
		// The breakpoint on the system block caches tools and system together
		// (tools render first). They are byte-identical on every request, so
		// after the first call each buyer's turn reads them from cache.
		System: []anthropic.TextBlockParam{{
			Text:         req.System,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Tools:    tools(req.Tools),
		Messages: messages(req.Messages),
		// Top-level cache_control moves a second breakpoint to the end of the
		// conversation, so the history of a multi-turn chat is cached too.
		CacheControl: anthropic.NewCacheControlEphemeralParam(),
		Thinking:     anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}},
		OutputConfig: anthropic.OutputConfigParam{Effort: c.effort},
	}

	resp, err := c.api.Messages.New(ctx, params)
	if err != nil {
		return nil, classify(err)
	}
	return response(resp), nil
}

func tools(in []llm.Tool) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(in))
	for _, t := range in {
		schema := anthropic.ToolInputSchemaParam{Properties: t.Schema["properties"]}
		if req, ok := t.Schema["required"].([]string); ok {
			schema.Required = req
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: schema,
		}})
	}
	return out
}

func messages(in []llm.Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(in))
	for _, m := range in {
		// Replaying the native turn keeps its thinking blocks -- signature
		// included -- exactly as the API produced them.
		if native, ok := m.Native.(anthropic.MessageParam); ok {
			out = append(out, native)
			continue
		}

		var blocks []anthropic.ContentBlockParamUnion
		switch m.Role {
		case llm.RoleAssistant:
			if m.Text != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Text))
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, tc.Input, tc.Name))
			}
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		default:
			// Tool results first, then any text: the API wants results to
			// open the turn, and the model treats text after them as the
			// user's own words rather than as tool output.
			for _, r := range m.ToolResults {
				blocks = append(blocks, anthropic.NewToolResultBlock(r.CallID, r.Content, r.IsError))
			}
			if m.Text != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Text))
			}
			out = append(out, anthropic.NewUserMessage(blocks...))
		}
	}
	return out
}

func response(resp *anthropic.Message) *llm.Response {
	msg := llm.Message{Role: llm.RoleAssistant, Native: resp.ToParam()}
	// Content is read by block type, not position: with thinking on, a turn
	// usually opens with a thinking block before any text.
	for _, block := range resp.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			msg.Text += b.Text
		case anthropic.ToolUseBlock:
			msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
				ID:    b.ID,
				Name:  b.Name,
				Input: json.RawMessage(b.JSON.Input.Raw()),
			})
		}
	}

	return &llm.Response{
		Message:    msg,
		StopReason: stopReason(resp.StopReason),
		Usage: llm.Usage{
			InputTokens:      resp.Usage.InputTokens,
			OutputTokens:     resp.Usage.OutputTokens,
			CacheReadTokens:  resp.Usage.CacheReadInputTokens,
			CacheWriteTokens: resp.Usage.CacheCreationInputTokens,
		},
	}
}

func stopReason(r anthropic.StopReason) llm.StopReason {
	switch r {
	case anthropic.StopReasonToolUse:
		return llm.StopToolUse
	case anthropic.StopReasonMaxTokens:
		return llm.StopMaxTokens
	case anthropic.StopReasonRefusal:
		return llm.StopRefusal
	default:
		return llm.StopEnd
	}
}

// classify marks the failures that are about capacity rather than about the
// request. By the time an error gets here the SDK has already retried it.
func classify(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", llm.ErrUnavailable, err)
	}
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == http.StatusTooManyRequests,
			apiErr.StatusCode >= http.StatusInternalServerError:
			return fmt.Errorf("%w: claude: HTTP %d", llm.ErrUnavailable, apiErr.StatusCode)
		}
		return fmt.Errorf("claude: HTTP %d: %w", apiErr.StatusCode, err)
	}
	return fmt.Errorf("claude: %w", err)
}
