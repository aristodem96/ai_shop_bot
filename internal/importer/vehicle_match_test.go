package importer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMatcher struct {
	got   []VehicleSuggestion
	asked []UnknownVehicle
}

func (f *fakeMatcher) Suggest(_ context.Context, unknown []UnknownVehicle, _ []Model) ([]VehicleSuggestion, error) {
	f.asked = unknown
	return f.got, nil
}

type fakeAliases struct{ models []Model }

func (f *fakeAliases) VehicleModels(context.Context) ([]Model, error) { return f.models, nil }
func (f *fakeAliases) AddVehicleAlias(context.Context, string, string, string, []int64) error {
	return nil
}
func (f *fakeAliases) ListVehicleAliases(context.Context) ([]VehicleAlias, error) { return nil, nil }
func (f *fakeAliases) RemoveVehicleAlias(context.Context, int64) (*VehicleAlias, error) {
	return nil, nil
}

func TestSuggestVehiclesTrustsNothingItCannotCheck(t *testing.T) {
	m := &fakeMatcher{got: []VehicleSuggestion{
		{Name: "Chery Tiggo", ModelIDs: []int64{12, 999, 11, 12}, Note: "сімейство"},
		{Name: "Invented Car", ModelIDs: []int64{11}},
	}}
	s := NewService(Deps{Aliases: &fakeAliases{models: []Model{{ID: 11}, {ID: 12}}}, Matcher: m}, "")

	got, err := s.SuggestVehicles(context.Background(), []UnknownVehicle{{Name: "Chery Tiggo"}})
	require.NoError(t, err)
	assert.Equal(t, map[string]VehicleSuggestion{
		"Chery Tiggo": {Name: "Chery Tiggo", ModelIDs: []int64{11, 12}, Note: "сімейство"},
	}, got, "an id the catalogue lacks and a name nobody asked about are dropped")
}

func TestSuggestVehiclesBoundsTheRequest(t *testing.T) {
	m := &fakeMatcher{}
	s := NewService(Deps{Aliases: &fakeAliases{}, Matcher: m}, "")
	many := make([]UnknownVehicle, maxSuggest+10)
	_, err := s.SuggestVehicles(context.Background(), many)
	require.NoError(t, err)
	assert.Len(t, m.asked, maxSuggest)

	_, err = NewService(Deps{Aliases: &fakeAliases{}}, "").SuggestVehicles(context.Background(), many)
	require.ErrorIs(t, err, ErrNoMatcher)
}
