package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

func newGarageFixture() *GarageService {
	f := &fakeVehicles{garages: map[int64]int64{}, list: []domain.Vehicle{
		{ID: 1, Make: "Daewoo", Model: "Lanos", YearFrom: 1997, YearTo: 2009, Engine: "1.5"},
		{ID: 2, Make: "Skoda", Model: "Octavia A7", YearFrom: 2013, YearTo: 2020, Engine: "1.4 TSI"},
		{ID: 3, Make: "Skoda", Model: "Octavia A7", YearFrom: 2013, YearTo: 2020, Engine: "2.0 TDI"},
		{ID: 4, Make: "Toyota", Model: "Corolla E210", YearFrom: 2019, Engine: "1.6"},
	}}
	return NewGarageService(f, f)
}

func TestFind(t *testing.T) {
	svc := newGarageFixture()
	ctx := context.Background()

	got, err := svc.Find(ctx, VehicleQuery{Make: "skoda", Model: "octavia", Year: 2015, Engine: "1.4"})
	require.NoError(t, err)
	require.Len(t, got, 1, "case-insensitive substrings and a year within the span")
	assert.Equal(t, int64(2), got[0].ID)

	got, err = svc.Find(ctx, VehicleQuery{Model: "Octavia", Year: 2021})
	require.NoError(t, err)
	assert.Empty(t, got, "2021 is after the A7 was produced")

	got, err = svc.Find(ctx, VehicleQuery{Make: "Toyota", Year: 2026})
	require.NoError(t, err)
	assert.Len(t, got, 1, "a model still in production matches any later year")
}

func TestFindTrimsInput(t *testing.T) {
	got, err := newGarageFixture().Find(context.Background(), VehicleQuery{Make: "  Skoda ", Engine: " 2.0 "})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, int64(3), got[0].ID)
}

func TestGarage(t *testing.T) {
	svc := newGarageFixture()
	ctx := context.Background()

	v, err := svc.Get(ctx, user)
	require.NoError(t, err)
	assert.Nil(t, v, "no car chosen yet")

	v, err = svc.Set(ctx, user, 2)
	require.NoError(t, err)
	assert.Equal(t, "1.4 TSI", v.Engine)

	_, err = svc.Set(ctx, user, 99)
	require.ErrorIs(t, err, domain.ErrNotFound, "an unknown vehicle cannot be chosen")

	require.NoError(t, svc.Clear(ctx, user))
	v, err = svc.Get(ctx, user)
	require.NoError(t, err)
	assert.Nil(t, v)
}
