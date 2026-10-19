// Package grouping files a supplier's goods into the shop's categories group
// by group, a language model proposing and the owner deciding what the
// model is unsure of.
//
// A 1C export files every product in one of the shop's own groups
// ("Трещітки (храповики)", "Фарба металік 1л"), and the group says what a
// product is better than words in its name: a ratchet's name has "1/2"" and
// "45 зубів", nothing a parts tree knows. So each group is mapped to a
// category once. The model gets the tree and the groups with a few product
// names each, and answers per group with a category of the tree, or with a
// new one the tree lacks, and whether it is sure:
//
//   - sure, an existing category: applied at once;
//   - a new category: proposed to the owner, all of them in one message,
//     since the tree is the shop's window and changes only when they agree;
//   - unsure: asked group by group, the model's guess shown.
//
// The model may only name categories of the list it was given; anything
// else is dropped. Mapped groups apply to every later file without asking.
package grouping

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/categories"
)

// Group is a supplier's group: its own group of products, or in a file
// without groups a product name no word of the tree recognised.
type Group struct {
	ID         int64
	SupplierID int64
	Kind       string // "group" or "name"
	Key        string
	Products   int
	Examples   []string
	// CategoryID is where its products go, zero while undecided.
	CategoryID int64
	DecidedBy  string // "model" or "owner"
	// Mixed groups hold products of different kinds: no one category is
	// theirs, and their products are sorted by name one by one.
	Mixed      bool
	Suggestion *Suggestion
}

// Suggestion is where the model would file a group: an existing category,
// a new subcategory of an existing section, a new section with a
// subcategory, or a new section alone.
type Suggestion struct {
	CategoryID int64  `json:"category_id,omitempty"`
	ParentID   int64  `json:"parent_id,omitempty"`
	NewSection string `json:"new_section,omitempty"`
	NewTitle   string `json:"new_title,omitempty"`
	// Mixed: the products are of different kinds, sorted by name instead.
	Mixed bool   `json:"mixed,omitempty"`
	Sure  bool   `json:"sure"`
	Note  string `json:"note,omitempty"`
}

// hasTarget reports whether the suggestion names somewhere to file: a
// dropped one keeps only its note.
func (s *Suggestion) hasTarget() bool { return s != nil && (s.CategoryID != 0 || s.isNew() || s.Mixed) }

// isNew reports whether the suggestion is a category the tree lacks.
func (s *Suggestion) isNew() bool {
	return s != nil && s.CategoryID == 0 && (s.NewSection != "" || s.NewTitle != "")
}

// Node is a category as the model sees it.
type Node struct {
	ID       int64
	Path     string // "Інструмент › Ручний інструмент"
	ParentID int64
	// Leaf: products can be filed here -- a subcategory, or a section
	// with none.
	Leaf bool
}

// Store keeps the groups.
type Store interface {
	// Open lists the supplier's undecided groups, most products first.
	Open(ctx context.Context, supplierID int64) ([]Group, error)
	Get(ctx context.Context, id int64) (*Group, error)
	SaveSuggestion(ctx context.Context, id int64, s *Suggestion) error
	// Decide maps a group and files the supplier's products of it there,
	// in one transaction.
	Decide(ctx context.Context, id, categoryID int64, by string) error
	// DecideMixed marks a group as of mixed products, sorted by name.
	DecideMixed(ctx context.Context, id int64, by string) error
	// Find lists groups, decided or not, whose key contains text.
	Find(ctx context.Context, text string, limit int) ([]Group, error)
}

// Categories is the tree the groups are filed into.
type Categories interface {
	Tree(ctx context.Context) ([]categories.Category, error)
	Apply(ctx context.Context, op categories.Op) (*categories.Plan, error)
}

// Matcher proposes categories for groups: one suggestion per group, in
// order, nil where it has none. proposed are the new categories proposed
// for earlier batches of the same pass, to be reused rather than named
// again otherwise.
type Matcher interface {
	Suggest(ctx context.Context, tree []Node, proposed []string, groups []Group) ([]*Suggestion, error)
}

// batch is how many groups go into one request: their names, five products
// each, and the answers stay well inside one response.
const batch = 80

// Service files groups.
type Service struct {
	store   Store
	cats    Categories
	matcher Matcher
}

// New returns a Service. A nil matcher leaves every group to the owner.
func New(store Store, cats Categories, matcher Matcher) *Service {
	return &Service{store: store, cats: cats, matcher: matcher}
}

// CanSuggest reports whether a model files groups.
func (s *Service) CanSuggest() bool { return s.matcher != nil }

// Proposal is a new category the model proposes, with the groups and
// products it would hold.
type Proposal struct {
	Path     string
	Groups   int
	Products int
}

// Summary is what a sorting pass did and what it leaves to the owner.
type Summary struct {
	// Filed are the groups the model was sure of, now mapped.
	Filed, FiledProducts int
	// Mixed are groups of different kinds of products, sorted by name.
	Mixed, MixedProducts int
	// Proposals are new categories waiting for the owner's yes.
	Proposals []Proposal
	// Doubts are groups the model was unsure of, or had nothing for.
	Doubts, DoubtProducts int
}

// Open reports whether the supplier has groups to decide.
func (s *Service) Open(ctx context.Context, supplierID int64) (int, error) {
	open, err := s.store.Open(ctx, supplierID)
	return len(open), err
}

// Sort asks the model about the supplier's undecided groups it has not
// been asked about, files the ones it is sure of, and sums up the rest.
func (s *Service) Sort(ctx context.Context, supplierID int64) (*Summary, error) {
	open, err := s.store.Open(ctx, supplierID)
	if err != nil {
		return nil, err
	}
	tree, err := s.nodes(ctx)
	if err != nil {
		return nil, err
	}
	var ask []Group
	for _, g := range open {
		if g.Suggestion == nil {
			ask = append(ask, g)
		}
	}
	if s.matcher != nil {
		var proposed []string
		for start := 0; start < len(ask); start += batch {
			part := ask[start:min(start+batch, len(ask))]
			got, err := s.matcher.Suggest(ctx, tree, proposed, part)
			if err != nil {
				return nil, fmt.Errorf("grouping: %w", err)
			}
			for i, g := range part {
				var sug *Suggestion
				if i < len(got) {
					sug = valid(got[i], tree)
				}
				if sug == nil {
					sug = &Suggestion{Note: "немає пропозиції"}
				}
				if err := s.store.SaveSuggestion(ctx, g.ID, sug); err != nil {
					return nil, err
				}
				switch {
				case sug.Sure && sug.Mixed:
					if err := s.store.DecideMixed(ctx, g.ID, "model"); err != nil {
						return nil, err
					}
				case sug.Sure && sug.CategoryID != 0:
					if err := s.store.Decide(ctx, g.ID, sug.CategoryID, "model"); err != nil {
						return nil, err
					}
				case sug.isNew():
					if p := newPath(sug, tree); !slices.Contains(proposed, p) {
						proposed = append(proposed, p)
					}
				}
			}
		}
	}
	return s.summary(ctx, supplierID, open)
}

// summary counts what is filed and what waits, after a pass over open.
func (s *Service) summary(ctx context.Context, supplierID int64, before []Group) (*Summary, error) {
	open, err := s.store.Open(ctx, supplierID)
	if err != nil {
		return nil, err
	}
	sum := &Summary{}
	still := map[int64]bool{}
	for _, g := range open {
		still[g.ID] = true
	}
	for _, g := range before {
		if still[g.ID] {
			continue
		}
		if now, err := s.store.Get(ctx, g.ID); err == nil && now.Mixed {
			sum.Mixed++
			sum.MixedProducts += g.Products
			continue
		}
		sum.Filed++
		sum.FiledProducts += g.Products
	}
	tree, err := s.nodes(ctx)
	if err != nil {
		return nil, err
	}
	props := map[string]*Proposal{}
	var order []string
	for _, g := range open {
		if g.Suggestion.isNew() && g.Suggestion.Sure {
			path := newPath(g.Suggestion, tree)
			p, ok := props[path]
			if !ok {
				p = &Proposal{Path: path}
				props[path] = p
				order = append(order, path)
			}
			p.Groups++
			p.Products += g.Products
			continue
		}
		sum.Doubts++
		sum.DoubtProducts += g.Products
	}
	for _, path := range order {
		sum.Proposals = append(sum.Proposals, *props[path])
	}
	slices.SortStableFunc(sum.Proposals, func(a, b Proposal) int { return b.Products - a.Products })
	return sum, nil
}

// Summary sums up the supplier's undecided groups without asking anyone.
func (s *Service) Summary(ctx context.Context, supplierID int64) (*Summary, error) {
	return s.summary(ctx, supplierID, nil)
}

// CreateProposed creates the new categories the model proposed and was sure
// of, and files their groups there: the owner's yes to the proposals.
func (s *Service) CreateProposed(ctx context.Context, supplierID int64) (created, filed int, err error) {
	open, err := s.store.Open(ctx, supplierID)
	if err != nil {
		return 0, 0, err
	}
	for _, g := range open {
		if !g.Suggestion.isNew() || !g.Suggestion.Sure {
			continue
		}
		id, made, err := s.ensure(ctx, g.Suggestion)
		if err != nil {
			return created, filed, err
		}
		created += made
		if err := s.store.Decide(ctx, g.ID, id, "owner"); err != nil {
			return created, filed, err
		}
		filed++
	}
	return created, filed, nil
}

// ensure finds or makes the category a suggestion names, returning its id
// and how many categories it made.
func (s *Service) ensure(ctx context.Context, sug *Suggestion) (int64, int, error) {
	made := 0
	parent := sug.ParentID
	if sug.NewSection != "" {
		id, err := s.findOrCreate(ctx, 0, sug.NewSection, &made)
		if err != nil {
			return 0, made, err
		}
		if sug.NewTitle == "" {
			return id, made, nil
		}
		parent = id
	}
	id, err := s.findOrCreate(ctx, parent, sug.NewTitle, &made)
	return id, made, err
}

func (s *Service) findOrCreate(ctx context.Context, parent int64, title string, made *int) (int64, error) {
	find := func() (int64, error) {
		tree, err := s.cats.Tree(ctx)
		if err != nil {
			return 0, err
		}
		for _, c := range tree {
			if c.ParentID == parent && strings.EqualFold(c.Title, strings.TrimSpace(title)) {
				return c.ID, nil
			}
		}
		return 0, nil
	}
	if id, err := find(); err != nil || id != 0 {
		return id, err
	}
	if _, err := s.cats.Apply(ctx, categories.Op{Kind: categories.Create, CategoryID: parent, Title: title}); err != nil {
		return 0, fmt.Errorf("grouping: create %q: %w", title, err)
	}
	*made++
	id, err := find()
	if err == nil && id == 0 {
		err = errors.New("grouping: a created category is not in the tree")
	}
	return id, err
}

// Doubts lists the supplier's groups waiting for the owner one by one:
// those the model was unsure of or had nothing for.
func (s *Service) Doubts(ctx context.Context, supplierID int64) ([]Group, error) {
	open, err := s.store.Open(ctx, supplierID)
	if err != nil {
		return nil, err
	}
	out := open[:0]
	for _, g := range open {
		if !g.Suggestion.isNew() || !g.Suggestion.Sure {
			out = append(out, g)
		}
	}
	return out, nil
}

// Decide files a group where the owner says. A category the model proposed
// and the owner accepts is made first: categoryID zero takes the
// suggestion.
func (s *Service) Decide(ctx context.Context, groupID, categoryID int64) (*Group, error) {
	g, err := s.store.Get(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if categoryID == 0 {
		if !g.Suggestion.hasTarget() {
			return nil, ErrNoSuggestion
		}
		if g.Suggestion.Mixed {
			return s.SortByName(ctx, groupID)
		}
		if g.Suggestion.CategoryID != 0 {
			categoryID = g.Suggestion.CategoryID
		} else if categoryID, _, err = s.ensure(ctx, g.Suggestion); err != nil {
			return nil, err
		}
	}
	if err := s.store.Decide(ctx, g.ID, categoryID, "owner"); err != nil {
		return nil, err
	}
	return s.store.Get(ctx, groupID)
}

// SortByName marks a group as of mixed products: the owner's "different
// things, sort them by name".
func (s *Service) SortByName(ctx context.Context, groupID int64) (*Group, error) {
	if err := s.store.DecideMixed(ctx, groupID, "owner"); err != nil {
		return nil, err
	}
	return s.store.Get(ctx, groupID)
}

// ErrNoSuggestion means a group has no proposal to accept.
var ErrNoSuggestion = errors.New("grouping: the group has no suggestion")

// Get returns a group.
func (s *Service) Get(ctx context.Context, id int64) (*Group, error) { return s.store.Get(ctx, id) }

// Find lists groups whose key contains text.
func (s *Service) Find(ctx context.Context, text string) ([]Group, error) {
	return s.store.Find(ctx, strings.TrimSpace(text), 20)
}

// SuggestionPath names where a suggestion files a group, for the owner.
func (s *Service) SuggestionPath(ctx context.Context, sug *Suggestion) (string, error) {
	if !sug.hasTarget() {
		return "", nil
	}
	if sug.Mixed {
		return "різні товари — за назвами", nil
	}
	tree, err := s.nodes(ctx)
	if err != nil {
		return "", err
	}
	if sug.CategoryID != 0 {
		for _, n := range tree {
			if n.ID == sug.CategoryID {
				return n.Path, nil
			}
		}
		return "", nil
	}
	return newPath(sug, tree) + " (нова)", nil
}

// nodes is the tree as the model sees it.
func (s *Service) nodes(ctx context.Context) ([]Node, error) {
	tree, err := s.cats.Tree(ctx)
	if err != nil {
		return nil, err
	}
	hasChildren := map[int64]bool{}
	byID := map[int64]categories.Category{}
	for _, c := range tree {
		hasChildren[c.ParentID] = true
		byID[c.ID] = c
	}
	out := make([]Node, len(tree))
	for i, c := range tree {
		path := c.Title
		if p, ok := byID[c.ParentID]; ok {
			path = p.Title + " › " + c.Title
		}
		out[i] = Node{ID: c.ID, Path: path, ParentID: c.ParentID, Leaf: !hasChildren[c.ID]}
	}
	return out, nil
}

// valid keeps a suggestion that names what the tree has: a leaf to file
// in, or a section to add a subcategory to. Anything else is the model's
// invention and is dropped.
func valid(sug *Suggestion, tree []Node) *Suggestion {
	if sug == nil {
		return nil
	}
	if sug.Mixed {
		return &Suggestion{Mixed: true, Sure: sug.Sure, Note: sug.Note}
	}
	sug.NewSection, sug.NewTitle = strings.TrimSpace(sug.NewSection), strings.TrimSpace(sug.NewTitle)
	byID := map[int64]Node{}
	for _, n := range tree {
		byID[n.ID] = n
	}
	switch {
	case sug.CategoryID != 0:
		if n, ok := byID[sug.CategoryID]; !ok || !n.Leaf {
			return nil
		}
		sug.ParentID, sug.NewSection, sug.NewTitle = 0, "", ""
	case sug.ParentID != 0:
		if n, ok := byID[sug.ParentID]; !ok || n.ParentID != 0 || sug.NewTitle == "" {
			return nil
		}
		sug.NewSection = ""
	case sug.NewSection == "" && sug.NewTitle == "":
		return nil
	case sug.NewSection == "":
		// A subcategory with no section: the title is the new section.
		sug.NewSection, sug.NewTitle = sug.NewTitle, ""
	}
	for _, t := range []string{sug.NewSection, sug.NewTitle} {
		if len([]rune(t)) > 40 {
			return nil
		}
	}
	return sug
}

// newPath names a new category a suggestion makes: "Інструмент › Головки".
func newPath(sug *Suggestion, tree []Node) string {
	section := sug.NewSection
	if sug.ParentID != 0 {
		for _, n := range tree {
			if n.ID == sug.ParentID {
				section = n.Path
			}
		}
	}
	if sug.NewTitle == "" {
		return section
	}
	return section + " › " + sug.NewTitle
}
