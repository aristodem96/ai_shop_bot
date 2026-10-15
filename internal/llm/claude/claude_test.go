package claude

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/llm"
)

// A response the way the API sends it with thinking on: a thinking block with
// an empty text and a signature, then text, then a tool call.
const toolUseResponse = `{
	"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-haiku-5-5",
	"content": [
		{"type": "thinking", "thinking": "", "signature": "sig-abc"},
		{"type": "text", "text": "Шукаю куртки."},
		{"type": "tool_use", "id": "tu_1", "name": "search_products", "input": {"category": "jackets"}}
	],
	"stop_reason": "tool_use", "stop_sequence": null,
	"usage": {"input_tokens": 100, "output_tokens": 20, "cache_read_input_tokens": 80, "cache_creation_input_tokens": 0}
}`

// server answers every request with the given status and body, and keeps the
// decoded request bodies.
func server(t *testing.T, status int, body string) (*Client, *[]map[string]any) {
	t.Helper()
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		got = append(got, req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	c := New(Config{APIKey: "test", Model: DefaultModel, Effort: "low"},
		option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	return c, &got
}

var searchTool = llm.Tool{
	Name:        "search_products",
	Description: "Search the catalogue",
	Schema: map[string]any{
		"type":       "object",
		"properties": map[string]any{"category": map[string]any{"type": "string"}},
		"required":   []string{"category"},
	},
}

func TestChatBuildsACacheableRequest(t *testing.T) {
	c, got := server(t, http.StatusOK, toolUseResponse)

	_, err := c.Chat(context.Background(), llm.Request{
		System:   "You are a shop assistant.",
		Tools:    []llm.Tool{searchTool},
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "тепла куртка"}},
	})
	require.NoError(t, err)
	require.Len(t, *got, 1)
	req := (*got)[0]

	assert.Equal(t, DefaultModel, req["model"])
	system := req["system"].([]any)[0].(map[string]any)
	assert.Equal(t, map[string]any{"type": "ephemeral"}, system["cache_control"],
		"the system block carries the breakpoint that caches tools and system")
	assert.Equal(t, map[string]any{"type": "ephemeral"}, req["cache_control"])
	assert.Equal(t, "adaptive", req["thinking"].(map[string]any)["type"])
	assert.Equal(t, "low", req["output_config"].(map[string]any)["effort"])
	assert.NotContains(t, req, "temperature", "Haiku 5.5 rejects non-default sampling")

	tool := req["tools"].([]any)[0].(map[string]any)
	assert.Equal(t, "search_products", tool["name"])
	assert.Equal(t, []any{"category"}, tool["input_schema"].(map[string]any)["required"])
}

func TestChatParsesBlocksByType(t *testing.T) {
	c, _ := server(t, http.StatusOK, toolUseResponse)

	resp, err := c.Chat(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "тепла куртка"}},
	})
	require.NoError(t, err)

	assert.Equal(t, llm.StopToolUse, resp.StopReason)
	assert.Equal(t, "Шукаю куртки.", resp.Message.Text, "the thinking block before it is skipped")
	require.Len(t, resp.Message.ToolCalls, 1)
	assert.Equal(t, "tu_1", resp.Message.ToolCalls[0].ID)
	assert.JSONEq(t, `{"category":"jackets"}`, string(resp.Message.ToolCalls[0].Input))
	assert.Equal(t, int64(80), resp.Usage.CacheReadTokens)
}

func TestNativeTurnIsReplayedWithItsThinkingBlock(t *testing.T) {
	c, got := server(t, http.StatusOK, toolUseResponse)
	ctx := context.Background()

	first, err := c.Chat(ctx, llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "куртка"}}})
	require.NoError(t, err)

	_, err = c.Chat(ctx, llm.Request{Messages: []llm.Message{
		{Role: llm.RoleUser, Text: "куртка"},
		first.Message,
		{Role: llm.RoleUser, ToolResults: []llm.ToolResult{{CallID: "tu_1", Content: "[]"}}},
	}})
	require.NoError(t, err)

	msgs := (*got)[1]["messages"].([]any)
	assistant := msgs[1].(map[string]any)["content"].([]any)
	thinking := assistant[0].(map[string]any)
	assert.Equal(t, "thinking", thinking["type"])
	assert.Equal(t, "sig-abc", thinking["signature"], "the signature goes back untouched")

	result := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	assert.Equal(t, "tool_result", result["type"])
	assert.Equal(t, "tu_1", result["tool_use_id"])
}

func TestRateLimitIsUnavailable(t *testing.T) {
	c, _ := server(t, http.StatusTooManyRequests,
		`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)

	_, err := c.Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}})
	require.ErrorIs(t, err, llm.ErrUnavailable)
}

func TestBadRequestIsNotUnavailable(t *testing.T) {
	c, _ := server(t, http.StatusBadRequest,
		`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)

	_, err := c.Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}})
	require.Error(t, err)
	assert.NotErrorIs(t, err, llm.ErrUnavailable, "a bug in our request must not read as an outage")
}

func TestEmptyMessagesAreNotSent(t *testing.T) {
	c, got := server(t, http.StatusOK, toolUseResponse)

	_, err := c.Chat(context.Background(), llm.Request{Messages: []llm.Message{
		{Role: llm.RoleUser, Text: "куртка"},
		{Role: llm.RoleAssistant},
		{Role: llm.RoleUser, Text: "як справи"},
	}})
	require.NoError(t, err)
	assert.Len(t, (*got)[0]["messages"], 2, "an empty message would be a 400 from the API")
}
