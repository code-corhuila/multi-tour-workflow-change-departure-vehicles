package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// the gateway sends the sagas to the port 8090 of each workflow
	defaultListenAddr   = ":8090"
	defaultStepTimeout  = 5 * time.Second
	defaultStepAttempts = 3
	maxStepTimeout      = 30 * time.Second
	maxStepAttempts     = 5
	// a saga calls the participants at most eight times: the preview, the lowering of the ceiling, the application of the vehicles and
	// five tries to restore the ceiling
	callsPerSaga = 8
	// settling time added to the arithmetic bound: database saves between steps and the waits between attempts
	sagaMargin = 10 * time.Second
	// a saga that stays unclear is asked again after this long without moving, or after the bound if that is longer
	minStale = 2 * time.Minute

	defaultIssuer   = "multi-tour-security"
	defaultAudience = "multi-tour"
)

type config struct {
	listenAddr             string
	reservationsURL        string
	operationsURL          string
	serviceToken           string
	publicKeyFile          string
	publicKey              string
	jwtIssuer, jwtAudience string
	databaseURL            string
	stepTimeout            time.Duration
	stepAttempts           int
}

// loadConfig reads the variables of .env.example. Every refusal names the variable and never its value: the token and the connection
// string are secrets. The issuer and the audience are never empty: the library skips the check when they are.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		listenAddr:      valueOr(getenv("LISTEN_ADDR"), defaultListenAddr),
		reservationsURL: getenv("COMMERCIAL_RESERVATION_BASE_URL"),
		operationsURL:   getenv("OPERATIONS_COST_BASE_URL"),
		serviceToken:    getenv("SERVICE_TOKEN"),
		publicKeyFile:   getenv("JWT_PUBLIC_KEY_FILE"),
		publicKey:       getenv("JWT_PUBLIC_KEY"),
		jwtIssuer:       valueOr(getenv("JWT_ISSUER"), defaultIssuer),
		jwtAudience:     valueOr(getenv("JWT_AUDIENCE"), defaultAudience),
		databaseURL:     getenv("DATABASE_URL"),
		stepTimeout:     defaultStepTimeout,
		stepAttempts:    defaultStepAttempts,
	}
	var problems []error
	refuse := func(name, why string) { problems = append(problems, fmt.Errorf("%s %s", name, why)) }

	for _, check := range []struct{ name, value string }{{"COMMERCIAL_RESERVATION_BASE_URL", cfg.reservationsURL}, {"OPERATIONS_COST_BASE_URL", cfg.operationsURL}} {
		if u, err := url.Parse(check.value); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			refuse(check.name, "must be an http or https URL")
		}
	}
	if cfg.serviceToken == "" {
		refuse("SERVICE_TOKEN", "is required")
	}
	if cfg.databaseURL == "" {
		refuse("DATABASE_URL", "is required")
	}
	if cfg.publicKeyFile == "" && cfg.publicKey == "" {
		refuse("JWT_PUBLIC_KEY", "or JWT_PUBLIC_KEY_FILE is required")
	}
	if raw := getenv("STEP_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < time.Second || d > maxStepTimeout {
			refuse("STEP_TIMEOUT", "must be a duration from 1s to 30s")
		} else {
			cfg.stepTimeout = d
		}
	}
	if raw := getenv("STEP_ATTEMPTS"); raw != "" {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < 1 || n > maxStepAttempts {
			refuse("STEP_ATTEMPTS", "must be a number from 1 to 5")
		} else {
			cfg.stepAttempts = n
		}
	}
	return cfg, errors.Join(problems...)
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// sagaBound is the longest a started saga may run: every call using every attempt, plus a margin.
func sagaBound(cfg config) time.Duration {
	return time.Duration(callsPerSaga*cfg.stepAttempts)*cfg.stepTimeout + sagaMargin
}

// staleAfter is how long a RUNNING saga may stand still before the resumption takes it: never less than a live saga can take.
func staleAfter(bound time.Duration) time.Duration { return max(minStale, bound) }

// shutdownGrace is how long a stop waits for the sagas still running: the saga bound plus a little, so the pool is not closed under a
// saga that is half way through a step. The container stop period must be longer.
func shutdownGrace(bound time.Duration) time.Duration { return bound + 5*time.Second }

// newServer declares every limit (Annex C). A saga runs inside the request that starts it, so the write timeout is the saga bound plus a
// little: a shorter one would cut the answer of a saga that is still correct.
func newServer(cfg config, handler http.Handler, bound time.Duration) *http.Server {
	return &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      bound + 5*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}
