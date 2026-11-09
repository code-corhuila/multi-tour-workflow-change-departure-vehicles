-- State of the change-departure-vehicles saga (ADR-005 E4, ADR-004 D3). No foreign key here: it is in 04_alter.
-- tenant_id is a reference to identity-audit, never a foreign key (Annex J, J.3.4); departure_code and vehicle_ids are references to
-- operations-cost, and the ceilings are those of commercial-reservation: the saga keeps them to resume and to restore the previous ceiling.
-- failure_detail holds only a sanitized error code and message, never a raw error (it can embed connection strings or tokens); it never
-- reaches a response (Annex E). version is raised by every save, so a save from an older copy of a saga is refused and the claim of a stale
-- saga is exclusive.
CREATE TABLE IF NOT EXISTS workflow_change_departure_vehicles.change_departure_vehicles_saga (
    saga_id          uuid CONSTRAINT pk_change_departure_vehicles_saga PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key  text        NOT NULL,
    tenant_id        uuid        NOT NULL,
    departure_code   text        NOT NULL,
    vehicle_ids      uuid[]      NOT NULL,
    previous_ceiling integer,
    new_ceiling      integer,
    saga_status      text        NOT NULL DEFAULT 'RUNNING',
    failed_step      text,
    failure_reason   text,
    failure_detail   text,
    version          bigint      NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_change_departure_vehicles_saga_idempotency_key UNIQUE (tenant_id, idempotency_key),
    CONSTRAINT chk_change_departure_vehicles_saga_idempotency_key CHECK (char_length(idempotency_key) BETWEEN 8 AND 128),
    CONSTRAINT chk_change_departure_vehicles_saga_departure_code CHECK (char_length(departure_code) BETWEEN 1 AND 40),
    CONSTRAINT chk_change_departure_vehicles_saga_vehicle_ids CHECK (cardinality(vehicle_ids) BETWEEN 1 AND 50),
    CONSTRAINT chk_change_departure_vehicles_saga_vehicle_ids_not_null CHECK (array_position(vehicle_ids, NULL) IS NULL),
    CONSTRAINT chk_change_departure_vehicles_saga_ceilings CHECK (coalesce(previous_ceiling, 0) >= 0 AND coalesce(new_ceiling, 0) >= 0),
    CONSTRAINT chk_change_departure_vehicles_saga_status CHECK (saga_status IN ('RUNNING', 'COMPLETED', 'COMPENSATED', 'FAILED')),
    CONSTRAINT chk_change_departure_vehicles_saga_failed_step CHECK (failed_step IN ('LOWER_CEILING', 'APPLY_VEHICLES')),
    CONSTRAINT chk_change_departure_vehicles_saga_failure_reason CHECK (failure_reason IN ('SEATS_HELD', 'VEHICLES_REFUSED', 'PARTICIPANT_UNAVAILABLE', 'COMPENSATION_FAILED')),
    -- a saga that ended undone or failed says which step failed and why; the others have neither
    CONSTRAINT chk_change_departure_vehicles_saga_failure CHECK ((saga_status IN ('COMPENSATED', 'FAILED')) = (failed_step IS NOT NULL)),
    CONSTRAINT chk_change_departure_vehicles_saga_failure_pair CHECK ((failed_step IS NULL) = (failure_reason IS NULL)),
    CONSTRAINT chk_change_departure_vehicles_saga_failure_detail CHECK (char_length(failure_detail) <= 2000)
);

CREATE TABLE IF NOT EXISTS workflow_change_departure_vehicles.change_departure_vehicles_saga_step (
    saga_id        uuid        NOT NULL,
    step_number    smallint    NOT NULL,
    step_name      text        NOT NULL,
    step_status    text        NOT NULL DEFAULT 'COMPLETED',
    completed_at   timestamptz NOT NULL DEFAULT now(),
    compensated_at timestamptz,
    CONSTRAINT pk_change_departure_vehicles_saga_step PRIMARY KEY (saga_id, step_number),
    CONSTRAINT chk_change_departure_vehicles_saga_step_number CHECK (step_number BETWEEN 1 AND 2),
    CONSTRAINT chk_change_departure_vehicles_saga_step_name CHECK (step_name IN ('LOWER_CEILING', 'APPLY_VEHICLES')),
    -- step 1 lowers the ceiling and step 2 applies the vehicles: the number and the name go together
    CONSTRAINT chk_change_departure_vehicles_saga_step_order CHECK ((step_number = 1) = (step_name = 'LOWER_CEILING')),
    CONSTRAINT chk_change_departure_vehicles_saga_step_status CHECK (step_status IN ('COMPLETED', 'COMPENSATED')),
    CONSTRAINT chk_change_departure_vehicles_saga_step_compensated CHECK ((step_status = 'COMPENSATED') = (compensated_at IS NOT NULL))
);
