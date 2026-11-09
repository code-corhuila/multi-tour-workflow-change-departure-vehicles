# multi-tour-workflow-change-departure-vehicles

> Workflow saga change-departure-vehicles (operations-cost), with compensations

Part of the **Multi Tour** distributed system — team `multi-tour`, Grupo 2.
Governance and documentation live in [`multi-tour-docs`](https://github.com/code-corhuila/multi-tour-docs).

## What it is

The saga that changes the vehicles of a departure (ADR-004 D3, story HU-WFL-005): the Tenant Administrator asks for the whole new set of vehicles of a departure; the saga lowers the ceiling of seats in `commercial-reservation` (refusing if more seats are held than the new ceiling, PDR H8) and then lets `operations-cost` apply the change; if that fails it restores the previous ceiling. Its state is persisted after every step.

## How it will be started

Entry through the gateway: `POST /api/v1/sagas/change-departure-vehicles` and `GET /api/v1/sagas/change-departure-vehicles/{id}` (see `openapi/openapi.yaml`). The variables are those of `.env.example`.

## Where the data will be

In PostgreSQL, schema `workflow_change_departure_vehicles` of the single instance (ADR-005 E4); its migrations will live in `db/` of this repository.

## How to test it

`gofmt -l .` must print nothing, then `go vet ./...` and `go test ./...` (Go 1.26). The same three run in `ci.yml` on pull requests to `develop` and `qa`, with a PostgreSQL service for `TEST_DATABASE_URL`.

## What is missing

Only the skeleton and the contract exist. The orchestrator, the store, the participants, the entry and the image come in tasks WF5-02 to WF5-06 of the spec (`07-api/specs/operations-cost/multi-tour-workflow-change-departure-vehicles` in `multi-tour-docs`). The routes of `commercial-reservation` and `operations-cost` that it calls do not exist yet.

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
