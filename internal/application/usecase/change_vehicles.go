// Package usecase is the orchestrator of the saga: it calls the participants in order, saves the state after every step and undoes
// what it must.
package usecase

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/application/port/in"
	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/application/port/out"
	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/domain/saga"
)

const (
	// restoreAttempts is how many times the restoring of the ceiling is tried before the saga ends FAILED for a person (plan D-06).
	restoreAttempts = 5
	resumeBatch     = 50
	// giveUpAfter is how long a saga may stay RUNNING because a participant gave no clear answer; then it ends FAILED for a person.
	giveUpAfter = 15 * time.Minute
)

// ChangeVehicles is the orchestrator of the saga.
type ChangeVehicles struct {
	reservations out.Reservations
	operations   out.Operations
	store        out.Store
	clock        func() time.Time
	log          *slog.Logger
}

var _ in.ChangeDepartureVehicles = (*ChangeVehicles)(nil)

// NewChangeVehicles returns the orchestrator over its participants and its store.
func NewChangeVehicles(r out.Reservations, o out.Operations, st out.Store, clock func() time.Time, log *slog.Logger) *ChangeVehicles {
	return &ChangeVehicles{reservations: r, operations: o, store: st, clock: clock, log: log}
}

// Start creates the saga, or returns the one that has the same tenant and key without running anything, and runs it to its end.
// A departure that does not exist ends the saga undone and is also returned as out.ErrNotFound.
func (c *ChangeVehicles) Start(ctx context.Context, cmd in.StartCommand) (saga.Saga, bool, error) {
	s, created, err := c.store.Create(ctx, saga.New(cmd.TenantID, cmd.IdempotencyKey, cmd.DepartureCode, cmd.VehicleIDs, c.clock()))
	if err != nil || !created {
		return s, false, err
	}
	s, err = c.run(ctx, s)
	return s, true, err
}

// Get reads a saga of the tenant; another tenant's is out.ErrNotFound.
func (c *ChangeVehicles) Get(ctx context.Context, tenantID, id string) (saga.Saga, error) {
	return c.store.Get(ctx, tenantID, id)
}

// Resume continues the RUNNING sagas that have not moved since before, each from its last ended step; a saga another instance saved
// first is left to it. It returns how many it took.
func (c *ChangeVehicles) Resume(ctx context.Context, before time.Time) (int, error) {
	stale, err := c.store.ListStale(ctx, before, resumeBatch)
	if err != nil {
		return 0, err
	}
	resumed := 0
	for _, s := range stale {
		s.UpdatedAt = c.clock()
		claimed, err := c.store.Save(ctx, s) // raises the version: a second instance with the old one is refused
		if errors.Is(err, out.ErrStale) {
			continue
		}
		if err != nil {
			c.log.ErrorContext(ctx, "a saga could not be claimed", "sagaId", s.ID, "error", err.Error())
			continue
		}
		resumed++
		if _, err := c.run(ctx, claimed); err != nil {
			c.log.ErrorContext(ctx, "resumed saga did not end well", "sagaId", claimed.ID, "error", err.Error())
		}
	}
	return resumed, nil
}

// run goes on from the first step that has not ended.
func (c *ChangeVehicles) run(ctx context.Context, s saga.Saga) (saga.Saga, error) {
	if !s.Has(saga.LowerCeiling) {
		var ended bool
		var err error
		if s, ended, err = c.lowerCeiling(ctx, s); ended || err != nil {
			return s, err
		}
	}
	return c.applyVehicles(ctx, s)
}

// lowerCeiling is step 1: it asks operations-cost what the new vehicles give and, if that is fewer seats than today, lowers the
// ceiling to it. commercial-reservation refuses when more seats are held, and then nothing has changed anywhere.
func (c *ChangeVehicles) lowerCeiling(ctx context.Context, s saga.Saga) (saga.Saga, bool, error) {
	preview, err := c.operations.PreviewChange(ctx, s.TenantID, s.DepartureCode, s.VehicleIDs)
	if err != nil {
		ended, endErr := c.end(ctx, s, saga.Compensated, saga.LowerCeiling, reasonOf(err))
		if errors.Is(err, out.ErrNotFound) {
			return ended, true, errors.Join(out.ErrNotFound, endErr)
		}
		return ended, true, endErr
	}
	if preview.NewEffectiveCapacity >= preview.CurrentEffectiveCapacity {
		// adding seats cannot oversell: the ceiling stays and operations-cost tells commercial-reservation the new total afterwards
		s.Done(saga.LowerCeiling, c.clock())
		saved, err := c.store.Save(ctx, s)
		return saved, false, err
	}
	target := preview.NewEffectiveCapacity
	s.TargetCeiling = &target // kept with the step, as the record of what was asked
	result, err := c.reservations.SetCeiling(ctx, s.TenantID, s.DepartureCode, target, s.ID+":LOWER_CEILING")
	if err != nil {
		if !definite(err) {
			ended, endErr := c.unclear(ctx, s, saga.LowerCeiling, err)
			return ended, true, endErr
		}
		ended, endErr := c.end(ctx, s, saga.Compensated, saga.LowerCeiling, reasonOf(err))
		return ended, true, endErr
	}
	s.PreviousCeiling = &result.Previous
	s.Done(saga.LowerCeiling, c.clock())
	saved, err := c.store.Save(ctx, s)
	return saved, false, err
}

// applyVehicles is step 2, the last one. If it fails the ceiling that step 1 lowered is restored.
func (c *ChangeVehicles) applyVehicles(ctx context.Context, s saga.Saga) (saga.Saga, error) {
	if err := c.operations.ApplyChange(ctx, s.TenantID, s.DepartureCode, s.VehicleIDs, s.ID+":APPLY_VEHICLES"); err != nil {
		if !definite(err) {
			return c.unclear(ctx, s, saga.ApplyVehicles, err)
		}
		return c.compensate(ctx, s, reasonOf(err))
	}
	s.Done(saga.ApplyVehicles, c.clock())
	s.End(saga.Completed, "", "", c.clock())
	return c.store.Save(ctx, s)
}

// compensate undoes step 1 if it changed the ceiling. The saga ends COMPENSATED when it could, FAILED when the ceiling could not be
// restored in time, so that a person decides.
func (c *ChangeVehicles) compensate(ctx context.Context, s saga.Saga, reason saga.Reason) (saga.Saga, error) {
	if s.PreviousCeiling == nil {
		return c.end(ctx, s, saga.Compensated, saga.ApplyVehicles, reason)
	}
	for attempt := 1; attempt <= restoreAttempts; attempt++ {
		_, err := c.reservations.SetCeiling(ctx, s.TenantID, s.DepartureCode, *s.PreviousCeiling, s.ID+":LOWER_CEILING:undo")
		if err == nil {
			s.Undone(saga.LowerCeiling, c.clock())
			return c.end(ctx, s, saga.Compensated, saga.ApplyVehicles, reason)
		}
		c.log.WarnContext(ctx, "restoring the ceiling failed", "sagaId", s.ID, "attempt", attempt, "error", err.Error())
	}
	return c.end(ctx, s, saga.Failed, saga.ApplyVehicles, saga.CompensationFailed)
}

// unclear handles an answer that is neither a yes nor a clear refusal (network, 429, 5xx after the attempts of the client): the
// participant may have done the work and lost the reply, so the saga says nothing is undone and undoes nothing. It stays RUNNING and the
// resumption asks again with the same key, which the participant repeats safely; after giveUpAfter it ends FAILED for a person.
// On an error the saga returned is the one the caller had, still RUNNING and saved.
func (c *ChangeVehicles) unclear(ctx context.Context, s saga.Saga, step saga.Step, cause error) (saga.Saga, error) {
	c.log.WarnContext(ctx, "a participant gave no clear answer", "sagaId", s.ID, "step", string(step), "error", cause.Error())
	if c.clock().Sub(s.CreatedAt) >= giveUpAfter {
		return c.end(ctx, s, saga.Failed, step, saga.ParticipantUnavailable)
	}
	return s, nil
}

func (c *ChangeVehicles) end(ctx context.Context, s saga.Saga, status saga.Status, failedStep saga.Step, reason saga.Reason) (saga.Saga, error) {
	s.End(status, failedStep, reason, c.clock())
	return c.store.Save(ctx, s)
}

// definite says whether the answer of a participant is a definite yes or no. Any other error leaves open whether the work was done.
func definite(err error) bool {
	return errors.Is(err, out.ErrSeatsHeld) || errors.Is(err, out.ErrRefused) || errors.Is(err, out.ErrNotFound)
}

// reasonOf turns what a participant answered into the short code the saga keeps; the text of the answer is dropped.
func reasonOf(err error) saga.Reason {
	switch {
	case errors.Is(err, out.ErrSeatsHeld):
		return saga.SeatsHeld
	case errors.Is(err, out.ErrRefused), errors.Is(err, out.ErrNotFound):
		return saga.VehiclesRefused
	default:
		return saga.ParticipantUnavailable
	}
}
