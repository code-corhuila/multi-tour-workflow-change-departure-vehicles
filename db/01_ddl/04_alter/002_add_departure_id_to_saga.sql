-- The salidaId of the departure: the ceiling route of commercial-reservation takes it, not the SAL-NNN code, and the saga learns it from the
-- preview of operations-cost, which owns the departure (HU-WFL-005, plan D-03). It is a reference to operations-cost, never a foreign key
-- (Annex J, J.3.4). It stays empty for a saga that did not need to lower the ceiling, and for the sagas written before this column.
-- Guarded, so applying the changeset again does nothing.
ALTER TABLE workflow_change_departure_vehicles.change_departure_vehicles_saga ADD COLUMN IF NOT EXISTS departure_id uuid;
