package directory

import (
	"fmt"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/autodb"
)

// FitLines turns the cars a part fits into lines a person reads: one per
// model, its engines and years together. "Chevrolet Lacetti (J200) 1.4
// 16V, 1.6, 1.8 (2005–)" says in one line what the catalogue lists as
// three. Models keep the catalogue's order.
func FitLines(vs []autodb.ArticleVehicle) []string {
	type model struct {
		name     string
		engines  []string
		from, to int
		open     bool // still produced
	}
	var (
		order []*model
		byKey = map[string]*model{}
	)
	for _, v := range vs {
		name := titleCase(v.Make) + " " + titleCase(v.Model)
		m, ok := byKey[name]
		if !ok {
			m = &model{name: name}
			byKey[name] = m
			order = append(order, m)
		}
		if e := strings.TrimSpace(v.Engine); e != "" && !contains(m.engines, e) {
			m.engines = append(m.engines, e)
		}
		from, to := parseInterval(v.Years)
		if from != 0 && (m.from == 0 || from < m.from) {
			m.from = from
		}
		if to == 0 && from != 0 {
			m.open = true
		}
		if to > m.to {
			m.to = to
		}
	}
	out := make([]string, len(order))
	for i, m := range order {
		line := m.name
		if len(m.engines) > 0 {
			line += " " + strings.Join(m.engines, ", ")
		}
		switch {
		case m.from == 0:
		case m.open:
			line += fmt.Sprintf(" (%d–)", m.from)
		default:
			line += fmt.Sprintf(" (%d–%d)", m.from, m.to)
		}
		out[i] = line
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
