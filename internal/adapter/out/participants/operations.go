package participants

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/application/port/out"
)

const operationsPrefix = "/api/v1/operations-cost"

// Operations implements out.Operations over HTTP.
type Operations struct{ c *client }

var _ out.Operations = (*Operations)(nil)

func NewOperations(baseURL string, cfg Config) *Operations {
	return &Operations{newClient(baseURL, cfg)}
}

// PreviewChange asks what the new set of vehicles would give; it changes nothing (plan D-03).
func (o *Operations) PreviewChange(ctx context.Context, tenantID, departureCode string, vehicleIDs []string) (out.Preview, error) {
	path, err := departurePath(operationsPrefix, tenantID, departureCode, "/vehicle-changes/preview")
	if err != nil {
		return out.Preview{}, err
	}
	status, reply, err := o.c.do(ctx, http.MethodPost, path, "", map[string][]string{"vehicleIds": vehicleIDs})
	if err != nil {
		return out.Preview{}, err
	}
	if status != http.StatusOK {
		if status >= 400 {
			return out.Preview{}, refusal(status, reply, false)
		}
		return out.Preview{}, errUnclear("status " + strconv.Itoa(status))
	}
	var answer struct{ CurrentEffectiveCapacity, NewEffectiveCapacity *int }
	if json.Unmarshal(reply, &answer) != nil || answer.CurrentEffectiveCapacity == nil || answer.NewEffectiveCapacity == nil {
		return out.Preview{}, errUnclear("the answer has not its fields")
	}
	return out.Preview{CurrentEffectiveCapacity: *answer.CurrentEffectiveCapacity, NewEffectiveCapacity: *answer.NewEffectiveCapacity}, nil
}

// ApplyChange puts the whole new set of vehicles on the departure; the key makes a repeat safe.
func (o *Operations) ApplyChange(ctx context.Context, tenantID, departureCode string, vehicleIDs []string, key string) error {
	path, err := departurePath(operationsPrefix, tenantID, departureCode, "/vehicles")
	if err != nil {
		return err
	}
	status, reply, err := o.c.do(ctx, http.MethodPut, path, key, map[string][]string{"vehicleIds": vehicleIDs})
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusOK:
		return nil
	case status >= 400:
		return refusal(status, reply, false)
	default:
		return errUnclear("status " + strconv.Itoa(status))
	}
}
