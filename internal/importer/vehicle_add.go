package importer

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// A vehicle the directory lacks -- "Peugeot 308" in a price list -- can be
// added from the chat. A model proposes the entries, the owner removes or
// corrects them, and only what the owner keeps is written. Years and engines
// from a model are its general knowledge, not a workshop reference: the
// owner is told so, and the proposal is checked like any row of the file.

// ProposeRequest is what a proposal is made from.
type ProposeRequest struct {
	// Name is the vehicle as the supplier or the owner wrote it; Examples
	// are product names that mention it.
	Name     string
	Examples []string
	// Correction is the owner's words on the previous proposal: "роки
	// 2008-2013, тільки дизель".
	Correction string
	Previous   []Vehicle
}

// VehicleProposal is a model's proposed directory entries.
type VehicleProposal struct {
	Vehicles []Vehicle
	// Note is the model's one line for the owner.
	Note string
	// Rejected lists entries the model proposed that failed the checks,
	// with why: the owner sees what was dropped.
	Rejected []string
}

// VehicleProposer proposes directory entries for a vehicle.
type VehicleProposer interface {
	ProposeVehicles(ctx context.Context, req ProposeRequest, models []Model) (*VehicleProposal, error)
}

// maxProposed bounds one proposal: a model sold in Ukraine with more than
// four engines worth stocking parts for is rare, and a long list is one the
// owner stops reading.
const maxProposed = 6

// ErrNoProposer means no model is configured to propose vehicles.
var ErrNoProposer = errors.New("importer: no vehicle proposer")

// CanPropose reports whether a model is configured to propose vehicles.
func (s *Service) CanPropose() bool { return s.proposer != nil }

// ProposeVehicles asks the model for directory entries and checks them. An
// entry that fails the directory's rules is dropped and named in Rejected;
// one already in the directory is dropped quietly, it needs no adding.
func (s *Service) ProposeVehicles(ctx context.Context, req ProposeRequest) (*VehicleProposal, error) {
	if s.proposer == nil {
		return nil, ErrNoProposer
	}
	models, err := s.aliases.VehicleModels(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.proposer.ProposeVehicles(ctx, req, models)
	if err != nil {
		return nil, err
	}
	out := &VehicleProposal{Note: p.Note}
	seen := make(map[string]bool)
	for _, v := range p.Vehicles {
		if err := v.Normalize(); err != nil {
			out.Rejected = append(out.Rejected, fmt.Sprintf("%s %s %s: %v", v.Make, v.Model, v.Engine, err))
			continue
		}
		if seen[v.Code()] || len(out.Vehicles) == maxProposed {
			continue
		}
		seen[v.Code()] = true
		out.Vehicles = append(out.Vehicles, v)
	}
	return out, nil
}

// AddVehicles writes vehicles to the directory and returns the ids of their
// models, to map a supplier's name to. A vehicle already there is updated,
// not doubled: the directory matches on make, model, engine and first year.
func (s *Service) AddVehicles(ctx context.Context, vs []Vehicle) ([]int64, error) {
	if len(vs) == 0 {
		return nil, errors.New("importer: no vehicles to add")
	}
	for i := range vs {
		if err := vs[i].Normalize(); err != nil {
			return nil, err
		}
	}
	if _, err := s.vehicles.ApplyVehicles(ctx, vs, false); err != nil {
		return nil, err
	}
	models, err := s.aliases.VehicleModels(ctx)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, v := range vs {
		for _, m := range models {
			if m.Make == v.Make && m.Model == v.Model && !slices.Contains(ids, m.ID) {
				ids = append(ids, m.ID)
			}
		}
	}
	return ids, nil
}
