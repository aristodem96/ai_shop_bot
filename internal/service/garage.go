package service

import (
	"context"
	"slices"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// GarageService manages the buyer's car and the vehicle list it is chosen
// from.
type GarageService struct {
	vehicles VehicleRepository
	garages  GarageRepository
}

// NewGarageService wires a GarageService.
func NewGarageService(vehicles VehicleRepository, garages GarageRepository) *GarageService {
	return &GarageService{vehicles: vehicles, garages: garages}
}

// Makes returns one vehicle per make, in alphabetical order. The bot builds
// its make menu from it and uses each vehicle's id to stand for its make,
// which keeps callback data numeric and short.
func (s *GarageService) Makes(ctx context.Context) ([]domain.Vehicle, error) {
	all, err := s.vehicles.Vehicles(ctx)
	if err != nil {
		return nil, err
	}
	var makes []domain.Vehicle
	for _, v := range all {
		if len(makes) == 0 || makes[len(makes)-1].Make != v.Make {
			makes = append(makes, v)
		}
	}
	return makes, nil
}

// SameMake returns every vehicle of the make of the given vehicle.
func (s *GarageService) SameMake(ctx context.Context, vehicleID int64) ([]domain.Vehicle, error) {
	ref, err := s.vehicles.Vehicle(ctx, vehicleID)
	if err != nil {
		return nil, err
	}
	all, err := s.vehicles.Vehicles(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(v domain.Vehicle) bool { return v.Make != ref.Make }), nil
}

// VehicleQuery describes a car the way a buyer does. Empty fields match all.
type VehicleQuery struct {
	Make   string
	Model  string
	Year   int
	Engine string
}

// Find returns the vehicles matching q. Make, model and engine match as
// case-insensitive substrings, so "octavia" finds "Octavia A7" and "1.4"
// finds "1.4 TSI"; a year must fall within the production span.
func (s *GarageService) Find(ctx context.Context, q VehicleQuery) ([]domain.Vehicle, error) {
	all, err := s.vehicles.Vehicles(ctx)
	if err != nil {
		return nil, err
	}
	has := func(field, want string) bool {
		return want == "" || strings.Contains(strings.ToLower(field), strings.ToLower(strings.TrimSpace(want)))
	}
	return slices.DeleteFunc(all, func(v domain.Vehicle) bool {
		inYears := q.Year == 0 || (q.Year >= v.YearFrom && (v.YearTo == 0 || q.Year <= v.YearTo))
		matches := has(v.Make, q.Make) && has(v.Model, q.Model) && has(v.Engine, q.Engine) && inYears
		return !matches
	}), nil
}

// Get returns the buyer's car, or nil if none was chosen.
func (s *GarageService) Get(ctx context.Context, userID int64) (*domain.Vehicle, error) {
	return s.garages.Garage(ctx, userID)
}

// Set chooses the buyer's car.
func (s *GarageService) Set(ctx context.Context, userID, vehicleID int64) (*domain.Vehicle, error) {
	v, err := s.vehicles.Vehicle(ctx, vehicleID)
	if err != nil {
		return nil, err
	}
	if err := s.garages.SetGarage(ctx, userID, vehicleID); err != nil {
		return nil, err
	}
	return v, nil
}

// Clear forgets the buyer's car, so the catalogue shows the whole range.
func (s *GarageService) Clear(ctx context.Context, userID int64) error {
	return s.garages.ClearGarage(ctx, userID)
}
