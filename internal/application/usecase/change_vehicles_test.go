package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/application/port/in"
	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/application/port/out"
	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/application/usecase"
	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/domain/saga"
)

const (
	tenant    = "11111111-1111-4111-8111-111111111111"
	otherTone = "22222222-2222-4222-8222-222222222222"
	vehicle   = "33333333-3333-4333-8333-333333333333"
	code      = "SAL-013"
	departure = "55555555-5555-4555-8555-555555555555"
)

var now = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

type ceilingCall struct {
	Tenant, Departure string
	Ceiling           int
	Key               string
}

// fakeReservations is commercial-reservation: it answers as told and records every call.
type fakeReservations struct {
	calls []ceilingCall
	// answer decides each call by its position (0 is the first)
	answer func(n int, c ceilingCall) (out.CeilingResult, error)
}

func (f *fakeReservations) SetCeiling(_ context.Context, tenantID, departureID string, ceiling int, key string) (out.CeilingResult, error) {
	c := ceilingCall{tenantID, departureID, ceiling, key}
	f.calls = append(f.calls, c)
	if f.answer == nil {
		return out.CeilingResult{Previous: 18, Ceiling: ceiling}, nil
	}
	return f.answer(len(f.calls)-1, c)
}

type applyCall struct {
	Tenant, Code string
	Vehicles     []string
	Key          string
}

// fakeOperations is operations-cost.
type fakeOperations struct {
	preview      out.Preview
	previewErr   error
	applyErr     error
	previews     int
	applies      []applyCall
	previewCalls []string
}

func (f *fakeOperations) PreviewChange(_ context.Context, tenantID, departureCode string, vehicleIDs []string) (out.Preview, error) {
	f.previews++
	f.previewCalls = append(f.previewCalls, tenantID+"/"+departureCode)
	return f.preview, f.previewErr
}

func (f *fakeOperations) ApplyChange(_ context.Context, tenantID, departureCode string, vehicleIDs []string, key string) error {
	f.applies = append(f.applies, applyCall{tenantID, departureCode, vehicleIDs, key})
	return f.applyErr
}

// memStore keeps the sagas in memory with the version rule of the real store.
type memStore struct {
	byID   map[string]saga.Saga
	saves  int
	nextID int
	// listing, when set, is what ListStale answers: the copies another instance may have already changed
	listing []saga.Saga
}

func newStore() *memStore { return &memStore{byID: map[string]saga.Saga{}} }

func (m *memStore) Create(_ context.Context, s saga.Saga) (saga.Saga, bool, error) {
	for _, existing := range m.byID {
		if existing.TenantID == s.TenantID && existing.IdempotencyKey == s.IdempotencyKey {
			return existing, false, nil
		}
	}
	m.nextID++
	s.ID = fmt.Sprintf("saga-%d", m.nextID)
	m.byID[s.ID] = s
	return s, true, nil
}

func (m *memStore) Save(_ context.Context, s saga.Saga) (saga.Saga, error) {
	if m.byID[s.ID].Version != s.Version {
		return saga.Saga{}, out.ErrStale
	}
	s.Version++
	m.byID[s.ID] = s
	m.saves++
	return s, nil
}

func (m *memStore) Get(_ context.Context, tenantID, id string) (saga.Saga, error) {
	if s, ok := m.byID[id]; ok && s.TenantID == tenantID {
		return s, nil
	}
	return saga.Saga{}, out.ErrNotFound
}

func (m *memStore) ListStale(_ context.Context, before time.Time, limit int) ([]saga.Saga, error) {
	if m.listing != nil {
		return m.listing, nil
	}
	var stale []saga.Saga
	for _, s := range m.byID {
		if s.Status == saga.Running && s.UpdatedAt.Before(before) {
			stale = append(stale, s)
		}
	}
	return stale, nil
}

type rig struct {
	res   *fakeReservations
	ops   *fakeOperations
	store *memStore
	uc    *usecase.ChangeVehicles
}

// newRig starts with a departure of 18 seats whose new vehicles give 10.
func newRig() *rig {
	r := &rig{res: &fakeReservations{}, ops: &fakeOperations{preview: out.Preview{CurrentEffectiveCapacity: 18, NewEffectiveCapacity: 10, DepartureID: departure}}, store: newStore()}
	r.uc = usecase.NewChangeVehicles(r.res, r.ops, r.store, func() time.Time { return now }, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	return r
}

func cmd(key string) in.StartCommand {
	return in.StartCommand{TenantID: tenant, IdempotencyKey: key, DepartureCode: code, VehicleIDs: []string{vehicle}}
}

func steps(s saga.Saga) string {
	var names []string
	for _, st := range s.Steps {
		names = append(names, string(st.Name))
	}
	return strings.Join(names, ",")
}

// AC-WF5-01.1
func TestA_ceiling_is_lowered_first_then_the_vehicles_are_changed(t *testing.T) {
	r := newRig()
	s, created, err := r.uc.Start(context.Background(), cmd("key-0000001"))
	if err != nil || !created {
		t.Fatalf("created %v, err %v", created, err)
	}
	if s.Status != saga.Completed || steps(s) != "LOWER_CEILING,APPLY_VEHICLES" {
		t.Fatalf("status %s, steps %s", s.Status, steps(s))
	}
	if len(r.res.calls) != 1 || r.res.calls[0].Ceiling != 10 || len(r.ops.applies) != 1 {
		t.Fatalf("ceiling calls %+v, applies %+v: want the ceiling lowered to 10 once, then one apply", r.res.calls, r.ops.applies)
	}
}

func TestA_a_change_that_adds_seats_does_not_lower_the_ceiling(t *testing.T) {
	r := newRig()
	r.ops.preview = out.Preview{CurrentEffectiveCapacity: 18, NewEffectiveCapacity: 30, DepartureID: departure}
	s, _, err := r.uc.Start(context.Background(), cmd("key-0000001"))
	if err != nil || s.Status != saga.Completed {
		t.Fatalf("status %s, err %v", s.Status, err)
	}
	if len(r.res.calls) != 0 || len(r.ops.applies) != 1 {
		t.Fatalf("the ceiling was called %d times, applies %d: want none and one", len(r.res.calls), len(r.ops.applies))
	}
}

// AC-WF5-01.2
func TestA_more_seats_held_than_the_ceiling_changes_nothing(t *testing.T) {
	r := newRig()
	r.res.answer = func(int, ceilingCall) (out.CeilingResult, error) {
		return out.CeilingResult{}, fmt.Errorf("422 from the participant: %w", out.ErrSeatsHeld)
	}
	s, _, err := r.uc.Start(context.Background(), cmd("key-0000001"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != saga.Compensated || s.FailedStep != saga.LowerCeiling || s.Reason != saga.SeatsHeld {
		t.Fatalf("status %s, failed step %s, reason %s", s.Status, s.FailedStep, s.Reason)
	}
	if len(r.ops.applies) != 0 || len(r.res.calls) != 1 {
		t.Fatalf("applies %d, ceiling calls %d: step 2 must never run and nothing is restored", len(r.ops.applies), len(r.res.calls))
	}
}

// AC-WF5-01.3
func TestA_step_two_failure_restores_the_previous_ceiling(t *testing.T) {
	r := newRig()
	r.ops.applyErr = fmt.Errorf("422: %w", out.ErrRefused)
	s, _, err := r.uc.Start(context.Background(), cmd("key-0000001"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != saga.Compensated || s.FailedStep != saga.ApplyVehicles || s.Reason != saga.VehiclesRefused {
		t.Fatalf("status %s, failed step %s, reason %s", s.Status, s.FailedStep, s.Reason)
	}
	if len(r.res.calls) != 2 || r.res.calls[1].Ceiling != 18 {
		t.Fatalf("ceiling calls %+v: want the second to restore 18", r.res.calls)
	}
	if !s.Steps[0].Compensated {
		t.Fatal("step 1 is not marked compensated")
	}
}

// An answer that is not a clear yes or no (network, 429, 5xx after the attempts of the client) may hide a change that was made: the
// saga does not say "nothing changed" and does not undo what it cannot be sure of. It stays RUNNING, and the resumption asks again
// with the same key, until it gives up after 15 minutes and ends FAILED for a person.
func TestA_an_unclear_answer_in_step_one_leaves_the_saga_running(t *testing.T) {
	r := newRig()
	r.res.answer = func(int, ceilingCall) (out.CeilingResult, error) { return out.CeilingResult{}, errors.New("timeout") }
	s, _, err := r.uc.Start(context.Background(), cmd("key-0000001"))
	if err != nil || s.Status != saga.Running || len(s.Steps) != 0 || len(r.ops.applies) != 0 {
		t.Fatalf("status %s, steps %s, applies %d, err %v", s.Status, steps(s), len(r.ops.applies), err)
	}
}

func TestA_an_unclear_answer_in_step_two_does_not_restore_the_ceiling(t *testing.T) {
	r := newRig()
	r.ops.applyErr = errors.New("connection reset")
	s, _, err := r.uc.Start(context.Background(), cmd("key-0000001"))
	if err != nil || s.Status != saga.Running || steps(s) != "LOWER_CEILING" {
		t.Fatalf("status %s, steps %s, err %v", s.Status, steps(s), err)
	}
	if len(r.res.calls) != 1 {
		t.Fatalf("%d ceiling calls: the change may have been applied, so the ceiling is not raised again", len(r.res.calls))
	}
}

func TestA_an_unclear_saga_is_asked_again_with_the_same_key_and_ends_when_the_participant_answers(t *testing.T) {
	r := newRig()
	r.ops.applyErr = errors.New("connection reset")
	first, _, _ := r.uc.Start(context.Background(), cmd("key-0000001"))
	stored := r.store.byID[first.ID]
	stored.UpdatedAt = now.Add(-time.Hour)
	r.store.byID[first.ID] = stored
	r.ops.applyErr = nil

	if resumed, err := r.uc.Resume(context.Background(), now.Add(-2*time.Minute)); err != nil || resumed != 1 {
		t.Fatalf("resumed %d, err %v", resumed, err)
	}
	if got := r.store.byID[first.ID]; got.Status != saga.Completed || len(r.ops.applies) != 2 || r.ops.applies[0].Key != r.ops.applies[1].Key {
		t.Fatalf("status %s, applies %+v: want the same key twice and a COMPLETED saga", got.Status, r.ops.applies)
	}
}

func TestA_a_saga_that_stays_unclear_for_fifteen_minutes_ends_failed_for_a_person(t *testing.T) {
	for _, stepTwo := range []bool{false, true} {
		r := newRig()
		if stepTwo {
			r.ops.applyErr = errors.New("down")
		} else {
			r.res.answer = func(int, ceilingCall) (out.CeilingResult, error) { return out.CeilingResult{}, errors.New("down") }
		}
		first, _, _ := r.uc.Start(context.Background(), cmd("key-0000001"))
		stored := r.store.byID[first.ID]
		stored.CreatedAt, stored.UpdatedAt = now.Add(-20*time.Minute), now.Add(-20*time.Minute)
		r.store.byID[first.ID] = stored

		r.uc.Resume(context.Background(), now.Add(-2*time.Minute))
		got := r.store.byID[first.ID]
		want := saga.LowerCeiling
		if stepTwo {
			want = saga.ApplyVehicles
		}
		if got.Status != saga.Failed || got.FailedStep != want || got.Reason != saga.ParticipantUnavailable {
			t.Fatalf("step two %v: status %s, failed step %s, reason %s", stepTwo, got.Status, got.FailedStep, got.Reason)
		}
	}
}

// AC-WF5-01.4
func TestA_failed_restore_ends_failed_after_its_attempts(t *testing.T) {
	r := newRig()
	r.ops.applyErr = out.ErrRefused
	r.res.answer = func(n int, c ceilingCall) (out.CeilingResult, error) {
		if n == 0 {
			return out.CeilingResult{Previous: 18, Ceiling: c.Ceiling}, nil
		}
		return out.CeilingResult{}, errors.New("down")
	}
	s, _, _ := r.uc.Start(context.Background(), cmd("key-0000001"))
	if s.Status != saga.Failed || s.Reason != saga.CompensationFailed || s.FailedStep != saga.ApplyVehicles {
		t.Fatalf("status %s, reason %s, failed step %s", s.Status, s.Reason, s.FailedStep)
	}
	if len(r.res.calls) != 1+5 {
		t.Fatalf("%d ceiling calls: want the lowering and five tries to restore", len(r.res.calls))
	}
}

// The preview changes nothing, but an answer that is not a clear yes or no is unclear all the same: the saga stays RUNNING and is asked
// again like in every other step, and ends FAILED for a person after fifteen minutes.
func TestA_an_unclear_preview_leaves_the_saga_running_and_ends_failed_after_fifteen_minutes(t *testing.T) {
	r := newRig()
	r.ops.previewErr = errors.New("connection reset")
	s, _, err := r.uc.Start(context.Background(), cmd("key-0000001"))
	if err != nil || s.Status != saga.Running || len(s.Steps) != 0 || len(r.res.calls) != 0 || len(r.ops.applies) != 0 {
		t.Fatalf("status %s, steps %s, err %v, calls %d/%d", s.Status, steps(s), err, len(r.res.calls), len(r.ops.applies))
	}
	stored := r.store.byID[s.ID]
	stored.CreatedAt, stored.UpdatedAt = now.Add(-20*time.Minute), now.Add(-20*time.Minute)
	r.store.byID[s.ID] = stored

	r.uc.Resume(context.Background(), now.Add(-2*time.Minute))
	got := r.store.byID[s.ID]
	if got.Status != saga.Failed || got.FailedStep != saga.LowerCeiling || got.Reason != saga.ParticipantUnavailable {
		t.Fatalf("status %s, failed step %s, reason %s", got.Status, got.FailedStep, got.Reason)
	}
}

// A refusal of the restore is an answer, not a network failure: trying it five times would only repeat the same refusal.
func TestA_a_definite_refusal_of_the_restore_is_not_retried(t *testing.T) {
	r := newRig()
	r.ops.applyErr = out.ErrRefused
	r.res.answer = func(n int, c ceilingCall) (out.CeilingResult, error) {
		if n == 0 {
			return out.CeilingResult{Previous: 18, Ceiling: c.Ceiling}, nil
		}
		return out.CeilingResult{}, out.ErrRefused
	}
	s, _, _ := r.uc.Start(context.Background(), cmd("key-0000001"))
	if s.Status != saga.Failed || s.Reason != saga.CompensationFailed || s.FailedStep != saga.ApplyVehicles {
		t.Fatalf("status %s, reason %s, failed step %s", s.Status, s.Reason, s.FailedStep)
	}
	if len(r.res.calls) != 2 {
		t.Fatalf("%d ceiling calls: want the lowering and one try to restore, not five", len(r.res.calls))
	}
}

// AC-WF5-01.5
func TestA_a_stopped_saga_resumes_from_its_last_step(t *testing.T) {
	r := newRig()
	previous, target := 18, 10
	stopped := saga.New(tenant, "key-0000001", code, []string{vehicle}, now.Add(-time.Hour))
	stopped.PreviousCeiling, stopped.TargetCeiling = &previous, &target
	stopped.Steps = []saga.StepState{{Name: saga.LowerCeiling}}
	stored, _, _ := r.store.Create(context.Background(), stopped)
	stored.UpdatedAt = now.Add(-time.Hour)
	r.store.byID[stored.ID] = stored

	resumed, err := r.uc.Resume(context.Background(), now.Add(-2*time.Minute))
	if err != nil || resumed != 1 {
		t.Fatalf("resumed %d, err %v", resumed, err)
	}
	got := r.store.byID[stored.ID]
	if got.Status != saga.Completed || steps(got) != "LOWER_CEILING,APPLY_VEHICLES" {
		t.Fatalf("status %s, steps %s", got.Status, steps(got))
	}
	if len(r.res.calls) != 0 || r.ops.previews != 0 || len(r.ops.applies) != 1 {
		t.Fatalf("ceiling calls %d, previews %d, applies %d: step 1 must not be repeated", len(r.res.calls), r.ops.previews, len(r.ops.applies))
	}
}

func TestA_a_saga_claimed_by_another_instance_is_left_alone(t *testing.T) {
	r := newRig()
	stopped := saga.New(tenant, "key-0000001", code, []string{vehicle}, now.Add(-time.Hour))
	stored, _, _ := r.store.Create(context.Background(), stopped)
	stored.UpdatedAt = now.Add(-time.Hour)
	r.store.byID[stored.ID] = stored
	// another instance saved it between the listing and the claim: the listing has the old copy, the store a newer one
	r.store.listing = []saga.Saga{stored}
	other := stored
	other.Version = 7
	r.store.byID[stored.ID] = other

	if resumed, _ := r.uc.Resume(context.Background(), now.Add(-2*time.Minute)); resumed != 0 || len(r.ops.applies) != 0 {
		t.Fatalf("resumed %d, applies %d: a saga that changed under the claim must be skipped", resumed, len(r.ops.applies))
	}
}

// AC-WF5-01.6
func TestA_the_same_key_returns_the_same_saga_and_runs_nothing_again(t *testing.T) {
	r := newRig()
	first, created, _ := r.uc.Start(context.Background(), cmd("key-0000001"))
	second, createdAgain, err := r.uc.Start(context.Background(), cmd("key-0000001"))
	if err != nil || !created || createdAgain || second.ID != first.ID {
		t.Fatalf("created %v then %v, ids %s and %s, err %v", created, createdAgain, first.ID, second.ID, err)
	}
	if len(r.res.calls) != 1 || len(r.ops.applies) != 1 || r.ops.previews != 1 {
		t.Fatalf("calls: ceiling %d, applies %d, previews %d: the repeat must run no step", len(r.res.calls), len(r.ops.applies), r.ops.previews)
	}
}

func TestA_two_tenants_may_use_the_same_key(t *testing.T) {
	r := newRig()
	a, _, _ := r.uc.Start(context.Background(), cmd("key-0000001"))
	other := cmd("key-0000001")
	other.TenantID = otherTone
	b, createdB, _ := r.uc.Start(context.Background(), other)
	if !createdB || a.ID == b.ID {
		t.Fatalf("created %v, ids %s and %s: another tenant has its own saga", createdB, a.ID, b.ID)
	}
}

// AC-WF5-01.8: every call names the tenant of the person and a key derived from the saga and the step.
func TestA_participants_get_the_tenant_and_a_derived_key(t *testing.T) {
	r := newRig()
	r.ops.applyErr = out.ErrRefused
	s, _, _ := r.uc.Start(context.Background(), cmd("key-0000001"))
	wantKeys := []string{s.ID + ":LOWER_CEILING", s.ID + ":LOWER_CEILING:undo"}
	for i, c := range r.res.calls {
		if c.Tenant != tenant || c.Departure != departure || c.Key != wantKeys[i] {
			t.Fatalf("ceiling call %d = %+v, want tenant %s, departure %s, key %s", i, c, tenant, departure, wantKeys[i])
		}
	}
	if a := r.ops.applies[0]; a.Tenant != tenant || a.Key != s.ID+":APPLY_VEHICLES" || len(a.Vehicles) != 1 {
		t.Fatalf("apply call %+v", a)
	}
	if r.ops.previewCalls[0] != tenant+"/"+code {
		t.Fatalf("preview call %v", r.ops.previewCalls)
	}
}

func TestA_an_unknown_departure_is_not_found_and_changes_nothing(t *testing.T) {
	r := newRig()
	r.ops.previewErr = out.ErrNotFound
	s, created, err := r.uc.Start(context.Background(), cmd("key-0000001"))
	if !errors.Is(err, out.ErrNotFound) || !created {
		t.Fatalf("created %v, err %v, want ErrNotFound", created, err)
	}
	if s.Status != saga.Compensated || len(r.res.calls) != 0 || len(r.ops.applies) != 0 {
		t.Fatalf("status %s, ceiling calls %d, applies %d", s.Status, len(r.res.calls), len(r.ops.applies))
	}
}

func TestA_a_change_the_preview_refuses_changes_nothing(t *testing.T) {
	r := newRig()
	r.ops.previewErr = out.ErrRefused
	s, _, err := r.uc.Start(context.Background(), cmd("key-0000001"))
	if err != nil || s.Status != saga.Compensated || s.FailedStep != saga.LowerCeiling || s.Reason != saga.VehiclesRefused {
		t.Fatalf("status %s, failed step %s, reason %s, err %v", s.Status, s.FailedStep, s.Reason, err)
	}
	if len(r.res.calls) != 0 || len(r.ops.applies) != 0 {
		t.Fatal("something was called after the preview refused")
	}
}

// RN-WFL5: the state is saved after every step.
func TestA_state_is_saved_after_each_step(t *testing.T) {
	r := newRig()
	r.uc.Start(context.Background(), cmd("key-0000001"))
	if r.store.saves != 2 {
		t.Fatalf("%d saves: want one after step 1 and one when step 2 ends the saga", r.store.saves)
	}
}

func TestA_get_reads_only_the_saga_of_the_tenant(t *testing.T) {
	r := newRig()
	s, _, _ := r.uc.Start(context.Background(), cmd("key-0000001"))
	if got, err := r.uc.Get(context.Background(), tenant, s.ID); err != nil || got.ID != s.ID {
		t.Fatalf("got %v, err %v", got.ID, err)
	}
	if _, err := r.uc.Get(context.Background(), otherTone, s.ID); !errors.Is(err, out.ErrNotFound) {
		t.Fatalf("err %v, want ErrNotFound for another tenant", err)
	}
}

// The ceiling route takes the id of the departure that the preview gave, and the saga keeps it with the target before the call: a restart
// after a lost answer then asks again with the same value, and does not decide again from a preview that may have changed.
func TestA_the_target_and_the_departure_id_are_saved_before_the_ceiling_is_called(t *testing.T) {
	r := newRig()
	r.res.answer = func(n int, c ceilingCall) (out.CeilingResult, error) {
		saved := r.store.byID["saga-1"]
		if n == 0 && (saved.TargetCeiling == nil || *saved.TargetCeiling != 10 || saved.DepartureID != departure) {
			t.Errorf("at the first call the stored saga has target %v and departure id %q", saved.TargetCeiling, saved.DepartureID)
		}
		return out.CeilingResult{Previous: 18, Ceiling: c.Ceiling}, nil
	}
	s, _, err := r.uc.Start(context.Background(), cmd("key-0000001"))
	if err != nil || s.Status != saga.Completed || s.DepartureID != departure {
		t.Fatalf("status %s, departure id %q, err %v", s.Status, s.DepartureID, err)
	}
}

func TestA_a_lost_answer_is_asked_again_with_the_saved_target_the_same_key_and_no_new_preview(t *testing.T) {
	r := newRig()
	r.res.answer = func(n int, c ceilingCall) (out.CeilingResult, error) {
		if n == 0 {
			return out.CeilingResult{}, errors.New("connection reset") // commercial-reservation may have lowered it
		}
		return out.CeilingResult{Previous: 18, Ceiling: c.Ceiling}, nil
	}
	first, _, _ := r.uc.Start(context.Background(), cmd("key-0000001"))
	stored := r.store.byID[first.ID]
	stored.UpdatedAt = now.Add(-time.Hour)
	r.store.byID[first.ID] = stored
	// the vehicles on the preview have changed in between: the saga must not decide again
	r.ops.preview = out.Preview{CurrentEffectiveCapacity: 18, NewEffectiveCapacity: 6, DepartureID: departure}

	if resumed, err := r.uc.Resume(context.Background(), now.Add(-2*time.Minute)); err != nil || resumed != 1 {
		t.Fatalf("resumed %d, err %v", resumed, err)
	}
	got := r.store.byID[first.ID]
	if got.Status != saga.Completed || r.ops.previews != 1 || len(r.res.calls) != 2 {
		t.Fatalf("status %s, previews %d, ceiling calls %d: want COMPLETED, one preview and two ceiling calls", got.Status, r.ops.previews, len(r.res.calls))
	}
	if a, b := r.res.calls[0], r.res.calls[1]; b.Ceiling != 10 || b.Key != a.Key || b.Departure != departure {
		t.Fatalf("calls %+v then %+v: want the same target 10, key and departure id", a, b)
	}
}
