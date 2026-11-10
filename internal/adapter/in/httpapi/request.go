package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
)

const (
	maxBodyBytes = 16 << 10
	maxVehicles  = 50
)

// plainCode is the shape of the code of a departure: it goes into a path of the participants, so nothing else is accepted.
var plainCode = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)

type startRequest struct {
	DepartureCode string   `json:"departureCode"`
	VehicleIDs    []string `json:"vehicleIds"`
}

type fieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// decodeStart reads the body strictly: one JSON object, no field the contract does not declare (the tenant is never read from the body).
// The messages name the field and the rule, never the value.
func decodeStart(r *http.Request, w http.ResponseWriter) (string, []string, []fieldError) {
	var req startRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return "", nil, []fieldError{{"body", "must be one JSON object with only the declared fields"}}
	}
	var problems []fieldError
	if !plainCode.MatchString(req.DepartureCode) {
		problems = append(problems, fieldError{"departureCode", "must be 1 to 40 letters, digits, dots, hyphens or underscores"})
	}
	switch {
	case len(req.VehicleIDs) < 1 || len(req.VehicleIDs) > maxVehicles:
		problems = append(problems, fieldError{"vehicleIds", "must have 1 to 50 vehicles"})
	default:
		seen := map[string]bool{}
		for _, id := range req.VehicleIDs {
			lower := strings.ToLower(id)
			if !uuidPattern.MatchString(id) || seen[lower] {
				problems = append(problems, fieldError{"vehicleIds", "must be distinct vehicle ids"})
				break
			}
			seen[lower] = true
		}
	}
	return req.DepartureCode, req.VehicleIDs, problems
}
