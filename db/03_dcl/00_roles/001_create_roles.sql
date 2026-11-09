-- NOLOGIN roles with no password. The login user workflow_change_departure_vehicles_app is created by multi-tour-infra-postgres
-- from a secret; this repository only grants it the writer role (Annex J, J.7).
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'workflow_change_departure_vehicles_reader') THEN
        CREATE ROLE workflow_change_departure_vehicles_reader NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'workflow_change_departure_vehicles_writer') THEN
        CREATE ROLE workflow_change_departure_vehicles_writer NOLOGIN;
    END IF;
END
$$;
