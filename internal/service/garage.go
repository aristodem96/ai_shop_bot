package service

import (
	"context"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// GarageService finds vehicles and keeps the buyer's car.
type GarageService struct {
	vehicles VehicleRepository
	garages  GarageRepository
	// chosen is told of every car a buyer chooses, to start fetching what
	// fits it; nil when nothing needs to.
	chosen func(ctx context.Context, vehicleID int64)
}

// OnChoose sets what is told of a car a buyer chooses. The parts catalogue
// uses it to fetch what fits the car before the buyer asks.
func (s *GarageService) OnChoose(f func(ctx context.Context, vehicleID int64)) { s.chosen = f }

// NewGarageService wires a GarageService.
func NewGarageService(vehicles VehicleRepository, garages GarageRepository) *GarageService {
	return &GarageService{vehicles: vehicles, garages: garages}
}

// VehicleQuery describes a car the way a buyer does. Empty fields match all.
type VehicleQuery struct {
	Make   string
	Model  string
	Year   int
	Engine string
}

// maxVehicleMatches bounds one lookup. More matches than this means the
// description was too vague, and the agent should ask rather than list.
const maxVehicleMatches = 10

// Find returns the vehicles matching q. Make, model and engine match as
// case-insensitive substrings, so "octavia" finds "Octavia A7" and "1.4"
// finds "1.4 TSI"; a year must fall within the production span.
func (s *GarageService) Find(ctx context.Context, q VehicleQuery) ([]domain.Vehicle, error) {
	q.Make, q.Model, q.Engine = strings.TrimSpace(q.Make), strings.TrimSpace(q.Model), strings.TrimSpace(q.Engine)
	return s.vehicles.FindVehicles(ctx, q, maxVehicleMatches)
}

// Makes returns the makes the shop covers.
func (s *GarageService) Makes(ctx context.Context) ([]string, error) {
	return s.vehicles.Makes(ctx)
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
	if s.chosen != nil {
		s.chosen(ctx, vehicleID)
	}
	return v, nil
}

// Clear forgets the buyer's car, so the catalogue shows the whole range.
func (s *GarageService) Clear(ctx context.Context, userID int64) error {
	return s.garages.ClearGarage(ctx, userID)
}
