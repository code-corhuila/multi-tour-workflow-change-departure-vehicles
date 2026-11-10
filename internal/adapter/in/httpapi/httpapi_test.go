package httpapi_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/golang-jwt/jwt/v5"

	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/adapter/in/httpapi"
	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/application/port/in"
	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/application/port/out"
	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/correlation"
	"github.com/code-corhuila/multi-tour-workflow-change-departure-vehicles/internal/domain/saga"
)

const (
	route    = "/api/v1/sagas/change-departure-vehicles"
	sagaID   = "55555555-5555-4555-8555-555555555555"
	tenantID = "11111111-1111-4111-8111-111111111111"
	vehicle  = "33333333-3333-4333-8333-333333333333"
	goodKey  = "key-0001-abcdef"
	goodBody = `{"departureCode":"SAL-013","vehicleIds":["` + vehicle + `"]}`
	issuer   = "multi-tour-security"
	audience = "multi-tour"
)

var signer = func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
}()

func token(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func claims(mutate func(jwt.MapClaims)) jwt.MapClaims {
	c := jwt.MapClaims{"sub": "ana", "roles": []string{"TENANT_ADMINISTRATOR"}, "tenantId": tenantID, "iss": issuer, "aud": []string{audience},
		"exp": time.Now().Add(time.Hour).Unix()}
	if mutate != nil {
		mutate(c)
	}
	return c
}

type fakeUseCase struct {
	started  saga.Saga
	created  bool
	startErr error
	cmd      in.StartCommand
	startCtx context.Context

	found     saga.Saga
	getErr    error
	getTenant string
}

func (f *fakeUseCase) Start(ctx context.Context, cmd in.StartCommand) (saga.Saga, bool, error) {
	f.cmd, f.startCtx = cmd, ctx
	return f.started, f.created, f.startErr
}

func (f *fakeUseCase) Get(_ context.Context, tenant, _ string) (saga.Saga, error) {
	f.getTenant = tenant
	return f.found, f.getErr
}

func completed() saga.Saga {
	now := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	return saga.Saga{ID: sagaID, TenantID: tenantID, DepartureCode: "SAL-013", Status: saga.Completed, CreatedAt: now, UpdatedAt: now,
		Steps: []saga.StepState{{Name: saga.LowerCeiling}, {Name: saga.ApplyVehicles}}}
}

func newHandler(uc in.ChangeDepartureVehicles, logs *bytes.Buffer) http.Handler {
	return httpapi.New(uc, httpapi.NewVerifier(&signer.PublicKey, issuer, audience), slog.New(slog.NewJSONHandler(logs, nil)))
}

type call struct {
	method, path, contentType, body, bearer, key, corr string
}

func (c call) send(h http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	if c.contentType != "" {
		req.Header.Set("Content-Type", c.contentType)
	}
	if c.key != "" {
		req.Header.Set("Idempotency-Key", c.key)
	}
	if c.corr != "" {
		req.Header.Set("X-Correlation-Id", c.corr)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func post(t *testing.T, body string) call {
	return call{http.MethodPost, route, "application/json", body, token(t, signer, claims(nil)), goodKey, ""}
}

func get(t *testing.T, id string) call {
	return call{method: http.MethodGet, path: route + "/" + id, bearer: token(t, signer, claims(nil))}
}

// Every answer is validated against the published contract, and the last check fails when a response the contract declares was never
// produced: the gate toward qa (ADR-004 D7).
func TestC_every_contract_operation_is_exercised(t *testing.T) {
	ctx := context.Background()
	doc, err := openapi3.NewLoader().LoadFromFile("../../../../openapi/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(ctx); err != nil {
		t.Fatalf("the contract is not valid: %v", err)
	}
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	exercised := map[string]bool{}
	platform := token(t, signer, claims(func(c jwt.MapClaims) { c["roles"] = []string{"PLATFORM_ADMINISTRATOR"} }))

	type scenario struct {
		name string
		c    call
		uc   *fakeUseCase
		want int
	}
	okUC := func(created bool) *fakeUseCase { return &fakeUseCase{started: completed(), created: created, found: completed()} }
	noAuth, forbidden := post(t, goodBody), post(t, goodBody)
	noAuth.bearer, forbidden.bearer = "", platform
	noKey, shortKey, notJSON, unknownField := post(t, goodBody), post(t, goodBody), post(t, `{"departureCode":`), post(t, strings.Replace(goodBody, `"vehicleIds"`, `"extra":1,"vehicleIds"`, 1))
	noKey.key, shortKey.key = "", "short"
	badType := post(t, goodBody)
	badType.contentType = "text/plain"
	getNoAuth, getForbidden := get(t, sagaID), get(t, sagaID)
	getNoAuth.bearer, getForbidden.bearer = "", platform

	scenarios := []scenario{
		{"created", post(t, goodBody), okUC(true), http.StatusCreated},
		{"repeated key", post(t, goodBody), okUC(false), http.StatusOK},
		{"no token", noAuth, okUC(true), http.StatusUnauthorized},
		{"not tenant administrator", forbidden, okUC(true), http.StatusForbidden},
		{"no key", noKey, okUC(true), http.StatusBadRequest},
		{"short key", shortKey, okUC(true), http.StatusBadRequest},
		{"malformed json", notJSON, okUC(true), http.StatusBadRequest},
		{"unknown field", unknownField, okUC(true), http.StatusBadRequest},
		{"wrong content type", badType, okUC(true), http.StatusBadRequest},
		{"unknown departure", post(t, goodBody), &fakeUseCase{started: completed(), created: true, startErr: out.ErrNotFound}, http.StatusNotFound},
		{"use case fails", post(t, goodBody), &fakeUseCase{startErr: errors.New("pq: password authentication failed for host=db-internal")}, http.StatusInternalServerError},
		{"read", get(t, sagaID), okUC(true), http.StatusOK},
		{"read without token", getNoAuth, okUC(true), http.StatusUnauthorized},
		{"read forbidden", getForbidden, okUC(true), http.StatusForbidden},
		{"read unknown", get(t, sagaID), &fakeUseCase{getErr: out.ErrNotFound}, http.StatusNotFound},
		{"read store failure", get(t, sagaID), &fakeUseCase{getErr: errors.New("connection refused host=db-internal")}, http.StatusInternalServerError},
	}
	for _, s := range scenarios {
		var logs bytes.Buffer
		s.c.corr = "gate-1"
		rec := s.c.send(newHandler(s.uc, &logs))
		if rec.Code != s.want {
			t.Errorf("%s: status %d, want %d (%s)", s.name, rec.Code, s.want, rec.Body)
			continue
		}
		if strings.Contains(rec.Body.String(), "db-internal") {
			t.Errorf("%s: the body leaks internal detail: %s", s.name, rec.Body)
		}
		req := httptest.NewRequest(s.c.method, s.c.path, nil)
		found, params, err := router.FindRoute(req)
		if err != nil {
			t.Fatalf("%s %s is not in the contract: %v", s.c.method, s.c.path, err)
		}
		in := &openapi3filter.ResponseValidationInput{
			RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: found},
			Status:                 rec.Code, Header: rec.Header(), Options: &openapi3filter.Options{IncludeResponseStatus: true},
		}
		in.SetBodyBytes(rec.Body.Bytes())
		if err := openapi3filter.ValidateResponse(ctx, in); err != nil {
			t.Errorf("%s: answer against the contract: %v", s.name, err)
		}
		exercised[s.c.method+" "+found.Path+" "+strconv.Itoa(rec.Code)] = true
	}
	for path, item := range doc.Paths.Map() {
		for method, operation := range item.Operations() {
			for status := range operation.Responses.Map() {
				if !exercised[method+" "+path+" "+status] {
					t.Errorf("the response %s of %s %s was never produced", status, method, path)
				}
			}
		}
	}
}

// AC-WF5-01.7: one detail per invalid field, and the saga does not start.
func TestC_a_request_is_refused_for_every_invalid_field(t *testing.T) {
	fields := map[string]string{
		"departureCode empty":     strings.Replace(goodBody, `"SAL-013"`, `""`, 1),
		"departureCode not plain": strings.Replace(goodBody, `"SAL-013"`, `"../x"`, 1),
		"departureCode too long":  strings.Replace(goodBody, `"SAL-013"`, `"`+strings.Repeat("A", 41)+`"`, 1),
		"vehicleIds empty":        `{"departureCode":"SAL-013","vehicleIds":[]}`,
		"vehicleIds not an id":    strings.Replace(goodBody, vehicle, "not-an-id", 1),
		"vehicleIds repeated":     `{"departureCode":"SAL-013","vehicleIds":["` + vehicle + `","` + vehicle + `"]}`,
		"vehicleIds missing":      `{"departureCode":"SAL-013"}`,
	}
	for name, body := range fields {
		uc := &fakeUseCase{started: completed(), created: true}
		rec := post(t, body).send(newHandler(uc, &bytes.Buffer{}))
		field := strings.SplitN(name, " ", 2)[0]
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"field":"`+field+`"`) {
			t.Errorf("%s: status %d, body %s", name, rec.Code, rec.Body)
		}
		if uc.startCtx != nil {
			t.Errorf("%s: the saga must not start on an invalid request", name)
		}
	}
	tooMany := `{"departureCode":"SAL-013","vehicleIds":[` + strings.TrimSuffix(strings.Repeat(`"`+vehicle+`",`, 51), ",") + `]}`
	if rec := post(t, tooMany).send(newHandler(&fakeUseCase{}, &bytes.Buffer{})); rec.Code != http.StatusBadRequest {
		t.Errorf("51 vehicles: status %d", rec.Code)
	}
}

// AC-WF5-01.7: the tenant is the one of the token, never of the body; the command carries the key and the correlation id.
func TestC_the_saga_starts_for_the_tenant_of_the_token_with_its_key_and_correlation_id(t *testing.T) {
	uc := &fakeUseCase{started: completed(), created: true}
	body := `{"departureCode":"SAL-013","vehicleIds":["` + vehicle + `"],"tenantId":"22222222-2222-4222-8222-222222222222"}`
	rec := call{http.MethodPost, route, "application/json", body, token(t, signer, claims(nil)), goodKey, "corr-77"}.send(newHandler(uc, &bytes.Buffer{}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a body with a tenantId must be refused (unknown field), got %d", rec.Code)
	}

	rec = call{http.MethodPost, route, "application/json", goodBody, token(t, signer, claims(nil)), goodKey, "corr-77"}.send(newHandler(uc, &bytes.Buffer{}))
	want := in.StartCommand{TenantID: tenantID, IdempotencyKey: goodKey, DepartureCode: "SAL-013", VehicleIDs: []string{vehicle}}
	if uc.cmd.TenantID != want.TenantID || uc.cmd.IdempotencyKey != want.IdempotencyKey || uc.cmd.DepartureCode != want.DepartureCode ||
		len(uc.cmd.VehicleIDs) != 1 || uc.cmd.VehicleIDs[0] != vehicle {
		t.Errorf("command %+v, want %+v", uc.cmd, want)
	}
	if got := correlation.From(uc.startCtx); got != "corr-77" || rec.Header().Get("X-Correlation-Id") != "corr-77" {
		t.Errorf("context id %q, header %q", got, rec.Header().Get("X-Correlation-Id"))
	}
	if loc := rec.Header().Get("Location"); loc != route+"/"+sagaID {
		t.Errorf("Location %q", loc)
	}
}

// AC-WF5-01.6
func TestC_repeated_start_returns_same_saga_200(t *testing.T) {
	uc := &fakeUseCase{started: completed(), created: false}
	rec := post(t, goodBody).send(newHandler(uc, &bytes.Buffer{}))
	if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" {
		t.Errorf("status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["id"] != sagaID {
		t.Errorf("body %s", rec.Body)
	}
}

// Only a Tenant Administrator, and only with an ordinary token that names its tenant.
func TestC_only_a_tenant_administrator_with_a_tenant_and_an_ordinary_token_gets_in(t *testing.T) {
	cases := map[string]jwt.MapClaims{
		"a customer":         claims(func(c jwt.MapClaims) { c["roles"] = []string{"CUSTOMER"} }),
		"a platform admin":   claims(func(c jwt.MapClaims) { c["roles"] = []string{"PLATFORM_ADMINISTRATOR"} }),
		"no roles":           claims(func(c jwt.MapClaims) { delete(c, "roles") }),
		"no tenant":          claims(func(c jwt.MapClaims) { delete(c, "tenantId") }),
		"a tenant not an id": claims(func(c jwt.MapClaims) { c["tenantId"] = "../x" }),
		"a restricted token": claims(func(c jwt.MapClaims) { c["access"] = "restricted" }),
	}
	for name, cl := range cases {
		c := post(t, goodBody)
		c.bearer = token(t, signer, cl)
		uc := &fakeUseCase{started: completed(), created: true}
		if rec := c.send(newHandler(uc, &bytes.Buffer{})); rec.Code != http.StatusForbidden || uc.startCtx != nil {
			t.Errorf("%s: status %d, started %v", name, rec.Code, uc.startCtx != nil)
		}
	}
	bad := map[string]string{
		"expired":        token(t, signer, claims(func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() })),
		"another issuer": token(t, signer, claims(func(c jwt.MapClaims) { c["iss"] = "someone" })),
		"another key":    token(t, mustKey(), claims(nil)),
		"garbage":        "not.a.token",
	}
	for name, tok := range bad {
		c := post(t, goodBody)
		c.bearer = tok
		if rec := c.send(newHandler(&fakeUseCase{}, &bytes.Buffer{})); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
}

func mustKey() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
}

// A person reads only the sagas of their tenant.
func TestC_a_saga_is_read_for_the_tenant_of_the_token(t *testing.T) {
	uc := &fakeUseCase{found: completed()}
	if rec := get(t, sagaID).send(newHandler(uc, &bytes.Buffer{})); rec.Code != http.StatusOK || uc.getTenant != tenantID {
		t.Errorf("status %d, tenant %q", rec.Code, uc.getTenant)
	}
	if rec := get(t, "not-an-id").send(newHandler(&fakeUseCase{}, &bytes.Buffer{})); rec.Code != http.StatusNotFound {
		t.Errorf("a bad id: status %d", rec.Code)
	}
}

func TestC_a_request_without_a_usable_correlation_id_gets_a_new_one_and_an_error_carries_it_as_trace_id(t *testing.T) {
	for _, corr := range []string{"", "bad id with spaces"} {
		c := get(t, sagaID)
		c.corr = corr
		rec := c.send(newHandler(&fakeUseCase{found: completed()}, &bytes.Buffer{}))
		if got := rec.Header().Get("X-Correlation-Id"); got == "" || got == corr {
			t.Errorf("sent %q, answered %q", corr, got)
		}
	}
	c := get(t, sagaID)
	c.bearer, c.corr = "", "trace-9"
	rec := c.send(newHandler(&fakeUseCase{}, &bytes.Buffer{}))
	var body struct{ Error, Message, TraceID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusUnauthorized || body.Error != "UNAUTHORIZED" || body.TraceID != "trace-9" {
		t.Errorf("status %d, body %s", rec.Code, rec.Body)
	}
}

// The reason is a short code; the answer names the failed step and nothing the participant said.
func TestC_a_compensated_saga_answers_the_failed_step_and_a_reason_code_only(t *testing.T) {
	undone := completed()
	undone.Steps = []saga.StepState{{Name: saga.LowerCeiling, Compensated: true}}
	undone.End(saga.Compensated, saga.ApplyVehicles, saga.VehiclesRefused, undone.UpdatedAt)
	rec := post(t, goodBody).send(newHandler(&fakeUseCase{started: undone, created: true}, &bytes.Buffer{}))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusCreated || body["status"] != "COMPENSATED" || body["failedStep"] != "APPLY_VEHICLES" ||
		body["reason"] != "VEHICLES_REFUSED" || body["departureCode"] != "SAL-013" {
		t.Errorf("status %d, body %s", rec.Code, rec.Body)
	}
	if steps, _ := body["completedSteps"].([]any); len(steps) != 1 || steps[0] != "LOWER_CEILING" {
		t.Errorf("completedSteps %v", body["completedSteps"])
	}
}

// A saga left RUNNING by an unclear answer is answered as such, 201: it goes on by itself.
func TestC_a_saga_that_is_still_running_is_answered_as_running(t *testing.T) {
	running := completed()
	running.Status, running.Steps = saga.Running, []saga.StepState{{Name: saga.LowerCeiling}}
	rec := post(t, goodBody).send(newHandler(&fakeUseCase{started: running, created: true}, &bytes.Buffer{}))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusCreated || body["status"] != "RUNNING" || body["failedStep"] != nil || body["reason"] != nil {
		t.Errorf("status %d, body %s", rec.Code, rec.Body)
	}
}

func TestC_the_cause_of_a_failure_goes_to_the_log_and_never_to_the_answer(t *testing.T) {
	var logs bytes.Buffer
	rec := post(t, goodBody).send(newHandler(&fakeUseCase{startErr: errors.New("dial tcp 10.0.0.9:5432: password=hunter2")}, &logs))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "10.0.0.9") {
		t.Errorf("status %d, body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(logs.String(), "10.0.0.9") {
		t.Error("the cause did not reach the log")
	}
}

func TestC_health_answers_without_a_token(t *testing.T) {
	rec := call{method: http.MethodGet, path: "/health"}.send(newHandler(&fakeUseCase{}, &bytes.Buffer{}))
	if rec.Code != http.StatusOK {
		t.Errorf("status %d", rec.Code)
	}
}
