// Package categories is the shop's category tree as the owner edits it from
// the managers' chat: sections and their subcategories, and the words that
// file products into them.
//
// The tree lives in the database; data/categories.csv only fills an empty
// one. A product goes where the words send it: every word of every category
// is tried against its name and the supplier's group, and the longest that
// occurs wins, the same way an import files new rows. So every change -- a
// word added or removed, a category created or deleted -- is worked out as
// the import would do it, shown first as a plan ("37 products move, 35 of
// them from Other"), and applied in one transaction when the owner agrees.
package categories

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
)

// Other is where products no word recognises go. It cannot be deleted.
const Other = "other"

// Category is one node of the tree: a section when ParentID is zero, a
// subcategory otherwise. Products sit in subcategories, or in a section
// that has none.
type Category struct {
	ID       int64
	Slug     string
	Title    string
	ParentID int64
	// PartType and Universal pass on to the products filed here: the
	// service kit picks oil filters by part type.
	PartType  string
	Universal bool
	Words     []Word
	// Products counts what is filed here directly.
	Products int
}

// Word files products whose name or group contains it into its category.
type Word struct {
	ID   int64
	Text string
}

// Product is a product as the tree sees it.
type Product struct {
	ID         int64
	Name       string
	Group      string // the supplier's own group, "" when none
	CategoryID int64
	// Pinned is the category the product's group is mapped to, zero when
	// none: a mapped group decides over the words.
	Pinned int64
}

// Move puts a product in a category. CategoryID zero means the category the
// change creates.
type Move struct {
	ProductID  int64
	CategoryID int64
}

// Change is what a plan writes, all in one transaction.
type Change struct {
	Create       *NewCategory
	RenameID     int64
	Title        string
	DeleteID     int64
	AddWordTo    int64
	Word         string
	RemoveWordID int64
	// MoveWordsFrom hands a section's words to the subcategory created in
	// it, with its products.
	MoveWordsFrom int64
	Moves         []Move
}

// NewCategory is a category to create.
type NewCategory struct {
	Slug     string
	Title    string
	ParentID int64
}

// Store reads the tree and writes changes.
type Store interface {
	// Tree lists the categories in menu order, sections before their
	// subcategories, with their words and product counts.
	Tree(ctx context.Context) ([]Category, error)
	// ProductsIn lists every product filed in the category, on sale or not.
	ProductsIn(ctx context.Context, categoryID int64) ([]Product, error)
	// ProductsWith lists every product whose name or group contains word.
	ProductsWith(ctx context.Context, word string) ([]Product, error)
	Apply(ctx context.Context, ch *Change) error
}

// Kind is what a plan does.
type Kind string

// Kinds of plans.
const (
	AddWord    Kind = "add-word"
	RemoveWord Kind = "remove-word"
	Create     Kind = "create"
	Rename     Kind = "rename"
	Delete     Kind = "delete"
)

// Op asks for a plan. It is plain data, kept while the owner decides.
type Op struct {
	Kind       Kind   `json:"kind"`
	CategoryID int64  `json:"category_id,omitempty"` // the category acted on, or the parent of a new one
	WordID     int64  `json:"word_id,omitempty"`
	Word       string `json:"word,omitempty"`
	Title      string `json:"title,omitempty"`
}

// Plan is what an op would do.
type Plan struct {
	Op Op
	// Target is the category acted on: the word's, the renamed, the
	// deleted, the new one. ParentID is its section, zero for a section.
	Target   Path
	ParentID int64
	// Moves are the products that change category; To counts them by
	// where they go, most first, and FromOther how many leave Other.
	Moves     []Product
	To        []Count
	FromOther int
	// Stay are products with an added word that a longer word keeps
	// elsewhere.
	Stay   []Product
	change Change
}

// Path names a category for people: "Гальмівна система › Колодки".
type Path string

// Count is a destination and how many products go there.
type Count struct {
	Path Path
	N    int
}

// Errors the owner reads in the chat.
var (
	ErrBadWord       = errors.New("слово має бути від 3 до 40 символів, без «|» і ком")
	ErrWordTaken     = errors.New("це слово вже веде в іншу категорію")
	ErrBadTitle      = errors.New("назва має бути від 2 до 40 символів")
	ErrTitleTaken    = errors.New("така назва тут уже є")
	ErrNoCategory    = errors.New("такої категорії вже немає")
	ErrHasChildren   = errors.New("у розділі є підкатегорії: спершу видаліть їх або оберіть одну з них")
	ErrKeepOther     = errors.New("«Інше» не можна видалити: туди йдуть товари, які жодне слово не впізнало")
	ErrTooDeep       = errors.New("підкатегорію можна створити лише в розділі")
	ErrNoWord        = errors.New("такого слова вже немає")
	ErrNotForWords   = errors.New("у розділі з підкатегоріями слова не працюють: оберіть підкатегорію")
	errUnknownOpKind = errors.New("categories: unknown op")
)

// Service plans and applies changes to the tree.
type Service struct {
	store Store
}

// New returns a Service.
func New(store Store) *Service { return &Service{store: store} }

// Tree lists the categories in menu order.
func (s *Service) Tree(ctx context.Context) ([]Category, error) { return s.store.Tree(ctx) }

// Plan works out what op would do, changing nothing.
func (s *Service) Plan(ctx context.Context, op Op) (*Plan, error) {
	tree, err := s.store.Tree(ctx)
	if err != nil {
		return nil, err
	}
	t := newTree(tree)
	switch op.Kind {
	case AddWord:
		return s.planAddWord(ctx, t, op)
	case RemoveWord:
		return s.planRemoveWord(ctx, t, op)
	case Create:
		return s.planCreate(ctx, t, op)
	case Rename:
		return t.planRename(op)
	case Delete:
		return s.planDelete(ctx, t, op)
	}
	return nil, errUnknownOpKind
}

// Apply plans op again -- the catalogue may have changed since the owner
// saw the plan -- and writes it.
func (s *Service) Apply(ctx context.Context, op Op) (*Plan, error) {
	p, err := s.Plan(ctx, op)
	if err != nil {
		return nil, err
	}
	if err := s.store.Apply(ctx, &p.change); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) planAddWord(ctx context.Context, t *tree, op Op) (*Plan, error) {
	word, err := NormalizeWord(op.Word)
	if err != nil {
		return nil, err
	}
	target, ok := t.byID[op.CategoryID]
	if !ok {
		return nil, ErrNoCategory
	}
	if t.hasChildren(target.ID) {
		return nil, ErrNotForWords
	}
	if owner, ok := t.wordOwner(word); ok {
		return nil, fmt.Errorf("%w: «%s»", ErrWordTaken, t.path(owner))
	}
	products, err := s.store.ProductsWith(ctx, word)
	if err != nil {
		return nil, err
	}
	after := t.withWord(target.ID, word)
	p := &Plan{Op: Op{Kind: AddWord, CategoryID: target.ID, Word: word}, Target: t.path(target)}
	for _, pr := range products {
		dest := after.classify(pr)
		switch {
		case dest == target.ID && pr.CategoryID != target.ID:
			p.Moves = append(p.Moves, pr)
			p.change.Moves = append(p.change.Moves, Move{ProductID: pr.ID, CategoryID: dest})
		case dest != target.ID && pr.CategoryID != target.ID:
			p.Stay = append(p.Stay, pr)
		}
	}
	p.summarise(t, p.change.Moves)
	p.change.AddWordTo, p.change.Word = target.ID, word
	return p, nil
}

func (s *Service) planRemoveWord(ctx context.Context, t *tree, op Op) (*Plan, error) {
	owner, word, ok := t.word(op.WordID)
	if !ok {
		return nil, ErrNoWord
	}
	products, err := s.store.ProductsWith(ctx, word.Text)
	if err != nil {
		return nil, err
	}
	after := t.withoutWord(word.ID)
	p := &Plan{Op: Op{Kind: RemoveWord, WordID: word.ID, Word: word.Text, CategoryID: owner.ID}, Target: t.path(owner)}
	// Only what sits in the word's category can be there by the word.
	var here []Product
	for _, pr := range products {
		if pr.CategoryID == owner.ID {
			here = append(here, pr)
		}
	}
	p.resort(t, after, here)
	p.change.RemoveWordID = word.ID
	return p, nil
}

func (s *Service) planCreate(ctx context.Context, t *tree, op Op) (*Plan, error) {
	title, err := normalizeTitle(op.Title)
	if err != nil {
		return nil, err
	}
	parent := Category{}
	if op.CategoryID != 0 {
		var ok bool
		if parent, ok = t.byID[op.CategoryID]; !ok {
			return nil, ErrNoCategory
		}
		if parent.ParentID != 0 {
			return nil, ErrTooDeep
		}
	}
	for _, c := range t.cats {
		if c.ParentID == parent.ID && strings.EqualFold(c.Title, title) {
			return nil, ErrTitleTaken
		}
	}
	p := &Plan{Op: Op{Kind: Create, CategoryID: parent.ID, Title: title}, Target: Path(title), ParentID: parent.ID}
	if parent.ID != 0 {
		p.Target = t.path(parent) + " › " + Path(title)
	}
	p.change.Create = &NewCategory{Slug: t.uniqueSlug(title), Title: title, ParentID: parent.ID}
	// A section that had no subcategories held products itself. A section
	// with subcategories shows only them, so its products move into its
	// first one, the one being made.
	if parent.ID != 0 && !t.hasChildren(parent.ID) && parent.Products > 0 {
		products, err := s.store.ProductsIn(ctx, parent.ID)
		if err != nil {
			return nil, err
		}
		for _, pr := range products {
			p.Moves = append(p.Moves, pr)
			p.change.Moves = append(p.change.Moves, Move{ProductID: pr.ID})
		}
		if len(p.Moves) > 0 {
			p.To = []Count{{Path: p.Target, N: len(p.Moves)}}
		}
	}
	if parent.ID != 0 && !t.hasChildren(parent.ID) && len(parent.Words) > 0 {
		// Its words would lead to a section with subcategories, where
		// products cannot sit: they go with the products.
		p.change.MoveWordsFrom = parent.ID
	}
	return p, nil
}

func (t *tree) planRename(op Op) (*Plan, error) {
	c, ok := t.byID[op.CategoryID]
	if !ok {
		return nil, ErrNoCategory
	}
	title, err := normalizeTitle(op.Title)
	if err != nil {
		return nil, err
	}
	for _, x := range t.cats {
		if x.ID != c.ID && x.ParentID == c.ParentID && strings.EqualFold(x.Title, title) {
			return nil, ErrTitleTaken
		}
	}
	p := &Plan{Op: Op{Kind: Rename, CategoryID: c.ID, Title: title}, Target: t.path(c)}
	p.change.RenameID, p.change.Title = c.ID, title
	return p, nil
}

func (s *Service) planDelete(ctx context.Context, t *tree, op Op) (*Plan, error) {
	c, ok := t.byID[op.CategoryID]
	if !ok {
		return nil, ErrNoCategory
	}
	if c.Slug == Other {
		return nil, ErrKeepOther
	}
	if t.hasChildren(c.ID) {
		return nil, ErrHasChildren
	}
	products, err := s.store.ProductsIn(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	p := &Plan{Op: Op{Kind: Delete, CategoryID: c.ID}, Target: t.path(c), ParentID: c.ParentID}
	p.resort(t, t.without(c.ID), products)
	p.change.DeleteID = c.ID
	return p, nil
}

// resort sends products where the tree after the change files them, Other
// when nothing there recognises them, and records the ones that move.
func (p *Plan) resort(before, after *tree, products []Product) {
	other := after.other()
	for _, pr := range products {
		dest := after.classify(pr)
		if dest == 0 {
			dest = other
		}
		if dest == pr.CategoryID {
			continue
		}
		p.Moves = append(p.Moves, pr)
		p.change.Moves = append(p.change.Moves, Move{ProductID: pr.ID, CategoryID: dest})
	}
	p.summarise(before, p.change.Moves)
}

// summarise counts the moves by destination, and those leaving Other.
func (p *Plan) summarise(t *tree, moves []Move) {
	other := t.other()
	to := map[Path]int{}
	for i, m := range moves {
		to[t.path(t.byID[m.CategoryID])]++
		if p.Moves[i].CategoryID == other {
			p.FromOther++
		}
	}
	for path, n := range to {
		p.To = append(p.To, Count{Path: path, N: n})
	}
	sortCounts(p.To)
}

// NormalizeWord lower-cases a word and collapses its spaces, and checks it
// is one the classifier can use: long enough not to match half the
// catalogue ("ка"), and free of the seed file's separators.
func NormalizeWord(s string) (string, error) {
	w := strings.ToLower(strings.Join(strings.Fields(s), " "))
	n := utf8.RuneCountInString(w)
	if n < 3 || n > 40 || strings.ContainsAny(w, "|,") {
		return "", ErrBadWord
	}
	return w, nil
}

func normalizeTitle(s string) (string, error) {
	t := strings.Join(strings.Fields(s), " ")
	if n := utf8.RuneCountInString(t); n < 2 || n > 40 {
		return "", ErrBadTitle
	}
	return t, nil
}

// tree indexes the categories and classifies products against them.
type tree struct {
	cats       []Category
	byID       map[int64]Category
	classifier *importer.Classifier
	bySlug     map[string]int64
}

func newTree(cats []Category) *tree {
	t := &tree{cats: cats, byID: make(map[int64]Category, len(cats)), bySlug: make(map[string]int64, len(cats))}
	for _, c := range cats {
		t.byID[c.ID] = c
		t.bySlug[c.Slug] = c.ID
	}
	// The classifier speaks the importer's categories: slugs and words.
	ic := make([]importer.Category, len(cats))
	for i, c := range cats {
		ic[i] = importer.Category{Slug: c.Slug, Title: c.Title}
		if p, ok := t.byID[c.ParentID]; ok {
			ic[i].Parent = p.Slug
		}
		for _, w := range c.Words {
			ic[i].Keywords = append(ic[i].Keywords, w.Text)
		}
	}
	t.classifier = importer.NewClassifier(ic)
	return t
}

// classify returns the category a product goes to, zero for none.
func (t *tree) classify(p Product) int64 {
	if _, ok := t.byID[p.Pinned]; ok && p.Pinned != 0 {
		return p.Pinned
	}
	if c := t.classifier.Classify(p.Name + " " + p.Group); c != nil {
		return t.bySlug[c.Slug]
	}
	return 0
}

func (t *tree) other() int64 { return t.bySlug[Other] }

func (t *tree) hasChildren(id int64) bool {
	for _, c := range t.cats {
		if c.ParentID == id {
			return true
		}
	}
	return false
}

func (t *tree) path(c Category) Path {
	if p, ok := t.byID[c.ParentID]; ok {
		return Path(p.Title + " › " + c.Title)
	}
	return Path(c.Title)
}

func (t *tree) wordOwner(word string) (Category, bool) {
	for _, c := range t.cats {
		for _, w := range c.Words {
			if w.Text == word {
				return c, true
			}
		}
	}
	return Category{}, false
}

func (t *tree) word(id int64) (Category, Word, bool) {
	for _, c := range t.cats {
		for _, w := range c.Words {
			if w.ID == id {
				return c, w, true
			}
		}
	}
	return Category{}, Word{}, false
}

// withWord, withoutWord and without are the tree after a change.
func (t *tree) withWord(id int64, word string) *tree {
	cats := slices.Clone(t.cats)
	for i := range cats {
		if cats[i].ID == id {
			cats[i].Words = append(slices.Clone(cats[i].Words), Word{Text: word})
		}
	}
	return newTree(cats)
}

func (t *tree) withoutWord(wordID int64) *tree {
	cats := slices.Clone(t.cats)
	for i := range cats {
		cats[i].Words = slices.DeleteFunc(slices.Clone(cats[i].Words), func(w Word) bool { return w.ID == wordID })
	}
	return newTree(cats)
}

func (t *tree) without(id int64) *tree {
	return newTree(slices.DeleteFunc(slices.Clone(t.cats), func(c Category) bool { return c.ID == id }))
}

// uniqueSlug makes a key from a title: "Тюнінг" gives "tiuninh". It is
// only a key; people see the title.
func (t *tree) uniqueSlug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch tr, ok := translit[r]; {
		case ok:
			b.WriteString(tr)
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
	slug := base
	for i := 2; t.bySlug[slug] != 0; i++ {
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

// WordInfo is what a word touches before a category is chosen for it.
type WordInfo struct {
	Word string
	// Products have it in their name or group; Where says which categories
	// hold them now, most first.
	Products int
	Where    []Count
	// Owner is the category the word already leads to, "" when none.
	Owner Path
}

// Word tells the owner what a word touches: whether it is the one they
// mean, where those products are now, and whether the tree has it already.
func (s *Service) Word(ctx context.Context, word string) (*WordInfo, error) {
	word, err := NormalizeWord(word)
	if err != nil {
		return nil, err
	}
	cats, err := s.store.Tree(ctx)
	if err != nil {
		return nil, err
	}
	t := newTree(cats)
	info := &WordInfo{Word: word}
	if c, ok := t.wordOwner(word); ok {
		info.Owner = t.path(c)
	}
	products, err := s.store.ProductsWith(ctx, word)
	if err != nil {
		return nil, err
	}
	info.Products = len(products)
	where := map[Path]int{}
	for _, p := range products {
		where[t.path(t.byID[p.CategoryID])]++
	}
	for path, n := range where {
		info.Where = append(info.Where, Count{Path: path, N: n})
	}
	sortCounts(info.Where)
	return info, nil
}

func sortCounts(cs []Count) {
	slices.SortFunc(cs, func(a, b Count) int {
		if a.N != b.N {
			return b.N - a.N
		}
		return strings.Compare(string(a.Path), string(b.Path))
	})
}
