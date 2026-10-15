package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

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

// Search finds active products with at least one size in stock that match
// every set field of the filter. Results carry their variants.
func (r *CatalogRepository) Search(ctx context.Context, f service.SearchFilter) ([]domain.Product, error) {
	var w whereBuilder
	w.add("p.is_active")

	if f.Category != "" {
		w.add("p.category_id = (SELECT id FROM categories WHERE slug = " + w.arg(f.Category) + ")")
	}
	if f.MinPrice > 0 {
		w.add("p.price >= " + w.arg(int64(f.MinPrice)))
	}
	if f.MaxPrice > 0 {
		w.add("p.price <= " + w.arg(int64(f.MaxPrice)))
	}
	if len(f.Colors) > 0 {
		w.add("p.color = ANY(" + w.arg(f.Colors) + ")")
	}
	if len(f.ExcludeColors) > 0 {
		w.add("NOT (p.color = ANY(" + w.arg(f.ExcludeColors) + "))")
	}
	// An all-season product suits any season, and a unisex one any gender:
	// "a winter hoodie for him" should find the all-season unisex hoodie.
	if f.Season != "" {
		w.add("p.season IN (" + w.arg(f.Season) + ", 'all')")
	}
	if f.Gender != "" {
		w.add("p.gender IN (" + w.arg(f.Gender) + ", 'unisex')")
	}
	if len(f.Styles) > 0 {
		w.add("p.styles && " + w.arg(f.Styles))
	}
	// With a size asked for, that size must be in stock; otherwise any size.
	if f.Size != "" {
		w.add(`EXISTS (SELECT 1 FROM product_variants v
			WHERE v.product_id = p.id AND v.stock > 0 AND upper(v.size) = upper(` + w.arg(f.Size) + `))`)
	} else {
		w.add("s.stock > 0")
	}

	order := "p.price, p.id"
	if q := tsQuery(f.Query); q != "" {
		ph := w.arg(q)
		w.add("p.search @@ to_tsquery('simple', " + ph + ")")
		order = "ts_rank(p.search, to_tsquery('simple', " + ph + ")) DESC, " + order
	}

	sql := `SELECT ` + productColumns + ` FROM products p` + stockJoin +
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
	if err := r.attachVariants(ctx, products); err != nil {
		return nil, err
	}
	return products, nil
}

// attachVariants loads the sizes of many products in one query rather than
// one query per product.
func (r *CatalogRepository) attachVariants(ctx context.Context, products []domain.Product) error {
	if len(products) == 0 {
		return nil
	}
	ids := make([]int64, len(products))
	index := make(map[int64]int, len(products))
	for i, p := range products {
		ids[i] = p.ID
		index[p.ID] = i
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, product_id, size, stock FROM product_variants
		WHERE product_id = ANY($1)
		ORDER BY product_id, position, id`, ids)
	if err != nil {
		return fmt.Errorf("postgres: list variants: %w", err)
	}
	variants, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Variant, error) {
		var v domain.Variant
		err := row.Scan(&v.ID, &v.ProductID, &v.Size, &v.Stock)
		return v, err
	})
	if err != nil {
		return fmt.Errorf("postgres: scan variants: %w", err)
	}
	for _, v := range variants {
		p := &products[index[v.ProductID]]
		p.Variants = append(p.Variants, v)
	}
	return nil
}

// maxQueryWords bounds the tsquery a single search can build.
const maxQueryWords = 8

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
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		// U+02BC, the Ukrainian apostrophe in "вʼязана", is a letter to
		// Unicode, but Postgres splits words on it like on any apostrophe;
		// splitting the same way keeps the query and the index in step.
		return r == '\u02bc' || (!unicode.IsLetter(r) && !unicode.IsDigit(r))
	}) {
		if len([]rune(word)) < 2 {
			continue
		}
		terms = append(terms, word+":*")
		if len(terms) == maxQueryWords {
			break
		}
	}
	return strings.Join(terms, " | ")
}
