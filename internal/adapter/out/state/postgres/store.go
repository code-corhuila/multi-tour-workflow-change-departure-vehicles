// Package postgres is the saga store over the schema workflow_change_departure_vehicles of the single PostgreSQL instance.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/application/port/out"
	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/domain/saga"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ out.Store = (*Store)(nil)

// Store keeps what the tables hold: ids, the departure, the vehicles, the ceilings, the status, the steps and a short reason code.
// No column holds the text of a participant. Ids go in as text and are cast in SQL, so the driver never parses them.
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const (
	sagaTable = "workflow_change_departure_vehicles.change_departure_vehicles_saga"
	stepTable = "workflow_change_departure_vehicles.change_departure_vehicles_saga_step"

	sagaColumns = `saga_id::text, idempotency_key, tenant_id::text, departure_code, vehicle_ids::text[], previous_ceiling, new_ceiling,
		saga_status, coalesce(failed_step, ''), coalesce(failure_reason, ''), version, created_at, updated_at`
)

// Create inserts the saga as RUNNING and gives it its id. When the tenant already used the key, nothing is inserted and the existing
// saga is returned with created false; the unique key makes two callers with one key take turns, so only one inserts.
func (s *Store) Create(ctx context.Context, sg saga.Saga) (saga.Saga, bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO `+sagaTable+` (idempotency_key, tenant_id, departure_code, vehicle_ids, saga_status, created_at, updated_at)
		VALUES ($1, $2::text::uuid, $3, $4::text[]::uuid[], 'RUNNING', $5, $6)
		ON CONFLICT ON CONSTRAINT uq_change_departure_vehicles_saga_idempotency_key DO NOTHING`,
		sg.IdempotencyKey, sg.TenantID, sg.DepartureCode, sg.VehicleIDs, sg.CreatedAt, sg.UpdatedAt)
	if err != nil {
		return saga.Saga{}, false, err
	}
	found, err := s.load(ctx, `WHERE tenant_id = $1::text::uuid AND idempotency_key = $2`, sg.TenantID, sg.IdempotencyKey)
	if err != nil || len(found) == 0 {
		return saga.Saga{}, false, errors.Join(err, errors.New("the saga of the idempotency key vanished"))
	}
	return found[0], tag.RowsAffected() == 1, nil
}

// Save writes the ceilings, the status, the failure and the steps in one transaction. Only a RUNNING saga is written, and only from
// the copy that is still the stored one (compare and swap on the version): a saga that ended is never overwritten and one moved by
// another instance is refused, both as out.ErrStale. It returns the saga with its new version.
func (s *Store) Save(ctx context.Context, sg saga.Saga) (saga.Saga, error) {
	if !saga.ValidID(sg.ID) {
		return saga.Saga{}, out.ErrStale
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return saga.Saga{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `UPDATE `+sagaTable+` SET saga_status = $2, previous_ceiling = $3, new_ceiling = $4,
		failed_step = nullif($5::text, ''), failure_reason = nullif($6::text, ''), updated_at = $7, version = version + 1
		WHERE saga_id = $1::text::uuid AND saga_status = 'RUNNING' AND version = $8`,
		sg.ID, string(sg.Status), sg.PreviousCeiling, sg.TargetCeiling, string(sg.FailedStep), string(sg.Reason), sg.UpdatedAt, sg.Version)
	if err != nil {
		return saga.Saga{}, err
	}
	if tag.RowsAffected() == 0 {
		return saga.Saga{}, out.ErrStale
	}
	batch := &pgx.Batch{}
	for i, st := range sg.Steps {
		status := "COMPLETED"
		if st.Compensated {
			status = "COMPENSATED"
		}
		batch.Queue(`INSERT INTO `+stepTable+` (saga_id, step_number, step_name, step_status, compensated_at)
			VALUES ($1::text::uuid, $2, $3, $4::text, CASE WHEN $4::text = 'COMPENSATED' THEN now() END)
			ON CONFLICT ON CONSTRAINT pk_change_departure_vehicles_saga_step DO UPDATE SET step_name = EXCLUDED.step_name,
				step_status = EXCLUDED.step_status,
				compensated_at = CASE WHEN EXCLUDED.step_status = 'COMPENSATED'
					THEN coalesce(`+stepTable+`.compensated_at, EXCLUDED.compensated_at) END`,
			sg.ID, int16(i+1), string(st.Name), status)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return saga.Saga{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return saga.Saga{}, err
	}
	sg.Version++
	return sg, nil
}

// Get reads a saga of the tenant; one of another tenant, an unknown id or a text that is not an id is out.ErrNotFound.
func (s *Store) Get(ctx context.Context, tenantID, id string) (saga.Saga, error) {
	if !saga.ValidID(id) || !saga.ValidID(tenantID) {
		return saga.Saga{}, out.ErrNotFound
	}
	found, err := s.load(ctx, `WHERE saga_id = $1::text::uuid AND tenant_id = $2::text::uuid`, id, tenantID)
	if err != nil {
		return saga.Saga{}, err
	}
	if len(found) == 0 {
		return saga.Saga{}, out.ErrNotFound
	}
	return found[0], nil
}

// ListStale returns up to limit RUNNING sagas not updated since before, the one that stopped first at the front. Taking one is the
// Save of the use case, which raises the version: two instances never run the same saga.
func (s *Store) ListStale(ctx context.Context, before time.Time, limit int) ([]saga.Saga, error) {
	if limit <= 0 {
		return nil, nil
	}
	return s.load(ctx, `WHERE saga_status = 'RUNNING' AND updated_at < $1 ORDER BY updated_at, saga_id LIMIT $2`, before, limit)
}

// load reads the sagas the WHERE clause selects and then their steps in step order.
func (s *Store) load(ctx context.Context, where string, args ...any) ([]saga.Saga, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sagaColumns+` FROM `+sagaTable+` `+where, args...)
	if err != nil {
		return nil, err
	}
	sagas, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (saga.Saga, error) {
		var sg saga.Saga
		var status, failed, reason string
		err := r.Scan(&sg.ID, &sg.IdempotencyKey, &sg.TenantID, &sg.DepartureCode, &sg.VehicleIDs, &sg.PreviousCeiling, &sg.TargetCeiling,
			&status, &failed, &reason, &sg.Version, &sg.CreatedAt, &sg.UpdatedAt)
		sg.Status, sg.FailedStep, sg.Reason = saga.Status(status), saga.Step(failed), saga.Reason(reason)
		return sg, err
	})
	if err != nil || len(sagas) == 0 {
		return nil, err
	}
	index := make(map[string]int, len(sagas))
	ids := make([]string, len(sagas))
	for i, sg := range sagas {
		index[sg.ID], ids[i] = i, sg.ID
	}
	steps, err := s.pool.Query(ctx, `SELECT saga_id::text, step_name, step_status = 'COMPENSATED' FROM `+stepTable+`
		WHERE saga_id IN (SELECT unnest($1::text[])::uuid) ORDER BY saga_id, step_number`, ids)
	if err != nil {
		return nil, err
	}
	defer steps.Close()
	for steps.Next() {
		var id, name string
		var compensated bool
		if err := steps.Scan(&id, &name, &compensated); err != nil {
			return nil, err
		}
		sg := &sagas[index[id]]
		sg.Steps = append(sg.Steps, saga.StepState{Name: saga.Step(name), Compensated: compensated})
	}
	return sagas, steps.Err()
}
