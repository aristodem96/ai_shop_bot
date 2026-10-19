package catmatch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/grouping"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
)

var tree = []grouping.Node{
	{ID: 1, Path: "Гальма"},
	{ID: 2, Path: "Гальма › Колодки", ParentID: 1, Leaf: true},
	{ID: 3, Path: "Інше", Leaf: true},
}

var groups = []grouping.Group{
	{Kind: "group", Key: "Трещітки (храповики)", Products: 12, Examples: []string{`Трещітка 1/2" ALLOID 45 зубів`}},
	{Kind: "name", Key: "Колодки задні 2108 HORT", Products: 1, Examples: []string{"Колодки задні 2108 HORT"}},
}

func TestSuggest(t *testing.T) {
	fake := llm.NewFake(llm.Call("c1", toolName, map[string]any{"answers": []map[string]any{
		{"group": 1, "new_section": "Інструмент", "new_title": "Ручний інструмент", "sure": true, "note": "храповики — інструмент"},
		{"group": 2, "category_id": 2, "sure": true, "note": "колодки"},
		{"group": 9, "category_id": 2, "sure": true, "note": "no such group"},
	}}))
	got, err := New(fake).Suggest(context.Background(), tree, []string{"Інструмент › Ручний інструмент"}, groups)
	require.NoError(t, err)
	assert.Equal(t, []*grouping.Suggestion{
		{NewSection: "Інструмент", NewTitle: "Ручний інструмент", Sure: true, Note: "храповики — інструмент"},
		{CategoryID: 2, Sure: true, Note: "колодки"},
	}, got, "answers by number, an unknown number dropped")

	req := fake.Requests[0]
	assert.Contains(t, req.System, "2 | Гальма › Колодки [leaf]", "the tree with ids, leaves marked, in the cached prefix")
	assert.Contains(t, req.System, "1 | Гальма\n")
	text := req.Messages[0].Text
	assert.Contains(t, text, "already proposed (section › subcategory), reuse them:\nІнструмент › Ручний інструмент", "earlier batches' proposals")
	assert.Contains(t, text, `1 | "Трещітки (храповики)" | 12 | "Трещітка 1/2\" ALLOID 45 зубів"`, "a supplier's text is quoted")
	assert.Contains(t, text, `2 | "name: Колодки задні 2108 HORT"`)
}

func TestSuggestWithoutToolCall(t *testing.T) {
	_, err := New(llm.NewFake(llm.Say("це інструмент"))).Suggest(context.Background(), tree, nil, groups)
	require.ErrorIs(t, err, ErrNoAnswer)
}
