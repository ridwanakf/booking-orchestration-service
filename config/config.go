package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

var (
	once    sync.Once
	loaded  AppConfig
	loadErr error
)

type loader struct {
	errs []error
}

// Load reads the environment once at startup. A missing secret or an
// unparseable value aborts the process rather than being silently replaced by a
// default, which would move the failure into production behaviour.
func Load() (AppConfig, error) {
	once.Do(func() {
		var l loader
		loaded = AppConfig{
			HTTPAddr:        l.str(keyHTTPAddr, ":8080"),
			PostgresDSN:     l.str(keyPostgresDSN, "postgres://booking:booking@localhost:55432/booking?sslmode=disable"),
			TemporalAddress: l.str(keyTemporalAddress, "localhost:7233"),
			TaskQueue:       l.str(keyTaskQueue, "supplier"),

			SupplierID:       l.str(keySupplierID, "mock-supplier"),
			SupplierBaseURL:  l.str(keySupplierBaseURL, "http://localhost:8080/mock-supplier"),
			SupplierDeadline: l.dur(keySupplierDeadline, 90*time.Second),
			SupplierMock:     l.boolean(keySupplierMock, false),

			CallbackToken:   l.required(keyCallbackToken),
			CallbackBaseURL: l.str(keyCallbackBaseURL, "http://localhost:8080"),

			CreateAttempts: l.integer(keyCreateAttempts, 2),
			ParkTimeout:    l.dur(keyParkTimeout, 24*time.Hour),

			CreateRetryDelay:  l.dur(keyCreateRetryDelay, 30*time.Second),
			MockTimeoutHold:   l.dur(keyMockTimeoutHold, 5*time.Second),
			MockCallbackDelay: l.dur(keyMockCallbackDelay, 3*time.Second),

			SweepInterval:          l.dur(keySweepInterval, 15*time.Second),
			SweepReceivedThreshold: l.dur(keySweepReceivedThreshold, 30*time.Second),
			SweepIdleThreshold:     l.dur(keySweepIdleThreshold, 15*time.Minute),

			WorkerRetryInterval: l.dur(keyWorkerRetryInterval, 5*time.Second),

			SwaggerEnabled: l.boolean(keySwaggerEnabled, false),
		}

		// Force-closing mid-call would manufacture the unknown outcome the whole
		// design exists to avoid, so shutdown must outlast one supplier attempt.
		loaded.ShutdownTimeout = l.dur(keyShutdownTimeout, loaded.SupplierDeadline+15*time.Second)

		// A backstop for a client deadline that fails to fire, so it must
		// outlast the supplier call rather than pre-empt it.
		loaded.ActivityStartToClose = loaded.SupplierDeadline + 15*time.Second

		// How long a persist may keep retrying. It holds an answer the supplier
		// already gave, so it is generous by design.
		loaded.PersistWindow = l.dur(keyPersistWindow, 10*time.Minute)

		// The marker is held from authorization until the outcome persists, so a
		// legitimate hold spans one supplier call plus the whole persist window.
		// Deriving it from the call alone declares healthy bookings wedged.
		loaded.SweepMarkerThreshold = l.dur(keySweepMarkerThreshold,
			loaded.ActivityStartToClose+loaded.PersistWindow+10*loaded.SweepInterval)

		l.validate(loaded)
		loadErr = errors.Join(l.errs...)
	})
	return loaded, loadErr
}

func (l *loader) fail(key, value string, err error) {
	l.errs = append(l.errs, fmt.Errorf("%s=%q: %w", key, value, err))
}

func (l *loader) str(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (l *loader) required(key string) string {
	v := os.Getenv(key)
	if v == "" {
		l.errs = append(l.errs, fmt.Errorf("%s must be set", key))
	}
	return v
}

func (l *loader) dur(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.fail(key, v, err)
		return fallback
	}
	return d
}

func (l *loader) integer(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.fail(key, v, err)
		return fallback
	}
	return n
}

func (l *loader) boolean(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail(key, v, err)
		return fallback
	}
	return b
}

// validate catches values that parse but leave a subsystem silently dead. A
// zero sweep interval panics a background goroutine after a clean boot; a zero
// attempt budget makes the workflow loop unreachable so every booking is
// restarted forever; a shutdown shorter than the supplier deadline manufactures
// the unknown outcome the whole design exists to avoid.
func (l *loader) validate(c AppConfig) {
	positive := map[string]time.Duration{
		"SUPPLIER_DEADLINE":        c.SupplierDeadline,
		"PARK_TIMEOUT":             c.ParkTimeout,
		"CREATE_RETRY_DELAY":       c.CreateRetryDelay,
		"SWEEP_INTERVAL":           c.SweepInterval,
		"SWEEP_MARKER_THRESHOLD":   c.SweepMarkerThreshold,
		"SWEEP_RECEIVED_THRESHOLD": c.SweepReceivedThreshold,
		"SWEEP_IDLE_THRESHOLD":     c.SweepIdleThreshold,
		"SHUTDOWN_TIMEOUT":         c.ShutdownTimeout,
		"PERSIST_WINDOW":           c.PersistWindow,
		"WORKER_RETRY_INTERVAL":    c.WorkerRetryInterval,
	}
	for key, d := range positive {
		if d <= 0 {
			l.errs = append(l.errs, fmt.Errorf("%s must be positive, got %s", key, d))
		}
	}

	if c.CreateAttempts < 1 {
		l.errs = append(l.errs, fmt.Errorf("CREATE_ATTEMPTS must be at least 1, got %d", c.CreateAttempts))
	}
	if c.ShutdownTimeout <= c.SupplierDeadline {
		l.errs = append(l.errs, fmt.Errorf(
			"SHUTDOWN_TIMEOUT (%s) must exceed SUPPLIER_DEADLINE (%s), or a deploy force-closes a call in flight",
			c.ShutdownTimeout, c.SupplierDeadline))
	}
	if c.PersistWindow <= c.ActivityStartToClose {
		l.errs = append(l.errs, fmt.Errorf(
			"PERSIST_WINDOW (%s) must exceed the activity backstop (%s); the server clamps start-to-close down to it silently, so a smaller window makes the persist less durable, not more",
			c.PersistWindow, c.ActivityStartToClose))
	}
	// The margin covers schedule-to-start latency, which is unbounded in
	// principle: the sweep must not clear a marker a live persist still owns.
	if hold := c.ActivityStartToClose + c.PersistWindow + 5*c.SweepInterval; c.SweepMarkerThreshold <= hold {
		l.errs = append(l.errs, fmt.Errorf(
			"SWEEP_MARKER_THRESHOLD (%s) must exceed one supplier call plus the persist window (%s), or the sweep reports healthy bookings as wedged",
			c.SweepMarkerThreshold, hold))
	}
}
