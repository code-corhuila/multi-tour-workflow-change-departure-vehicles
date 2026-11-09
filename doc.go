// Package workflow is the saga that changes the vehicles of a departure (ADR-004 D3, HU-WFL-005): it lowers the ceiling of seats in
// commercial-reservation and then lets operations-cost apply the change, restoring the ceiling if that fails. The code lives under cmd/
// and internal/.
package workflow
