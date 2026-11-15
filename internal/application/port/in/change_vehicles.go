// Package in holds what the entry of the saga asks of it.
package in

import (
	"context"

	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/domain/saga"
)

// StartCommand is the request to change the vehicles of a departure; the tenant is the one of the token, never of the body.
type StartCommand struct {
	TenantID, IdempotencyKey, DepartureCode string
	VehicleIDs                              []string
}

// ChangeDepartureVehicles is the use case behind the two routes of the saga.
type ChangeDepartureVehicles interface {
	// Start runs the saga to its end; created is false when the key was already used and nothing ran.
	Start(ctx context.Context, cmd StartCommand) (s saga.Saga, created bool, err error)
	Get(ctx context.Context, tenantID, id string) (saga.Saga, error)
}
