package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/adapter/out/state/postgres"
	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/adapter/out/state/postgres/pgtest"
	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/application/port/out"
	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/domain/saga"
)

const (
	tenant  = "11111111-1111-4111-8111-111111111111"
	other   = "22222222-2222-4222-8222-222222222222"
	vehicle = "33333333-3333-4333-8333-333333333333"
	second  = "44444444-4444-4444-8444-444444444444"
)

var now = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

func newSaga(tenantID, key string) saga.Saga {
	return saga.New(tenantID, key, "SAL-013", []string{vehicle, second}, now)
}

// RN: the state is saved and read back whole.
func TestI_a_saga_is_created_and_read_back_whole(t *testing.T) {
	store := postgres.NewStore(pgtest.Open(t))
	ctx := context.Background()
	created, isNew, err := store.Create(ctx, newSaga(tenant, "key-0000001"))
	if err != nil || !isNew || created.ID == "" || created.Status != saga.Running || created.Version != 0 {
		t.Fatalf("created %+v, new %v, err %v", created, isNew, err)
	}
	got, err := store.Get(ctx, tenant, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DepartureCode != "SAL-013" || len(got.VehicleIDs) != 2 || got.VehicleIDs[1] != second || got.TenantID != tenant ||
		got.IdempotencyKey != "key-0000001" || !got.CreatedAt.Equal(now) || got.PreviousCeiling != nil || got.TargetCeiling != nil {
		t.Fatalf("read back %+v", got)
	}
}

// AC-WF5-01.6: the same tenant and key give the same saga; another tenant has its own.
func TestI_the_same_tenant_and_key_return_the_same_saga_and_another_tenant_has_its_own(t *testing.T) {
	store := postgres.NewStore(pgtest.Open(t))
	ctx := context.Background()
	first, _, _ := store.Create(ctx, newSaga(tenant, "key-0000001"))
	again, isNew, err := store.Create(ctx, newSaga(tenant, "key-0000001"))
	if err != nil || isNew || again.ID != first.ID {
		t.Fatalf("ids %s and %s, new %v, err %v", first.ID, again.ID, isNew, err)
	}
	otherSaga, isNew, err := store.Create(ctx, newSaga(other, "key-0000001"))
	if err != nil || !isNew || otherSaga.ID == first.ID {
		t.Fatalf("other tenant: id %s, new %v, err %v", otherSaga.ID, isNew, err)
	}
}

// Annex E rule 2: steps, ceilings and the end are persisted.
func TestI_steps_ceilings_and_the_end_are_persisted(t *testing.T) {
	store := postgres.NewStore(pgtest.Open(t))
	ctx := context.Background()
	s, _, _ := store.Create(ctx, newSaga(tenant, "key-0000001"))
	previous, target := 18, 10
	s.PreviousCeiling, s.TargetCeiling = &previous, &target
	s.Done(saga.LowerCeiling, now.Add(time.Second))
	s, err := store.Save(ctx, s)
	if err != nil || s.Version != 1 {
		t.Fatalf("version %d, err %v", s.Version, err)
	}
	s.Undone(saga.LowerCeiling, now.Add(2*time.Second))
	s.End(saga.Compensated, saga.ApplyVehicles, saga.VehiclesRefused, now.Add(3*time.Second))
	if s, err = store.Save(ctx, s); err != nil || s.Version != 2 {
		t.Fatalf("version %d, err %v", s.Version, err)
	}

	got, err := store.Get(ctx, tenant, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != saga.Compensated || got.FailedStep != saga.ApplyVehicles || got.Reason != saga.VehiclesRefused ||
		got.PreviousCeiling == nil || *got.PreviousCeiling != 18 || got.TargetCeiling == nil || *got.TargetCeiling != 10 ||
		len(got.Steps) != 1 || got.Steps[0].Name != saga.LowerCeiling || !got.Steps[0].Compensated || !got.UpdatedAt.Equal(now.Add(3*time.Second)) {
		t.Fatalf("read back %+v", got)
	}
}

// Annex E: a save from an older copy is refused, and a saga that ended is never overwritten.
func TestI_a_save_from_an_older_copy_is_refused_and_an_ended_saga_is_not_overwritten(t *testing.T) {
	store := postgres.NewStore(pgtest.Open(t))
	ctx := context.Background()
	s, _, _ := store.Create(ctx, newSaga(tenant, "key-0000001"))
	older := s
	s.Done(saga.LowerCeiling, now)
	if _, err := store.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ctx, older); !errors.Is(err, out.ErrStale) {
		t.Fatalf("err %v, want ErrStale for the older copy", err)
	}

	ended, _ := store.Get(ctx, tenant, s.ID)
	ended.End(saga.Completed, "", "", now)
	ended, err := store.Save(ctx, ended)
	if err != nil {
		t.Fatal(err)
	}
	ended.End(saga.Failed, saga.ApplyVehicles, saga.CompensationFailed, now)
	if _, err := store.Save(ctx, ended); !errors.Is(err, out.ErrStale) {
		t.Fatalf("err %v, want ErrStale: a COMPLETED saga cannot be rewritten", err)
	}
}

func TestI_get_reads_only_the_saga_of_the_tenant_and_a_bad_id_is_not_found(t *testing.T) {
	store := postgres.NewStore(pgtest.Open(t))
	ctx := context.Background()
	s, _, _ := store.Create(ctx, newSaga(tenant, "key-0000001"))
	if _, err := store.Get(ctx, other, s.ID); !errors.Is(err, out.ErrNotFound) {
		t.Fatalf("another tenant: err %v", err)
	}
	if _, err := store.Get(ctx, tenant, "not-a-uuid"); !errors.Is(err, out.ErrNotFound) {
		t.Fatalf("bad id: err %v", err)
	}
	if _, err := store.Get(ctx, tenant, "99999999-9999-4999-8999-999999999999"); !errors.Is(err, out.ErrNotFound) {
		t.Fatalf("unknown id: err %v", err)
	}
}

// Plan D-06: the resumption looks for RUNNING sagas that stopped advancing.
func TestI_list_stale_returns_only_running_sagas_not_updated_since_the_cutoff_oldest_first(t *testing.T) {
	store := postgres.NewStore(pgtest.Open(t))
	ctx := context.Background()
	old, _, _ := store.Create(ctx, saga.New(tenant, "key-0000001", "SAL-001", []string{vehicle}, now.Add(-time.Hour)))
	older, _, _ := store.Create(ctx, saga.New(tenant, "key-0000002", "SAL-002", []string{vehicle}, now.Add(-2*time.Hour)))
	store.Create(ctx, saga.New(tenant, "key-0000003", "SAL-003", []string{vehicle}, now)) // fresh
	done, _, _ := store.Create(ctx, saga.New(tenant, "key-0000004", "SAL-004", []string{vehicle}, now.Add(-3*time.Hour)))
	done.End(saga.Completed, "", "", now.Add(-3*time.Hour))
	if _, err := store.Save(ctx, done); err != nil {
		t.Fatal(err)
	}

	stale, err := store.ListStale(ctx, now.Add(-2*time.Minute), 10)
	if err != nil || len(stale) != 2 || stale[0].ID != older.ID || stale[1].ID != old.ID {
		t.Fatalf("stale %v, err %v: want the two RUNNING old ones, oldest first", len(stale), err)
	}
	if limited, _ := store.ListStale(ctx, now.Add(-2*time.Minute), 1); len(limited) != 1 {
		t.Fatalf("%d with limit 1", len(limited))
	}
}

// Annex J: the service connects as its own user, which writes the tables but cannot delete from them.
func TestI_the_store_works_as_the_app_user_which_cannot_delete(t *testing.T) {
	pool := pgtest.OpenAsApp(t)
	store := postgres.NewStore(pool)
	ctx := context.Background()
	s, _, err := store.Create(ctx, newSaga(tenant, "key-0000001"))
	if err != nil {
		t.Fatal(err)
	}
	s.Done(saga.LowerCeiling, now)
	if _, err := store.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM change_departure_vehicles_saga`); err == nil {
		t.Fatal("the app user deleted a saga")
	}
}

// The errors of the driver quote the connection string, and it carries the password.
func TestI_open_names_the_kind_of_failure_and_never_the_connection_string(t *testing.T) {
	_, err := postgres.Open(context.Background(), "postgres://workflow_user:s3cret-pass@127.0.0.1:1/db?sslmode=disable",
		postgres.Limits{MaxConns: 2, ConnectTimeout: time.Second, StatementTimeout: time.Second})
	if err == nil {
		t.Fatal("a closed port was reported as open")
	}
	for _, leaked := range []string{"s3cret-pass", "workflow_user", "127.0.0.1"} {
		if contains(err.Error(), leaked) {
			t.Fatalf("the error leaks %q: %v", leaked, err)
		}
	}
	if _, err := postgres.Open(context.Background(), "::not a url::", postgres.Limits{}); err == nil || contains(err.Error(), "::not") {
		t.Fatalf("a bad url: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
