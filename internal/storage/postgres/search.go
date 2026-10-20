package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

// whereBuilder collects conditions with numbered placeholders. Values only
// ever travel as arguments: the filter comes from a language model, which
// means from whatever a buyer typed, so nothing in it may reach the SQL text.
type whereBuilder struct {
	conds []string
	args  []any
}

// arg adds a value and returns its placeholder.
func (w *whereBuilder) arg(v any) string {
	w.args = append(w.args, v)
	return "$" + strconv.Itoa(len(w.args))
}

func (w *whereBuilder) add(cond string) { w.conds = append(w.conds, cond) }

// Search finds active products that can be bought and match every set field
// of the filter.
func (r *CatalogRepository) Search(ctx context.Context, f service.SearchFilter) ([]domain.Product, error) {
	var w whereBuilder
	w.add(listed)
	w.add(available)

	if f.Category != "" {
		// A section's slug finds everything under it.
		ph := w.arg(f.Category)
		w.add("p.category_id IN (SELECT c.id FROM categories c LEFT JOIN categories s ON s.id = c.parent_id" +
			" WHERE c.slug = " + ph + " OR s.slug = " + ph + ")")
	}
	if f.VehicleID != 0 {
		w.add(fitsVehicle(w.arg(f.VehicleID)))
	}
	// A part number is matched against the OEM numbers and the shop's own
	// SKU, both normalised, so "1K0-698-151" and "1k0698151" find the same
	// part.
	if f.PartNumber != "" {
		ph := w.arg(f.PartNumber)
		w.add("(" + ph + " = ANY(p.oem_numbers) OR upper(replace(p.sku, '-', '')) = " + ph + ")")
	}
	if f.Brand != "" {
		w.add("lower(p.brand) = lower(" + w.arg(f.Brand) + ")")
	}
	if f.MinPrice > 0 {
		w.add("p.price >= " + w.arg(int64(f.MinPrice)))
	}
	if f.MaxPrice > 0 {
		w.add("p.price <= " + w.arg(int64(f.MaxPrice)))
	}

	order := "p.price, p.id"
	if q := tsQuery(f.Query); q != "" {
		ph := w.arg(q)
		w.add("p.search @@ to_tsquery('simple', " + ph + ")")
		order = "ts_rank(p.search, to_tsquery('simple', " + ph + ")) DESC, " + order
	}

	sql := `SELECT ` + productColumns + ` FROM products p` +
		` WHERE ` + strings.Join(w.conds, " AND ") +
		` ORDER BY ` + order +
		` LIMIT ` + w.arg(f.Limit)

	rows, err := r.pool.Query(ctx, sql, w.args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: search products: %w", err)
	}
	products, err := collectProducts(rows)
	if err != nil {
		return nil, fmt.Errorf("postgres: scan search results: %w", err)
	}
	return products, nil
}

// maxQueryWords bounds the tsquery a single search can build.
const maxQueryWords = 16

// tsQuery turns free words into a prefix OR-query: "тепл пуховик" becomes
// "тепл:* | пуховик:*". Each word is reduced to letters and digits first, so
// no tsquery operator a buyer types (&, |, !, parentheses) survives into it.
//
// Prefixes stand in for the Ukrainian stemmer Postgres does not have: the
// agent is told to send word stems, and "пухов" then finds "пуховик". OR
// rather than AND, because ranking already puts the products matching more
// words first, and one unknown word should not empty the result.
func tsQuery(text string) string {
	var terms []string
	seen := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		// U+02BC, the Ukrainian apostrophe in "вʼязана", is a letter to
		// Unicode, but Postgres splits words on it like on any apostrophe;
		// splitting the same way keeps the query and the index in step.
		return r == '\u02bc' || (!unicode.IsLetter(r) && !unicode.IsDigit(r))
	}) {
		if len([]rune(word)) < 2 || seen[word] {
			continue
		}
		seen[word] = true
		terms = append(terms, word+":*")
		if len(terms) == maxQueryWords {
			break
		}
	}
	return strings.Join(terms, " | ")
}
