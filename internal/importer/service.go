package importer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ErrUnknownProfile means no profile has the name asked for.
var ErrUnknownProfile = errors.New("importer: no such profile")

// Service runs imports for a front end other than the command line -- the
// managers' chat. Profiles are files in one directory, named after the file:
// "chinaavto" is profiles/chinaavto.json. Adding a supplier is adding a file
// there; the people uploading price lists only ever pick one by name.
type Service struct {
	store    Store
	vehicles VehicleStore
	dir      string
}

// NewService returns a Service reading profiles from dir.
func NewService(store Store, vehicles VehicleStore, dir string) *Service {
	return &Service{store: store, vehicles: vehicles, dir: dir}
}

// Profiles lists the profile names, sorted.
func (s *Service) Profiles() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("importer: list profiles in %s: %w", s.dir, err)
	}
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".json"); ok && !e.IsDir() {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names, nil
}

// Prices imports a price list with the named profile.
func (s *Service) Prices(ctx context.Context, profile, path string, opts Options) (*Report, Result, error) {
	// The name comes from a chat. Only names that are files in the
	// directory are opened, so "../../etc/x" never becomes a path.
	names, err := s.Profiles()
	if err != nil {
		return nil, Result{}, err
	}
	if !slices.Contains(names, profile) {
		return nil, Result{}, fmt.Errorf("%w: %q", ErrUnknownProfile, profile)
	}
	p, err := LoadProfile(filepath.Join(s.dir, profile+".json"))
	if err != nil {
		return nil, Result{}, err
	}
	return Run(ctx, s.store, p, path, opts)
}

// Vehicles loads a vehicle directory.
func (s *Service) Vehicles(ctx context.Context, path string, dryRun bool) ([]Problem, VehicleResult, error) {
	return ImportVehicles(ctx, s.vehicles, path, dryRun)
}
