package participants_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/adapter/out/participants"
	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/application/port/out"
	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/correlation"
)

const (
	tenant    = "11111111-1111-4111-8111-111111111111"
	vehicle   = "33333333-3333-4333-8333-333333333333"
	departure = "55555555-5555-4555-8555-555555555555"
	token     = "service-token-value"
)

var cfg = participants.Config{Token: token, Timeout: time.Second, Attempts: 3}

// seen is what a participant received.
type seen struct {
	Method, Path, Key, Auth, Correlation, ContentType string
	Body                                              map[string]any
}

// server answers each request with the next of the given statuses/bodies (the last repeats) and records what it received.
type server struct {
	*httptest.Server
	mu       sync.Mutex
	requests []seen
}

type reply struct {
	status int
	body   string
}

func newServer(t *testing.T, replies ...reply) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		n := len(s.requests)
		s.requests = append(s.requests, seen{r.Method, r.URL.EscapedPath(), r.Header.Get("Idempotency-Key"), r.Header.Get("Authorization"),
			r.Header.Get("X-Correlation-Id"), r.Header.Get("Content-Type"), body})
		s.mu.Unlock()
		rep := replies[min(n, len(replies)-1)]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		_, _ = w.Write([]byte(rep.body))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.requests) }

const ceilingOK = `{"previousCeiling":18,"ceiling":10,"seatsHeld":7}`

// AC-WF5-01.8
func TestC_participants_receive_the_service_token_the_tenant_and_a_derived_key(t *testing.T) {
	srv := newServer(t, reply{200, ceilingOK})
	ctx := correlation.With(context.Background(), "c0ffee00-0000-4000-8000-000000000001")
	result, err := participants.NewReservations(srv.URL, cfg).SetCeiling(ctx, tenant, departure, 10, "saga-1:LOWER_CEILING")
	if err != nil || result.Previous != 18 || result.Ceiling != 10 || result.SeatsHeld != 7 {
		t.Fatalf("result %+v, err %v", result, err)
	}
	got := srv.requests[0]
	if got.Method != "PUT" || got.Path != "/api/v1/commercial-reservation/internal/tenants/"+tenant+"/departures/"+departure+"/capacity-ceiling" ||
		got.Auth != "Bearer "+token || got.Key != "saga-1:LOWER_CEILING" || got.Correlation != "c0ffee00-0000-4000-8000-000000000001" ||
		!strings.HasPrefix(got.ContentType, "application/json") || got.Body["ceiling"] != float64(10) {
		t.Fatalf("request %+v", got)
	}
}

func TestC_operations_receives_the_preview_and_the_whole_new_set_of_vehicles(t *testing.T) {
	srv := newServer(t, reply{200, `{"currentEffectiveCapacity":18,"newEffectiveCapacity":10,"departureId":"55555555-5555-4555-8555-555555555555"}`}, reply{200, `{"departureCode":"SAL-013"}`})
	ops := participants.NewOperations(srv.URL, cfg)
	preview, err := ops.PreviewChange(context.Background(), tenant, "SAL-013", []string{vehicle})
	if err != nil || preview.CurrentEffectiveCapacity != 18 || preview.NewEffectiveCapacity != 10 || preview.DepartureID != departure {
		t.Fatalf("preview %+v, err %v", preview, err)
	}
	if err := ops.ApplyChange(context.Background(), tenant, "SAL-013", []string{vehicle}, "saga-1:APPLY_VEHICLES"); err != nil {
		t.Fatal(err)
	}
	p, a := srv.requests[0], srv.requests[1]
	base := "/api/v1/operations-cost/internal/tenants/" + tenant + "/departures/SAL-013"
	if p.Method != "POST" || p.Path != base+"/vehicle-changes/preview" || p.Auth != "Bearer "+token {
		t.Fatalf("preview request %+v", p)
	}
	if a.Method != "PUT" || a.Path != base+"/vehicles" || a.Key != "saga-1:APPLY_VEHICLES" || a.Auth != "Bearer "+token {
		t.Fatalf("apply request %+v", a)
	}
	for _, r := range []seen{p, a} {
		if ids, _ := r.Body["vehicleIds"].([]any); len(ids) != 1 || ids[0] != vehicle {
			t.Fatalf("body %+v", r.Body)
		}
	}
}

// The code of a departure is text the person typed: it cannot climb out of its path, so a code that is not plain is refused before any call.
func TestC_the_departure_code_cannot_change_the_path(t *testing.T) {
	srv := newServer(t, reply{200, ceilingOK})
	for _, code := range []string{"../x", "a/b", "x?y=1", "x#z", "x y", "", strings.Repeat("A", 41), "%2e%2e"} {
		_, err := participants.NewReservations(srv.URL, cfg).SetCeiling(context.Background(), tenant, code, 10, "saga-1:LOWER_CEILING")
		if !errors.Is(err, out.ErrRefused) {
			t.Fatalf("code %q: err %v, want ErrRefused", code, err)
		}
		if err := participants.NewOperations(srv.URL, cfg).ApplyChange(context.Background(), tenant, code, []string{vehicle}, "k-1"); !errors.Is(err, out.ErrRefused) {
			t.Fatalf("code %q on operations: err %v", code, err)
		}
	}
	if srv.count() != 0 {
		t.Fatalf("%d calls were made with a code that is not plain", srv.count())
	}
}

// The saga tells these answers apart (plan D-03): the owners' own words are reduced to a definite sentinel.
func TestC_definite_answers_become_the_sentinels_of_the_port(t *testing.T) {
	cases := []struct {
		name string
		rep  reply
		want error
	}{
		{"seats held", reply{422, `{"error":"BUSINESS_RULE_VIOLATION","message":"seatsHeld 12 is more than 10","traceId":"t"}`}, out.ErrSeatsHeld},
		{"another 422", reply{422, `{"error":"INVALID_STATUS_TRANSITION","message":"x","traceId":"t"}`}, out.ErrRefused},
		{"not found", reply{404, `{"error":"NOT_FOUND","message":"x","traceId":"t"}`}, out.ErrNotFound},
		{"bad request", reply{400, `{"error":"VALIDATION_ERROR","message":"x","traceId":"t"}`}, out.ErrRefused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, tc.rep)
			_, err := participants.NewReservations(srv.URL, cfg).SetCeiling(context.Background(), tenant, departure, 10, "k-1")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
			if srv.count() != 1 {
				t.Fatalf("%d requests: a clear answer is not repeated", srv.count())
			}
		})
	}
}

// A 401 or 403 is the token of the saga, a fault of configuration and not a decision about the change: it says nothing about the work, so
// it is unclear and the saga is asked again after the token is fixed, instead of being reported as refused.
func TestC_a_refused_token_is_an_unclear_answer_not_a_refusal(t *testing.T) {
	for _, status := range []int{401, 403} {
		srv := newServer(t, reply{status, `{"error":"FORBIDDEN","message":"x","traceId":"t"}`})
		_, err := participants.NewReservations(srv.URL, cfg).SetCeiling(context.Background(), tenant, departure, 10, "k-1")
		if err == nil || errors.Is(err, out.ErrRefused) || errors.Is(err, out.ErrNotFound) || errors.Is(err, out.ErrSeatsHeld) {
			t.Fatalf("status %d: err %v, want an unclear answer", status, err)
		}
	}
}

// The tenant comes from the token, but it goes into a path: a value that is not an id is refused before any call.
func TestC_a_tenant_that_is_not_an_id_is_refused_before_any_call(t *testing.T) {
	srv := newServer(t, reply{200, ceilingOK})
	for _, bad := range []string{"", "..", "a/b", "not-a-uuid"} {
		if _, err := participants.NewReservations(srv.URL, cfg).SetCeiling(context.Background(), bad, "SAL-013", 10, "k-1"); !errors.Is(err, out.ErrRefused) {
			t.Fatalf("tenant %q: err %v", bad, err)
		}
	}
	if srv.count() != 0 {
		t.Fatalf("%d calls", srv.count())
	}
}

// Only a 422 is the seats rule of commercial-reservation; operations-cost refuses a change with a 422 of its own.
func TestC_a_422_of_operations_is_a_refusal_of_the_change_and_a_404_an_unknown_departure(t *testing.T) {
	srv := newServer(t, reply{422, `{"error":"BUSINESS_RULE_VIOLATION","message":"overlap","traceId":"t"}`}, reply{404, `{"error":"NOT_FOUND","message":"x","traceId":"t"}`})
	ops := participants.NewOperations(srv.URL, cfg)
	if err := ops.ApplyChange(context.Background(), tenant, "SAL-013", []string{vehicle}, "k-1"); !errors.Is(err, out.ErrRefused) || errors.Is(err, out.ErrSeatsHeld) {
		t.Fatalf("422: err %v", err)
	}
	if _, err := ops.PreviewChange(context.Background(), tenant, "SAL-013", []string{vehicle}); !errors.Is(err, out.ErrNotFound) {
		t.Fatalf("404: err %v", err)
	}
}

// ADR-004 D10: network errors, 429 and 5xx are repeated with the same key; nothing else is.
func TestC_transient_answers_are_repeated_with_the_same_key_and_then_succeed(t *testing.T) {
	for _, status := range []int{500, 502, 503, 429} {
		srv := newServer(t, reply{status, `{}`}, reply{200, ceilingOK})
		result, err := participants.NewReservations(srv.URL, cfg).SetCeiling(context.Background(), tenant, departure, 10, "same-key-1")
		if err != nil || result.Previous != 18 || srv.count() != 2 || srv.requests[0].Key != srv.requests[1].Key {
			t.Fatalf("status %d: result %+v, err %v, requests %d", status, result, err, srv.count())
		}
	}
}

// An answer that stays unclear after the attempts is an error that is none of the sentinels: the saga does not read it as a refusal.
func TestC_an_answer_that_stays_unclear_is_not_a_refusal_and_leaks_nothing(t *testing.T) {
	srv := newServer(t, reply{503, `{"error":"X","message":"jdbc:postgresql://db:5432 password=hunter2"}`})
	_, err := participants.NewReservations(srv.URL, cfg).SetCeiling(context.Background(), tenant, departure, 10, "k-1")
	if err == nil || errors.Is(err, out.ErrSeatsHeld) || errors.Is(err, out.ErrRefused) || errors.Is(err, out.ErrNotFound) {
		t.Fatalf("err %v: want an unclear error", err)
	}
	if srv.count() != 3 || strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "jdbc") || strings.Contains(err.Error(), token) {
		t.Fatalf("%d requests, err %q", srv.count(), err.Error())
	}
}

func TestC_a_server_that_is_down_is_an_unclear_answer(t *testing.T) {
	srv := newServer(t, reply{200, ceilingOK})
	url := srv.URL
	srv.Close()
	_, err := participants.NewReservations(url, cfg).SetCeiling(context.Background(), tenant, departure, 10, "k-1")
	if err == nil || errors.Is(err, out.ErrRefused) || errors.Is(err, out.ErrNotFound) || errors.Is(err, out.ErrSeatsHeld) {
		t.Fatalf("err %v", err)
	}
}

// A redirect would send the credential elsewhere: it is not followed, and it is an unclear answer.
func TestC_a_redirect_is_not_followed(t *testing.T) {
	elsewhere := newServer(t, reply{200, ceilingOK})
	srv := newServer(t, reply{307, ``})
	srv.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/x", http.StatusTemporaryRedirect)
	})
	_, err := participants.NewReservations(srv.URL, cfg).SetCeiling(context.Background(), tenant, departure, 10, "k-1")
	if err == nil || elsewhere.count() != 0 {
		t.Fatalf("err %v, requests that reached the other host: %d", err, elsewhere.count())
	}
}

func TestC_a_cancelled_context_stops_the_attempts(t *testing.T) {
	srv := newServer(t, reply{503, `{}`})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := participants.NewReservations(srv.URL, cfg).SetCeiling(ctx, tenant, departure, 10, "k-1")
	if err == nil || srv.count() > 1 {
		t.Fatalf("err %v, %d requests", err, srv.count())
	}
}

// An accepted answer that has not the fields the saga needs is not a yes.
func TestC_an_accepted_answer_without_its_fields_is_unclear(t *testing.T) {
	srv := newServer(t, reply{200, `{"hello":"world"}`})
	if _, err := participants.NewReservations(srv.URL, cfg).SetCeiling(context.Background(), tenant, departure, 10, "k-1"); err == nil ||
		errors.Is(err, out.ErrRefused) {
		t.Fatalf("err %v", err)
	}
	if _, err := participants.NewOperations(srv.URL, cfg).PreviewChange(context.Background(), tenant, "SAL-013", []string{vehicle}); err == nil {
		t.Fatal("a preview without its fields was accepted")
	}
}

// The ceiling route of commercial-reservation takes the salidaId: a code like SAL-013 would be a 404 there, so it is refused before any call.
func TestC_the_ceiling_route_takes_the_departure_id_and_refuses_a_code(t *testing.T) {
	srv := newServer(t, reply{200, ceilingOK})
	if _, err := participants.NewReservations(srv.URL, cfg).SetCeiling(context.Background(), tenant, "SAL-013", 10, "k-1"); !errors.Is(err, out.ErrRefused) {
		t.Fatalf("err %v, want ErrRefused for a code", err)
	}
	if srv.count() != 0 {
		t.Fatalf("%d calls were made with a code", srv.count())
	}
}

// The preview is where the saga learns the id: an answer that has no valid one is not a yes, so nothing is called with a guess.
func TestC_a_preview_without_a_valid_departure_id_is_unclear(t *testing.T) {
	for _, body := range []string{`{"currentEffectiveCapacity":18,"newEffectiveCapacity":10}`,
		`{"currentEffectiveCapacity":18,"newEffectiveCapacity":10,"departureId":"SAL-013"}`,
		`{"currentEffectiveCapacity":18,"newEffectiveCapacity":10,"departureId":null}`} {
		srv := newServer(t, reply{200, body})
		_, err := participants.NewOperations(srv.URL, cfg).PreviewChange(context.Background(), tenant, "SAL-013", []string{vehicle})
		if err == nil || errors.Is(err, out.ErrRefused) || errors.Is(err, out.ErrNotFound) {
			t.Fatalf("body %s: err %v, want an unclear answer", body, err)
		}
	}
}
