package importer

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Category is one node of the category tree: a section when Parent is
// empty, a subcategory otherwise. The tree is two levels deep, which is what
// a chat menu can show: sections, then their subcategories, then products.
type Category struct {
	Slug, Parent, Title string
	// PartType and Universal pass on to the products sorted here.
	PartType  string
	Universal bool
	// Keywords sort imported rows into the category, see Classifier.
	Keywords []string
}

var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)

// ParseCategories reads the category file. Unlike a price list it is the
// shop's own file and small, so any bad line fails it whole: a typo there
// would misfile thousands of products silently.
func ParseCategories(data []byte) ([]Category, error) {
	rows, err := readCSV(bytes.NewReader(data), ',')
	if err != nil {
		return nil, fmt.Errorf("importer: read categories: %w", err)
	}
	var (
		out      []Category
		errs     []error
		bySlug   = make(map[string]Category)
		children = make(map[string]int)
		header   = true
	)
	for i, row := range rows {
		if isBlank(row) || isComment(row) {
			continue
		}
		if header {
			header = false
			continue
		}
		line := i + 1
		if len(row) != 6 {
			errs = append(errs, fmt.Errorf("line %d: want 6 fields, got %d", line, len(row)))
			continue
		}
		c := Category{
			Slug: clean(row[0]), Parent: clean(row[1]), Title: clean(row[2]),
			PartType: clean(row[3]), Universal: clean(row[4]) == "true",
		}
		for _, k := range strings.Split(row[5], "|") {
			if k = strings.ToLower(clean(k)); k != "" {
				c.Keywords = append(c.Keywords, k)
			}
		}
		switch {
		case !slugPattern.MatchString(c.Slug):
			errs = append(errs, fmt.Errorf("line %d: bad slug %q", line, c.Slug))
			continue
		case bySlug[c.Slug].Slug != "":
			errs = append(errs, fmt.Errorf("line %d: %q repeats", line, c.Slug))
			continue
		case c.Title == "":
			errs = append(errs, fmt.Errorf("line %d: %q has no title", line, c.Slug))
		}
		if c.Parent != "" {
			parent, ok := bySlug[c.Parent]
			switch {
			case !ok:
				errs = append(errs, fmt.Errorf("line %d: parent %q of %q is not listed above it", line, c.Parent, c.Slug))
			case parent.Parent != "":
				errs = append(errs, fmt.Errorf("line %d: %q is a subcategory and cannot have its own", line, c.Parent))
			}
			children[c.Parent]++
		}
		out = append(out, c)
		bySlug[c.Slug] = c
	}
	// A section with subcategories holds no products of its own, so words
	// that would file products into it are a mistake.
	for _, c := range out {
		if children[c.Slug] > 0 && len(c.Keywords) > 0 {
			errs = append(errs, fmt.Errorf("%q has subcategories; give the keywords to them", c.Slug))
		}
	}
	if len(out) == 0 {
		errs = append(errs, errors.New("no categories"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("importer: categories: %w", err)
	}
	return out, nil
}

// Leaves are the categories products can be filed in: subcategories, and
// sections without any.
func Leaves(cats []Category) map[string]bool {
	parents := make(map[string]bool)
	for _, c := range cats {
		if c.Parent != "" {
			parents[c.Parent] = true
		}
	}
	out := make(map[string]bool)
	for _, c := range cats {
		if !parents[c.Slug] {
			out[c.Slug] = true
		}
	}
	return out
}

// Classifier files a product by its name. Every keyword of every category
// is tried and the longest that occurs wins: "фильтр масл" (oil filter)
// beats "масл" (oil), "амортизатор багаж" (a boot strut) beats "амортиз" (a
// shock absorber). Length is a fair proxy for specificity, and it makes the
// result independent of how the file is ordered, except between keywords of
// equal length, where the earlier category wins.
type Classifier struct {
	words []keyword
}

type keyword struct {
	word string
	cat  *Category
}

// NewClassifier indexes the keywords of cats.
func NewClassifier(cats []Category) *Classifier {
	c := &Classifier{}
	for i := range cats {
		cat := &cats[i]
		for _, w := range cat.Keywords {
			c.words = append(c.words, keyword{word: w, cat: cat})
		}
	}
	// Stable: equal lengths keep file order.
	slices.SortStableFunc(c.words, func(a, b keyword) int {
		return cmp.Compare(utf8.RuneCountInString(b.word), utf8.RuneCountInString(a.word))
	})
	return c
}

// Classify returns the category for a product name, or nil.
func (c *Classifier) Classify(text string) *Category {
	text = strings.ToLower(text)
	for _, k := range c.words {
		if strings.Contains(text, k.word) {
			return k.cat
		}
	}
	return nil
}
