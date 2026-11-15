# multi-tour-workflow-change-departure-vehicles

> Workflow saga change-departure-vehicles (operations-cost), with compensations

Part of the **Multi Tour** distributed system — team `multi-tour`, Grupo 2.
Governance and documentation live in [`multi-tour-docs`](https://github.com/code-corhuila/multi-tour-docs).

## What it is

The saga that changes the vehicles of a departure (ADR-004 D3, story HU-WFL-005): the Tenant Administrator asks for the whole new set of vehicles of a departure; the saga lowers the ceiling of seats in `commercial-reservation` (refusing if more seats are held than the new ceiling, PDR H8) and then lets `operations-cost` apply the change; if that fails it restores the previous ceiling. Its state is persisted after every step.

## How to start it

Run the migrator once per environment (see `deploy/compose.yml`), then `docker compose -f deploy/compose.yml up workflow-change-departure-vehicles`, with the variables of `.env.example` (the compose file maps the secrets from `env/<environment>.env` of `multi-tour-infra-postgres`) and the `SERVICE_TOKEN` issued by the security service. The service listens on 8090 inside the platform network, under the name `multi-tour-workflow-change-departure-vehicles`, and publishes no port. Entry through the gateway: `POST /api/v1/sagas/change-departure-vehicles` and `GET /api/v1/sagas/change-departure-vehicles/{id}` (see `openapi/openapi.yaml`). A saga left running because a participant gave no clear answer is taken again every minute.

## Where the data is

In PostgreSQL, schema `workflow_change_departure_vehicles` of the single instance (ADR-005 E4); its migrations are in `db/`, with the control table `databasechangelog_workflow_change_departure_vehicles`.

## How to test it

`gofmt -l .` must print nothing, then `go vet ./...` and `go test ./...` (Go 1.26). The tests of the store need `TEST_DATABASE_URL` (a PostgreSQL where the test can create databases) and are skipped without it; the CI sets it. `sh db/tests/db-ci.sh` builds the schema from an empty PostgreSQL, reruns it, rolls it back and rebuilds it (Docker is the only requirement). The same checks run in `ci.yml` on pull requests to `develop` and `qa`.

## What is missing

The routes that the saga calls do not exist yet: the ceiling of a departure in `commercial-reservation` and the preview and the application of a change of vehicles in `operations-cost` (plan D-03 of the spec in `multi-tour-docs`). The saga is tested against doubles that follow them, and has not been run end to end.

## Branching

Three permanent branches. **None of them accepts a direct commit** — you enter through a child
branch and leave through a Pull Request.

```
develop  <--PR--  feat/... fix/... chore/...
qa       <--PR--  qa/...
main     <--PR--  release/...  hotfix/...
```

Promotion happens **by re-application** (`git cherry-pick -x`), never by merging one permanent
branch into another: `merge develop -> qa` and `merge qa -> main` do not exist in this model.

`main` requires **1 approval from `ariel5253`**. On `develop` and `qa` the team sets its own review
rule.

Full policy: `00-governance/branching-policy.md` in `multi-tour-docs`.
