// Package out holds what the saga needs from the outside, defined by the saga: the two participants and the store.
package out

import (
	"context"
	"errors"
	"time"

	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/domain/saga"
)

// The answers the saga tells apart. Any other error is a participant that did not answer well (network, 429, 5xx) after its attempts.
var (
	ErrSeatsHeld = errors.New("more seats are held than the ceiling")
	ErrNotFound  = errors.New("not found")
	ErrRefused   = errors.New("refused by a rule of the participant")
	ErrStale     = errors.New("the saga changed since it was read")
)

// CeilingResult is what commercial-reservation answers when it sets the ceiling of a departure.
type CeilingResult struct{ Previous, Ceiling, SeatsHeld int }

// Reservations is commercial-reservation, which owns the ceiling of seats of a departure.
type Reservations interface {
	// SetCeiling sets the ceiling; it is repeatable with the same key, and it refuses with ErrSeatsHeld when more seats are held.
	SetCeiling(ctx context.Context, tenantID, departureCode string, ceiling int, idempotencyKey string) (CeilingResult, error)
}

// Preview is what a change of vehicles would do to the seats of the departure; it changes nothing.
type Preview struct{ CurrentEffectiveCapacity, NewEffectiveCapacity int }

// Operations is operations-cost, which owns the vehicles of a departure.
type Operations interface {
	PreviewChange(ctx context.Context, tenantID, departureCode string, vehicleIDs []string) (Preview, error)
	// ApplyChange puts the whole new set of vehicles; it is repeatable with the same key.
	ApplyChange(ctx context.Context, tenantID, departureCode string, vehicleIDs []string, idempotencyKey string) error
}

// Store keeps the sagas. Create returns the saga that already has that tenant and key, with created false.
type Store interface {
	Create(ctx context.Context, s saga.Saga) (stored saga.Saga, created bool, err error)
	// Save writes the saga only if its version is the stored one and raises it; otherwise ErrStale.
	Save(ctx context.Context, s saga.Saga) (saved saga.Saga, err error)
	Get(ctx context.Context, tenantID, id string) (saga.Saga, error)
	// ListStale returns up to limit RUNNING sagas not updated since before.
	ListStale(ctx context.Context, before time.Time, limit int) ([]saga.Saga, error)
}
