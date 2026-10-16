// Package vehiclematch asks a language model to map a supplier's vehicle
// names to the catalogue's models.
//
// It is a proposal engine, not a source of truth: the model sees the
// catalogue's models with their ids and may only answer with those ids, the
// caller drops any it does not know, and the owner confirms every mapping
// before it is stored. What the model adds is reading: "Daewoo SENS" is the
// Sens, and "Chery Tiggo" next to "Амортизатор багажника TIGGO 4" is likely
// more than one model.
package vehiclematch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
)

// Matcher is an importer.VehicleMatcher backed by a language model.
type Matcher struct {
	model llm.Client
}

var _ importer.VehicleMatcher = (*Matcher)(nil)

// New returns a Matcher.
func New(model llm.Client) *Matcher { return &Matcher{model: model} }

const toolName = "record_matches"

const system = `You map vehicle names from a Ukrainian auto parts price list to the models of a shop's vehicle directory.

For each supplier name you get how many price list rows use it and a few product names from those rows. Product names often mention the exact model ("TIGGO 4", "Авео T250", "Lanos 1.5"); use them as evidence.

Rules:
- Use only ids from the directory. If no model fits, return an empty model_ids list. An empty answer is far better than a wrong one: a wrong mapping sells parts that do not fit.
- One supplier name may cover several models (a family such as "Chery Tiggo", or "Haval H3/H5"). List every model the name and the product names support, and no others.
- Prefer models whose production years fit the evidence. Several generations of one model are separate directory entries; include a generation only when the evidence allows it.
- note: one short sentence in Ukrainian for the shop owner, at most 120 characters: why these models, or why none (for example "у довіднику немає такої моделі").

Call ` + toolName + ` exactly once, with an entry for every supplier name.`

var tool = llm.Tool{
	Name:        toolName,
	Description: "Record the proposed directory models for every supplier vehicle name.",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"matches": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":      map[string]any{"type": "string", "description": "The supplier name, exactly as given."},
						"model_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
						"note":      map[string]any{"type": "string"},
					},
					"required": []string{"name", "model_ids", "note"},
				},
			},
		},
		"required": []string{"matches"},
	},
}

// ErrNoAnswer means the model replied without recording matches.
var ErrNoAnswer = errors.New("vehiclematch: the model did not record matches")

// Suggest implements importer.VehicleMatcher in one request.
func (m *Matcher) Suggest(ctx context.Context, unknown []importer.UnknownVehicle, models []importer.Model) ([]importer.VehicleSuggestion, error) {
	if len(unknown) == 0 {
		return nil, nil
	}
	resp, err := m.model.Chat(ctx, llm.Request{
		System:   system,
		Tools:    []llm.Tool{tool},
		Messages: []llm.Message{{Role: llm.RoleUser, Text: prompt(unknown, models)}},
	})
	if err != nil {
		return nil, fmt.Errorf("vehiclematch: %w", err)
	}
	for _, call := range resp.Message.ToolCalls {
		if call.Name != toolName {
			continue
		}
		var in struct {
			Matches []struct {
				Name     string  `json:"name"`
				ModelIDs []int64 `json:"model_ids"`
				Note     string  `json:"note"`
			} `json:"matches"`
		}
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return nil, fmt.Errorf("vehiclematch: decode matches: %w", err)
		}
		out := make([]importer.VehicleSuggestion, len(in.Matches))
		for i, x := range in.Matches {
			out[i] = importer.VehicleSuggestion{Name: x.Name, ModelIDs: x.ModelIDs, Note: truncate(x.Note, 160)}
		}
		return out, nil
	}
	return nil, ErrNoAnswer
}

// prompt lays the directory and the names out as plain lines: ids where the
// model must copy them, nothing it could mistake for an instruction. Product
// names come from a supplier's file, so they are data to the model, quoted.
func prompt(unknown []importer.UnknownVehicle, models []importer.Model) string {
	var b strings.Builder
	b.WriteString("Directory (id | model | production years):\n")
	for _, md := range models {
		fmt.Fprintf(&b, "%d | %s %s | %s\n", md.ID, md.Make, md.Model, years(md))
	}
	b.WriteString("\nSupplier names (name | rows | product names from those rows):\n")
	for _, u := range unknown {
		quoted := make([]string, len(u.Examples))
		for i, e := range u.Examples {
			quoted[i] = fmt.Sprintf("%q", truncate(e, 80))
		}
		fmt.Fprintf(&b, "%q | %d | %s\n", u.Name, u.Rows, strings.Join(quoted, ", "))
	}
	return b.String()
}

func years(m importer.Model) string {
	switch {
	case m.YearFrom == 0:
		return "unknown"
	case m.YearTo == 0:
		return fmt.Sprintf("%d-present", m.YearFrom)
	}
	return fmt.Sprintf("%d-%d", m.YearFrom, m.YearTo)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
