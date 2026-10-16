package vehiclematch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
)

var _ importer.VehicleProposer = (*Matcher)(nil)

const proposeTool = "record_vehicles"

const proposeSystem = `You propose entries for a Ukrainian auto parts shop's vehicle directory.

An entry is one make, model and engine: the level at which parts differ. The model name carries the generation the way the trade names it ("Octavia A7", "Golf VII", "308 T7", "Camry XV50"), because generations take different parts.

Rules:
- Propose the engines commonly sold in Ukraine for that model, at most 4. Years are the generation's production years; year_to 0 means still produced.
- Spell makes and models the way the existing directory below does when the make is already there (for example "Volkswagen", not "VW" or "Фольксваген"). Write Latin names, not Cyrillic.
- Use the product names as evidence of which generation and engines the shop's parts are for.
- When the owner corrects a previous proposal, apply the correction and keep the rest.
- If you do not know the vehicle well enough to give its generation and years, return no vehicles and say so in the note. A wrong year range sells parts that do not fit.
- note: one short sentence in Ukrainian for the shop owner, at most 120 characters.

Call ` + proposeTool + ` exactly once.`

var proposeSchema = llm.Tool{
	Name:        proposeTool,
	Description: "Record the proposed directory entries.",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"vehicles": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"make":      map[string]any{"type": "string"},
						"model":     map[string]any{"type": "string", "description": "With the generation, e.g. \"308 T7\"."},
						"year_from": map[string]any{"type": "integer"},
						"year_to":   map[string]any{"type": "integer", "description": "0 while still produced."},
						"engine":    map[string]any{"type": "string", "description": "e.g. \"1.6 HDi\", \"2.0 TSI\", \"EV\"."},
						"fuel":      map[string]any{"type": "string", "enum": []string{"petrol", "diesel", "hybrid", "electric"}},
					},
					"required": []string{"make", "model", "year_from", "year_to", "engine", "fuel"},
				},
			},
			"note": map[string]any{"type": "string"},
		},
		"required": []string{"vehicles", "note"},
	},
}

// ProposeVehicles implements importer.VehicleProposer in one request.
func (m *Matcher) ProposeVehicles(ctx context.Context, req importer.ProposeRequest, models []importer.Model) (*importer.VehicleProposal, error) {
	resp, err := m.model.Chat(ctx, llm.Request{
		System:   proposeSystem,
		Tools:    []llm.Tool{proposeSchema},
		Messages: []llm.Message{{Role: llm.RoleUser, Text: proposePrompt(req, models)}},
	})
	if err != nil {
		return nil, fmt.Errorf("vehiclematch: %w", err)
	}
	for _, call := range resp.Message.ToolCalls {
		if call.Name != proposeTool {
			continue
		}
		var in struct {
			Vehicles []struct {
				Make     string `json:"make"`
				Model    string `json:"model"`
				YearFrom int    `json:"year_from"`
				YearTo   int    `json:"year_to"`
				Engine   string `json:"engine"`
				Fuel     string `json:"fuel"`
			} `json:"vehicles"`
			Note string `json:"note"`
		}
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return nil, fmt.Errorf("vehiclematch: decode vehicles: %w", err)
		}
		p := &importer.VehicleProposal{Note: truncate(in.Note, 160)}
		for _, v := range in.Vehicles {
			p.Vehicles = append(p.Vehicles, importer.Vehicle{
				Make: v.Make, Model: v.Model, YearFrom: v.YearFrom, YearTo: v.YearTo, Engine: v.Engine, Fuel: v.Fuel,
			})
		}
		return p, nil
	}
	return nil, ErrNoAnswer
}

// proposePrompt gives the existing makes and models, so names stay in the
// directory's spelling, then the vehicle and what is known of it. The
// supplier's and the owner's words are quoted as data.
func proposePrompt(req importer.ProposeRequest, models []importer.Model) string {
	var b strings.Builder
	b.WriteString("Existing directory models:\n")
	for _, md := range models {
		fmt.Fprintf(&b, "%s %s | %s\n", md.Make, md.Model, years(md))
	}
	fmt.Fprintf(&b, "\nVehicle to add: %q\n", req.Name)
	if len(req.Examples) > 0 {
		quoted := make([]string, len(req.Examples))
		for i, e := range req.Examples {
			quoted[i] = fmt.Sprintf("%q", truncate(e, 80))
		}
		fmt.Fprintf(&b, "Product names from the price list: %s\n", strings.Join(quoted, ", "))
	}
	if len(req.Previous) > 0 {
		b.WriteString("\nYour previous proposal:\n")
		for _, v := range req.Previous {
			fmt.Fprintf(&b, "%s %s | %d-%d | %s | %s\n", v.Make, v.Model, v.YearFrom, v.YearTo, v.Engine, v.Fuel)
		}
	}
	if req.Correction != "" {
		fmt.Fprintf(&b, "\nThe owner's correction: %q\n", truncate(req.Correction, 300))
	}
	return b.String()
}
