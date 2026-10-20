package service

import (
	"context"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// Search limits. The agent asks for what it wants within these; the code
// decides the bounds.
const (
	DefaultSearchLimit = 5
	MaxSearchLimit     = 10
)

// SearchFilter is a structured catalogue query. Zero fields do not filter.
type SearchFilter struct {
	// Query names the kind of part, as word stems, in one or more ways
	// separated by commas -- in Ukrainian and Russian and by synonyms:
	// "гальмівн диск, тормозн диск". A product matches any of the words, the
	// best match first; a catalogue part, all the words of any one way.
	Query string
	// PartNumber matches an OEM number in any spelling ("1K0 698 151").
	PartNumber string
	Category   string // category slug
	// VehicleID keeps only parts that fit the vehicle, universal ones included.
	VehicleID int64
	Brand     string
	MinPrice  domain.Money
	MaxPrice  domain.Money
	Limit     int
}

// SearchResult is what a search found.
type SearchResult struct {
	Products []domain.Product
	// Relaxed is set when nothing matched the keywords, so the search was
	// repeated with the structured filters alone. The agent is told, so it
	// can say "no exact match, but here is what fits otherwise" rather than
	// present loose results as exact ones.
	Relaxed bool
}

// Search runs a catalogue query.
func (s *CatalogService) Search(ctx context.Context, f SearchFilter) (*SearchResult, error) {
	switch {
	case f.Limit <= 0:
		f.Limit = DefaultSearchLimit
	case f.Limit > MaxSearchLimit:
		f.Limit = MaxSearchLimit
	}
	f.PartNumber = domain.NormalizeOEM(f.PartNumber)

	products, err := s.repo.Search(ctx, f)
	if err != nil {
		return nil, err
	}
	// Keywords are the least reliable part of a filter: a buyer's word may
	// simply not appear in any description. The car, the category and the
	// budget are what the buyer actually asked for, so those are kept.
	if len(products) == 0 && f.Query != "" {
		f.Query = ""
		products, err = s.repo.Search(ctx, f)
		if err != nil {
			return nil, err
		}
		return &SearchResult{Products: products, Relaxed: len(products) > 0}, nil
	}
	return &SearchResult{Products: products}, nil
}

// Phrases splits a query into its ways of naming a part, each lowercased,
// its words single-spaced: "Гальмівн диск, тормозн  диск" is "гальмівн
// диск" and "тормозн диск". Commas, semicolons and bars separate ways; a word
// of one letter is dropped.
func Phrases(query string) []string {
	out := []string{}
	for _, ph := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return r == ',' || r == ';' || r == '|' }) {
		var words []string
		for _, w := range strings.Fields(ph) {
			if len([]rune(w)) >= 2 {
				words = append(words, w)
			}
		}
		if len(words) > 0 {
			out = append(out, strings.Join(words, " "))
		}
	}
	return out
}
