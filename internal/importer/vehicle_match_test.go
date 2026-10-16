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

type fakeProposer struct{ p *VehicleProposal }

func (f fakeProposer) ProposeVehicles(context.Context, ProposeRequest, []Model) (*VehicleProposal, error) {
	return f.p, nil
}

func TestProposeVehiclesChecksWhatTheModelSays(t *testing.T) {
	s := NewService(Deps{Aliases: &fakeAliases{}, Proposer: fakeProposer{&VehicleProposal{Note: "ок", Vehicles: []Vehicle{
		{Make: " Peugeot ", Model: "308  T7", YearFrom: 2007, YearTo: 2013, Engine: "1.6 HDi", Fuel: "diesel"},
		{Make: "Peugeot", Model: "308 T7", YearFrom: 2007, YearTo: 2013, Engine: "1.6 HDi", Fuel: "diesel"},
		{Make: "Peugeot", Model: "308 T7", YearFrom: 2013, YearTo: 2007, Engine: "1.6 VTi", Fuel: "petrol"},
		{Make: "Peugeot", Model: "308 T7", YearFrom: 2007, Engine: "1.4", Fuel: "steam"},
	}}}}, "")

	p, err := s.ProposeVehicles(context.Background(), ProposeRequest{Name: "Peugeot 308"})
	require.NoError(t, err)
	assert.Equal(t, []Vehicle{{Make: "Peugeot", Model: "308 T7", YearFrom: 2007, YearTo: 2013, Engine: "1.6 HDi", Fuel: "diesel"}},
		p.Vehicles, "tidied, and the same entry twice is one")
	require.Len(t, p.Rejected, 2, "a proposal passes the checks a file row does")
	assert.Contains(t, p.Rejected[0], "навпаки")
	assert.Contains(t, p.Rejected[1], "пальне")

	_, err = NewService(Deps{Aliases: &fakeAliases{}}, "").ProposeVehicles(context.Background(), ProposeRequest{})
	require.ErrorIs(t, err, ErrNoProposer)
}

func TestParseVehicleLine(t *testing.T) {
	v, err := ParseVehicleLine("Peugeot, 308 T7, 2007, 2013, 1.6 HDi, дизель")
	require.NoError(t, err)
	assert.Equal(t, Vehicle{Make: "Peugeot", Model: "308 T7", YearFrom: 2007, YearTo: 2013, Engine: "1.6 HDi", Fuel: "diesel"}, v)

	v, err = ParseVehicleLine("Tesla, Model Y, 2020, н.ч., EV, електро")
	require.NoError(t, err)
	assert.Zero(t, v.YearTo, "\"н.ч.\" is a car still produced")
	assert.Equal(t, "electric", v.Fuel)

	_, err = ParseVehicleLine("Peugeot 308 2007")
	require.ErrorContains(t, err, "6 частин")
	_, err = ParseVehicleLine("Peugeot, 308, рік, 2013, 1.6, дизель")
	require.ErrorContains(t, err, "рік початку")
}
