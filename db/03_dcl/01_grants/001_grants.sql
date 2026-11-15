GRANT USAGE ON SCHEMA workflow_change_departure_vehicles TO workflow_change_departure_vehicles_reader, workflow_change_departure_vehicles_writer;
GRANT SELECT ON ALL TABLES IN SCHEMA workflow_change_departure_vehicles TO workflow_change_departure_vehicles_reader;
-- No DELETE: a saga is never removed by the service, the state is the audit of the process
GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA workflow_change_departure_vehicles TO workflow_change_departure_vehicles_writer;
ALTER DEFAULT PRIVILEGES IN SCHEMA workflow_change_departure_vehicles GRANT SELECT ON TABLES TO workflow_change_departure_vehicles_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA workflow_change_departure_vehicles GRANT SELECT, INSERT, UPDATE ON TABLES TO workflow_change_departure_vehicles_writer;
GRANT workflow_change_departure_vehicles_writer TO workflow_change_departure_vehicles_app;
