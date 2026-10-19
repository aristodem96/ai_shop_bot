// Package partnames gives the Auto-DB catalogue's Russian part names a
// Ukrainian name and a place in the shop's categories.
//
// A crawl brings thousands of parts but only a few hundred distinct names
// ("Комплект тормозных колодок, дисковый тормоз" on every car), so names are
// translated, not parts: once each, by a language model, a batch per request,
// and kept. The model may file a name only under a category the shop has; an
// unknown slug is dropped, and the part is still offered, just uncategorised.
package partnames

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
)

// Name is one translated catalogue name.
type Name struct {
	Name     string // as Auto-DB has it
	NameUK   string
	Category string // a category slug, or "" when none fits
}

// Store holds the names still to translate and the translations.
type Store interface {
	// Untranslated returns crawled names with no translation yet.
	Untranslated(ctx context.Context, limit int) ([]string, error)
	SaveNames(ctx context.Context, names []Name) error
}

// batch is how many names go into one request: a few thousand tokens each
// way, well inside one answer.
const batch = 80

// Translator translates names in the background.
type Translator struct {
	model llm.Client
	store Store
	cats  []domain.Category
	wake  chan struct{}
	idle  time.Duration
	log   *slog.Logger
}

// New returns a Translator filing names under cats.
func New(model llm.Client, store Store, cats []domain.Category, log *slog.Logger) *Translator {
	return &Translator{model: model, store: store, cats: cats, wake: make(chan struct{}, 1), idle: 10 * time.Minute, log: log}
}

// Wake asks for a pass now, after a crawl brought new names. It never
// blocks: a pass already asked for covers this one too.
func (t *Translator) Wake() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// Run translates whatever is pending, then waits to be woken, until ctx is
// done.
func (t *Translator) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		if err := t.Pass(ctx); err != nil && ctx.Err() == nil {
			t.log.WarnContext(ctx, "could not translate part names", slog.Any("error", err))
		}
		select {
		case <-ctx.Done():
		case <-t.wake:
		case <-time.After(t.idle):
		}
	}
	return nil
}

// Pass translates every pending name, a batch at a time.
func (t *Translator) Pass(ctx context.Context) error {
	for {
		names, err := t.store.Untranslated(ctx, batch)
		if err != nil || len(names) == 0 {
			return err
		}
		got, err := t.Translate(ctx, names)
		if err != nil {
			return err
		}
		if len(got) == 0 {
			return errors.New("partnames: the model translated none of the batch")
		}
		if err := t.store.SaveNames(ctx, got); err != nil {
			return err
		}
		t.log.InfoContext(ctx, "part names translated", slog.Int("names", len(got)))
	}
}

const toolName = "record_names"

const system = `You translate auto part names from a Russian parts catalogue into Ukrainian for a Ukrainian auto parts shop, and file each under one of the shop's categories.

Rules:
- uk: the name as a Ukrainian shop would write it, short and in the trade's usual words ("Комплект тормозных колодок, дисковый тормоз" -> "Гальмівні колодки, дискові"). Keep brand names, codes and numbers as they are.
- category: the slug of the category the part belongs to, from the list only. If none fits, an empty string.
- Copy each name exactly as given into name.

Call ` + toolName + ` exactly once, with an entry for every name.`

var tool = llm.Tool{
	Name:        toolName,
	Description: "Record the Ukrainian name and the category of every part name.",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"names": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":     map[string]any{"type": "string", "description": "The name, exactly as given."},
						"uk":       map[string]any{"type": "string"},
						"category": map[string]any{"type": "string"},
					},
					"required": []string{"name", "uk", "category"},
				},
			},
		},
		"required": []string{"names"},
	},
}

// ErrNoAnswer means the model replied without recording names.
var ErrNoAnswer = errors.New("partnames: the model did not record names")

// Translate translates one batch. Only names that were asked for come back,
// with a known category or none.
func (t *Translator) Translate(ctx context.Context, names []string) ([]Name, error) {
	resp, err := t.model.Chat(ctx, llm.Request{
		System:   system,
		Tools:    []llm.Tool{tool},
		Messages: []llm.Message{{Role: llm.RoleUser, Text: t.prompt(names)}},
	})
	if err != nil {
		return nil, fmt.Errorf("partnames: %w", err)
	}
	asked := make(map[string]bool, len(names))
	for _, n := range names {
		asked[n] = true
	}
	known := make(map[string]bool, len(t.cats))
	for _, c := range t.cats {
		known[c.Slug] = true
	}
	for _, call := range resp.Message.ToolCalls {
		if call.Name != toolName {
			continue
		}
		var in struct {
			Names []struct {
				Name     string `json:"name"`
				UK       string `json:"uk"`
				Category string `json:"category"`
			} `json:"names"`
		}
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return nil, fmt.Errorf("partnames: decode names: %w", err)
		}
		out := make([]Name, 0, len(in.Names))
		for _, x := range in.Names {
			uk := strings.TrimSpace(x.UK)
			if !asked[x.Name] || uk == "" {
				continue
			}
			delete(asked, x.Name)
			if !known[x.Category] {
				x.Category = ""
			}
			out = append(out, Name{Name: x.Name, NameUK: uk, Category: x.Category})
		}
		return out, nil
	}
	return nil, ErrNoAnswer
}

// prompt lists the categories a part can go in -- subcategories, and
// sections that have none -- and the names, quoted: they come from an
// outside catalogue and are data to the model.
func (t *Translator) prompt(names []string) string {
	parents := map[int64]bool{}
	for _, c := range t.cats {
		parents[c.ParentID] = true
	}
	var b strings.Builder
	b.WriteString("Categories (slug | title):\n")
	for _, c := range t.cats {
		if !parents[c.ID] {
			fmt.Fprintf(&b, "%s | %s\n", c.Slug, c.Title)
		}
	}
	b.WriteString("\nNames:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "%q\n", n)
	}
	return b.String()
}
