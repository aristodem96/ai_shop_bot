package importer

import (
	"context"
	"errors"
	"strings"
)

// ErrNoSupplier means no supplier has the id or name asked for.
var ErrNoSupplier = errors.New("такого постачальника немає")

// ErrSupplierTaken means another supplier has the name.
var ErrSupplierTaken = errors.New("постачальник з такою назвою вже є")

// SupplierStore keeps the suppliers.
type SupplierStore interface {
	Suppliers(ctx context.Context) ([]Supplier, error)
	// SaveSupplier creates a supplier, setting its ID, or updates one by
	// ID. Its Source is never updated.
	SaveSupplier(ctx context.Context, s *Supplier) error
	DeleteSupplier(ctx context.Context, id int64) error
	// SavePending records the supplier's groups waiting for a category,
	// with their counts and examples from the latest file.
	SavePending(ctx context.Context, supplierID int64, groups []PendingGroup) error
}

// Service runs imports for the managers' chat and the command line.
type Service struct {
	store     Store
	vehicles  VehicleStore
	aliases   AliasStore
	suppliers SupplierStore
	matcher   VehicleMatcher
	proposer  VehicleProposer
}

// Deps are what a Service works with. Matcher and Proposer may be nil:
// vehicle names are then matched, and vehicles added, by hand only.
type Deps struct {
	Prices    Store
	Vehicles  VehicleStore
	Aliases   AliasStore
	Suppliers SupplierStore
	Matcher   VehicleMatcher
	Proposer  VehicleProposer
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	return &Service{store: d.Prices, vehicles: d.Vehicles, aliases: d.Aliases, suppliers: d.Suppliers,
		matcher: d.Matcher, proposer: d.Proposer}
}

// Suppliers lists the suppliers.
func (s *Service) Suppliers(ctx context.Context) ([]Supplier, error) {
	return s.suppliers.Suppliers(ctx)
}

// Supplier finds a supplier by id, or by name when id is zero.
func (s *Service) Supplier(ctx context.Context, id int64, name string) (*Supplier, error) {
	all, err := s.suppliers.Suppliers(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if (id != 0 && all[i].ID == id) || (id == 0 && strings.EqualFold(all[i].Name, strings.TrimSpace(name))) {
			return &all[i], nil
		}
	}
	return nil, ErrNoSupplier
}

// Inspect reads a file's header and finds the supplier it is from.
func (s *Service) Inspect(ctx context.Context, path string) (*Inspection, error) {
	known, err := s.suppliers.Suppliers(ctx)
	if err != nil {
		return nil, err
	}
	return Inspect(path, known)
}

// SaveSupplier checks a supplier and keeps it. A new one gets its key from
// its name, one no other supplier has.
func (s *Service) SaveSupplier(ctx context.Context, sup *Supplier) error {
	sup.Name = strings.Join(strings.Fields(sup.Name), " ")
	if err := sup.Validate(); err != nil {
		return err
	}
	all, err := s.suppliers.Suppliers(ctx)
	if err != nil {
		return err
	}
	for _, x := range all {
		if x.ID != sup.ID && strings.EqualFold(x.Name, sup.Name) {
			return ErrSupplierTaken
		}
	}
	if sup.ID == 0 {
		sup.Source = SourceFor(sup.Name, func(src string) bool {
			for _, x := range all {
				if x.Source == src {
					return true
				}
			}
			return false
		})
	}
	return s.suppliers.SaveSupplier(ctx, sup)
}

// DeleteSupplier forgets a supplier. Its products stay as they are; a file
// from it is a new supplier's then.
func (s *Service) DeleteSupplier(ctx context.Context, id int64) error {
	return s.suppliers.DeleteSupplier(ctx, id)
}

// Prices imports a price list as the supplier's.
func (s *Service) Prices(ctx context.Context, supplierID int64, path string, opts Options) (*Report, Result, error) {
	sup, err := s.Supplier(ctx, supplierID, "")
	if err != nil {
		return nil, Result{}, err
	}
	rep, res, err := Run(ctx, s.store, sup, path, opts)
	// Recorded even on a dry run, which rolls back its own transaction:
	// the groups are there to be mapped before the file is applied.
	if rep != nil && len(rep.Pending) > 0 {
		if perr := s.suppliers.SavePending(ctx, sup.ID, rep.Pending); perr != nil && err == nil {
			err = perr
		}
	}
	return rep, res, err
}

// Vehicles loads a vehicle directory.
func (s *Service) Vehicles(ctx context.Context, path string, dryRun bool) ([]Problem, VehicleResult, error) {
	return ImportVehicles(ctx, s.vehicles, path, dryRun)
}
