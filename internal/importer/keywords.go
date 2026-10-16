package importer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The shop's owner teaches the classifier from the managers' chat: "this
// word means that category", with a new category when none fits. A change
// is previewed -- which products would move -- and only then applied.

// Errors of keyword changes, worded for the owner who reads them in the chat.
var (
	ErrBadKeyword    = errors.New("слово має бути від 3 до 40 символів, без «|» і ком")
	ErrKeywordTaken  = errors.New("це слово вже веде в іншу категорію")
	ErrBadCategory   = errors.New("такої категорії немає")
	ErrBadCategoryNm = errors.New("назва категорії має бути від 2 до 40 символів")
)

// ProductRef is a product as a keyword change sees it.
type ProductRef struct {
	ID       int64
	Name     string
	Category string // slug
}

// NewCategory is a category the owner creates with a keyword. Parent empty
// makes it a section.
type NewCategory struct {
	Slug, Parent, Title string
}

// OwnerKeyword is a word the owner added.
type OwnerKeyword struct {
	ID       int64
	Word     string
	Category string // "Section › Subcategory"
	Slug     string // the category's
}

// Move is a product going to another category.
type Move struct {
	ProductID int64
	Category  string // slug
}

// KeywordStore is what keyword changes need from the database.
type KeywordStore interface {
	Categories(ctx context.Context) ([]Category, error)
	// ProductsContaining lists active products whose name contains word.
	ProductsContaining(ctx context.Context, word string) ([]ProductRef, error)
	// AddKeyword stores word for the category slug, creating it first when
	// create is set, and files the products in move there, in one
	// transaction.
	AddKeyword(ctx context.Context, word, slug string, create *NewCategory, move []int64) error
	OwnerKeywords(ctx context.Context) ([]OwnerKeyword, error)
	// RemoveKeyword deletes the owner's word and applies moves, in one
	// transaction.
	RemoveKeyword(ctx context.Context, id int64, moves []Move) (*OwnerKeyword, error)
}

// KeywordRemoval is what forgetting a word would do: its products go where
// the tree without the word files them, as if it had never been added.
type KeywordRemoval struct {
	Keyword OwnerKeyword
	// Back are the products that move; To counts them by destination,
	// "Section › Subcategory", most first.
	Back []ProductRef
	To   []Count
	// moves pairs each product with its destination, for the store.
	moves []Move
}

// KeywordChange is what adding a word would do.
type KeywordChange struct {
	Word   string
	Target Category
	// Create is set when the target is a new category.
	Create *NewCategory
	// Matching counts products whose name has the word; Move are those the
	// word would file in the target, FromDefault how many of them now sit in
	// the default category. The rest stay: a longer word elsewhere wins.
	Matching    int
	Move        []ProductRef
	FromDefault int
	Stay        []ProductRef
}

// defaultCategory is where unrecognised products land, the move the owner
// is usually after.
const defaultCategory = "other"

// NormalizeKeyword lower-cases a word and collapses its spaces, and checks
// it is one the classifier can use: long enough not to match half the
// catalogue ("ка"), and free of the file's separators.
func NormalizeKeyword(s string) (string, error) {
	w := strings.ToLower(clean(s))
	n := utf8.RuneCountInString(w)
	if n < 3 || n > 40 || strings.ContainsAny(w, "|,") {
		return "", ErrBadKeyword
	}
	return w, nil
}

// KeywordInfo is what a word means now, before a category is chosen.
type KeywordInfo struct {
	Word string
	// Total products have the word in their name; Where says which
	// categories hold them now, "Section › Subcategory", most first, and
	// InDefault how many sit in the default one.
	Total     int
	InDefault int
	Where     []Count
	// TakenBy names the category the word already leads to, if any: then
	// there is nothing to choose.
	TakenBy string
}

// KeywordInfo tells the owner what a word touches before they pick a
// category: whether the word is the one they mean, where those products are
// now, and whether the tree knows the word already.
func (s *Service) KeywordInfo(ctx context.Context, word string) (*KeywordInfo, error) {
	word, err := NormalizeKeyword(word)
	if err != nil {
		return nil, err
	}
	cats, err := s.keywords.Categories(ctx)
	if err != nil {
		return nil, err
	}
	info := &KeywordInfo{Word: word}
	for _, c := range cats {
		if slices.Contains(c.Keywords, word) {
			info.TakenBy = Path(cats, c)
		}
	}
	refs, err := s.keywords.ProductsContaining(ctx, word)
	if err != nil {
		return nil, err
	}
	bySlug := make(map[string]Category, len(cats))
	for _, c := range cats {
		bySlug[c.Slug] = c
	}
	where := make(map[string]int)
	for _, r := range refs {
		where[Path(cats, bySlug[r.Category])]++
		if r.Category == defaultCategory {
			info.InDefault++
		}
	}
	info.Total = len(refs)
	info.Where = sortedCounts(where)
	return info, nil
}

// Path names a category the way the menu shows it: "Двигун › Деталі
// двигуна".
func Path(cats []Category, c Category) string {
	for _, p := range cats {
		if c.Parent != "" && p.Slug == c.Parent {
			return p.Title + " › " + c.Title
		}
	}
	return c.Title
}

// PreviewKeyword works out what filing word under the category slug would
// move. With create set, slug is ignored and the category is new.
func (s *Service) PreviewKeyword(ctx context.Context, word, slug string, create *NewCategory) (*KeywordChange, error) {
	word, err := NormalizeKeyword(word)
	if err != nil {
		return nil, err
	}
	cats, err := s.keywords.Categories(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range cats {
		if slices.Contains(c.Keywords, word) {
			return nil, fmt.Errorf("%w: «%s»", ErrKeywordTaken, Path(cats, c))
		}
	}

	ch := &KeywordChange{Word: word}
	target := -1
	if create != nil {
		create.Title = clean(create.Title)
		if n := utf8.RuneCountInString(create.Title); n < 2 || n > 40 {
			return nil, ErrBadCategoryNm
		}
		create.Slug = uniqueSlug(create.Title, cats)
		ch.Create = create
		cats = append(cats, Category{Slug: create.Slug, Parent: create.Parent, Title: create.Title})
		target = len(cats) - 1
	} else {
		for i := range cats {
			if cats[i].Slug == slug {
				target = i
			}
		}
		if target < 0 {
			return nil, ErrBadCategory
		}
	}
	cats[target].Keywords = append(cats[target].Keywords, word)
	ch.Target = cats[target]

	refs, err := s.keywords.ProductsContaining(ctx, word)
	if err != nil {
		return nil, err
	}
	ch.Matching = len(refs)
	// The whole classifier decides, not "contains the word": a product that
	// a longer word files elsewhere stays there, as it would on import.
	classifier := NewClassifier(cats)
	for _, r := range refs {
		c := classifier.Classify(r.Name)
		switch {
		case c != nil && c.Slug == ch.Target.Slug && r.Category != ch.Target.Slug:
			ch.Move = append(ch.Move, r)
			if r.Category == defaultCategory {
				ch.FromDefault++
			}
		case r.Category != ch.Target.Slug:
			ch.Stay = append(ch.Stay, r)
		}
	}
	return ch, nil
}

// ApplyKeyword stores a previewed change and moves its products. The
// preview is taken again first: products may have changed since it was
// shown, and what moves is what the classifier says now.
func (s *Service) ApplyKeyword(ctx context.Context, word, slug string, create *NewCategory) (*KeywordChange, error) {
	ch, err := s.PreviewKeyword(ctx, word, slug, create)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(ch.Move))
	for i, r := range ch.Move {
		ids[i] = r.ID
	}
	if err := s.keywords.AddKeyword(ctx, ch.Word, ch.Target.Slug, ch.Create, ids); err != nil {
		return nil, err
	}
	return ch, nil
}

// OwnerKeywords lists the words the owner added.
func (s *Service) OwnerKeywords(ctx context.Context) ([]OwnerKeyword, error) {
	return s.keywords.OwnerKeywords(ctx)
}

// PreviewRemoval works out where forgetting a word sends its products. The
// tree without the word decides, the same classifier an import runs, so a
// product lands where it would be had the word never been added -- not
// where it was before, which an import since may have changed. What the
// tree does not recognise goes to the default category.
func (s *Service) PreviewRemoval(ctx context.Context, id int64) (*KeywordRemoval, error) {
	kws, err := s.keywords.OwnerKeywords(ctx)
	if err != nil {
		return nil, err
	}
	var kw *OwnerKeyword
	for i := range kws {
		if kws[i].ID == id {
			kw = &kws[i]
		}
	}
	if kw == nil {
		return nil, ErrBadKeyword
	}
	cats, err := s.keywords.Categories(ctx)
	if err != nil {
		return nil, err
	}
	bySlug := make(map[string]Category, len(cats))
	for i := range cats {
		cats[i].Keywords = slices.DeleteFunc(cats[i].Keywords, func(w string) bool { return w == kw.Word })
		bySlug[cats[i].Slug] = cats[i]
	}

	refs, err := s.keywords.ProductsContaining(ctx, kw.Word)
	if err != nil {
		return nil, err
	}
	rm := &KeywordRemoval{Keyword: *kw}
	classifier := NewClassifier(cats)
	to := make(map[string]int)
	for _, r := range refs {
		// Only what the word filed: a product with the word that sits
		// elsewhere got there by a longer word or a rule, and stays.
		if r.Category != kw.Slug {
			continue
		}
		dest := defaultCategory
		if c := classifier.Classify(r.Name); c != nil {
			dest = c.Slug
		}
		if dest == r.Category {
			continue
		}
		rm.Back = append(rm.Back, r)
		rm.moves = append(rm.moves, Move{ProductID: r.ID, Category: dest})
		to[Path(cats, bySlug[dest])]++
	}
	rm.To = sortedCounts(to)
	return rm, nil
}

// RemoveKeyword forgets a word and sends its products where the tree
// without it files them. The removal is worked out again first: the
// catalogue may have changed since the preview.
func (s *Service) RemoveKeyword(ctx context.Context, id int64) (*KeywordRemoval, error) {
	rm, err := s.PreviewRemoval(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.keywords.RemoveKeyword(ctx, id, rm.moves); err != nil {
		return nil, err
	}
	return rm, nil
}

// Categories lists the tree, for the owner to pick from.
func (s *Service) Categories(ctx context.Context) ([]Category, error) {
	return s.keywords.Categories(ctx)
}

// uniqueSlug makes a slug from a Ukrainian title: "Тюнінг" gives "tiuninh".
// It is only a key; people see the title.
func uniqueSlug(title string, cats []Category) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch t, ok := translit[r]; {
		case ok:
			b.WriteString(t)
			dash = false
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	base := strings.Trim(b.String(), "-")
	if len(base) < 2 || !unicode.IsLetter(rune(base[0])) {
		base = "custom-" + base
	}
	base = strings.TrimSuffix(base[:min(len(base), 32)], "-")
	taken := make(map[string]bool, len(cats))
	for _, c := range cats {
		taken[c.Slug] = true
	}
	slug := base
	for i := 2; taken[slug]; i++ {
		slug = fmt.Sprintf("%s-%d", base, i)
	}
	return slug
}

// translit follows the Ukrainian national transliteration, simplified to
// one form per letter; Russian letters are covered for Russian titles.
var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "h", 'ґ': "g", 'д': "d", 'е': "e", 'є': "ie", 'ж': "zh", 'з': "z",
	'и': "y", 'і': "i", 'ї': "i", 'й': "i", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p",
	'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
	'ь': "", 'ю': "iu", 'я': "ia", '\'': "", 'ʼ': "", 'ы': "y", 'э': "e", 'ё': "e", 'ъ': "",
}
