// Package saga is the state of one change of the vehicles of a departure: what was asked, which steps ended, and how the saga ended. It
// knows nothing of HTTP or PostgreSQL.
package saga

import "time"

// Step is one step of the saga, named as the contract names it.
type Step string

const (
	LowerCeiling  Step = "LOWER_CEILING"
	ApplyVehicles Step = "APPLY_VEHICLES"
)

// Status is where the saga is. A saga ends COMPLETED, COMPENSATED (undone, nothing is left changed) or FAILED (a person decides).
type Status string

const (
	Running     Status = "RUNNING"
	Completed   Status = "COMPLETED"
	Compensated Status = "COMPENSATED"
	Failed      Status = "FAILED"
)

// Reason says in a short code why a saga did not complete; it is never the text of a participant.
type Reason string

const (
	SeatsHeld              Reason = "SEATS_HELD"
	VehiclesRefused        Reason = "VEHICLES_REFUSED"
	ParticipantUnavailable Reason = "PARTICIPANT_UNAVAILABLE"
	CompensationFailed     Reason = "COMPENSATION_FAILED"
)

// StepState is a step that ended; Compensated says it was undone afterwards.
type StepState struct {
	Name        Step
	Compensated bool
}

// Saga is the persisted state. PreviousCeiling is what the ceiling was before step 1 lowered it (nil when step 1 changed nothing);
// TargetCeiling is the ceiling the saga asks for (nil when the change does not need to lower it).
type Saga struct {
	ID, TenantID, IdempotencyKey, DepartureCode string
	// DepartureID is the salidaId that the preview of operations-cost gave; commercial-reservation takes it on its ceiling route. Empty until the
	// saga decides to lower the ceiling.
	DepartureID                    string
	VehicleIDs                     []string
	PreviousCeiling, TargetCeiling *int
	Status                         Status
	Steps                          []StepState
	FailedStep                     Step
	Reason                         Reason
	Version                        int64
	CreatedAt, UpdatedAt           time.Time
}

// New returns a saga that has just been asked for; the store gives it its id.
func New(tenantID, idempotencyKey, departureCode string, vehicleIDs []string, now time.Time) Saga {
	return Saga{TenantID: tenantID, IdempotencyKey: idempotencyKey, DepartureCode: departureCode, VehicleIDs: vehicleIDs,
		Status: Running, CreatedAt: now, UpdatedAt: now}
}

// Has says whether the step ended.
func (s Saga) Has(step Step) bool {
	for _, st := range s.Steps {
		if st.Name == step {
			return true
		}
	}
	return false
}

// Done records that the step ended.
func (s *Saga) Done(step Step, now time.Time) {
	s.Steps = append(s.Steps, StepState{Name: step})
	s.UpdatedAt = now
}

// Undone records that the step was undone.
func (s *Saga) Undone(step Step, now time.Time) {
	for i := range s.Steps {
		if s.Steps[i].Name == step {
			s.Steps[i].Compensated = true
		}
	}
	s.UpdatedAt = now
}

// End closes the saga. failedStep and reason are empty for a COMPLETED one.
func (s *Saga) End(status Status, failedStep Step, reason Reason, now time.Time) {
	s.Status, s.FailedStep, s.Reason, s.UpdatedAt = status, failedStep, reason, now
}
