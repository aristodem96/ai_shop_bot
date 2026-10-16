package importer

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// A supplier names vehicles its own way, and the import report lists the
// names no model of the catalogue matches. The owner maps them in the chat,
// one by one, a language model suggesting: it reads "Chery Tiggo" next to
// product names like "Амортизатор багажника TIGGO 4" and proposes models
// from the catalogue's list. The model only ever proposes; nothing is stored
// before the owner confirms, and it can propose only ids that exist.

// UnknownVehicle is a vehicle name from a price list that matched nothing.
type UnknownVehicle struct {
	Name     string
	Rows     int
	Examples []string // product names from its rows
}

// VehicleSuggestion is a proposed mapping for one name.
type VehicleSuggestion struct {
	Name     string
	ModelIDs []int64
	// Note is the model's one-line reason, for the owner: why these, or why
	// none.
	Note string
}

// VehicleMatcher proposes models for unknown vehicle names.
type VehicleMatcher interface {
	Suggest(ctx context.Context, unknown []UnknownVehicle, models []Model) ([]VehicleSuggestion, error)
}

// VehicleAlias is a confirmed mapping, as the owner lists them.
type VehicleAlias struct {
	ID     int64
	Source string
	Name   string
	Model  string // the model's label
	// Fitments counts what removing it took away.
	Fitments int
}

// AliasStore keeps confirmed mappings.
type AliasStore interface {
	VehicleModels(ctx context.Context) ([]Model, error)
	AddVehicleAlias(ctx context.Context, source, name, key string, modelIDs []int64) error
	ListVehicleAliases(ctx context.Context) ([]VehicleAlias, error)
	RemoveVehicleAlias(ctx context.Context, id int64) (*VehicleAlias, error)
}

// Errors of vehicle matching.
var (
	// ErrNoMatcher means no model is configured to suggest.
	ErrNoMatcher = errors.New("importer: no vehicle matcher")
	// ErrNoAlias means the mapping is gone: removed already.
	ErrNoAlias = errors.New("importer: no such vehicle alias")
)

// maxSuggest bounds the names sent to the model in one go. The report sorts
// them by rows, so these are the ones that matter most.
const maxSuggest = 40

// VehicleModels lists the catalogue's models, for picking by hand.
func (s *Service) VehicleModels(ctx context.Context) ([]Model, error) {
	return s.aliases.VehicleModels(ctx)
}

// CanSuggest reports whether a model is configured to suggest mappings.
func (s *Service) CanSuggest() bool { return s.matcher != nil }

// SuggestVehicles asks the model for mappings of up to maxSuggest names. An
// id the catalogue does not have is dropped: the model's answer is a
// proposal checked against the list it was given, never trusted.
func (s *Service) SuggestVehicles(ctx context.Context, unknown []UnknownVehicle) (map[string]VehicleSuggestion, error) {
	if s.matcher == nil {
		return nil, ErrNoMatcher
	}
	models, err := s.aliases.VehicleModels(ctx)
	if err != nil {
		return nil, err
	}
	unknown = unknown[:min(len(unknown), maxSuggest)]
	got, err := s.matcher.Suggest(ctx, unknown, models)
	if err != nil {
		return nil, err
	}
	known := make(map[int64]bool, len(models))
	for _, m := range models {
		known[m.ID] = true
	}
	asked := make(map[string]bool, len(unknown))
	for _, u := range unknown {
		asked[u.Name] = true
	}
	out := make(map[string]VehicleSuggestion, len(got))
	for _, g := range got {
		if !asked[g.Name] {
			continue
		}
		g.ModelIDs = slices.DeleteFunc(slices.Clone(g.ModelIDs), func(id int64) bool { return !known[id] })
		slices.Sort(g.ModelIDs)
		g.ModelIDs = slices.Compact(g.ModelIDs)
		out[g.Name] = g
	}
	return out, nil
}

// AddVehicleAlias stores a mapping the owner confirmed, for the supplier of
// the named profile.
func (s *Service) AddVehicleAlias(ctx context.Context, profile, name string, modelIDs []int64) error {
	p, err := s.profile(profile)
	if err != nil {
		return err
	}
	if len(modelIDs) == 0 {
		return fmt.Errorf("importer: no models for %q", name)
	}
	return s.aliases.AddVehicleAlias(ctx, p.Source, name, vehicleKey(name), modelIDs)
}

// VehicleAliases lists the confirmed mappings.
func (s *Service) VehicleAliases(ctx context.Context) ([]VehicleAlias, error) {
	return s.aliases.ListVehicleAliases(ctx)
}

// RemoveVehicleAlias forgets a mapping and the fitments it made, at once.
func (s *Service) RemoveVehicleAlias(ctx context.Context, id int64) (*VehicleAlias, error) {
	return s.aliases.RemoveVehicleAlias(ctx, id)
}

// Unknown turns a report's unmatched names into what SuggestVehicles takes.
func (r *Report) Unknown() []UnknownVehicle {
	out := make([]UnknownVehicle, len(r.UnmatchedVehicles))
	for i, c := range r.UnmatchedVehicles {
		out[i] = UnknownVehicle{Name: c.Name, Rows: c.N, Examples: r.VehicleExamples[c.Name]}
	}
	return out
}
