package participants

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/application/port/out"
)

const reservationsPrefix = "/api/v1/commercial-reservation"

// Reservations implements out.Reservations over HTTP.
type Reservations struct{ c *client }

var _ out.Reservations = (*Reservations)(nil)

func NewReservations(baseURL string, cfg Config) *Reservations {
	return &Reservations{newClient(baseURL, cfg)}
}

// SetCeiling puts the ceiling of seats of a departure (plan D-03). It is refused with out.ErrSeatsHeld when more seats are held.
func (r *Reservations) SetCeiling(ctx context.Context, tenantID, departureCode string, ceiling int, key string) (out.CeilingResult, error) {
	path, err := departurePath(reservationsPrefix, tenantID, departureCode, "/capacity-ceiling")
	if err != nil {
		return out.CeilingResult{}, err
	}
	status, reply, err := r.c.do(ctx, http.MethodPut, path, key, map[string]int{"ceiling": ceiling})
	if err != nil {
		return out.CeilingResult{}, err
	}
	if status != http.StatusOK {
		if status >= 400 {
			return out.CeilingResult{}, refusal(status, reply, true)
		}
		return out.CeilingResult{}, errUnclear("status " + strconv.Itoa(status))
	}
	var answer struct{ PreviousCeiling, Ceiling, SeatsHeld *int }
	if json.Unmarshal(reply, &answer) != nil || answer.PreviousCeiling == nil || answer.Ceiling == nil || answer.SeatsHeld == nil {
		return out.CeilingResult{}, errUnclear("the answer has not its fields")
	}
	return out.CeilingResult{Previous: *answer.PreviousCeiling, Ceiling: *answer.Ceiling, SeatsHeld: *answer.SeatsHeld}, nil
}
