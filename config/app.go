package config

import "time"

type AppConfig struct {
	HTTPAddr        string
	PostgresDSN     string
	TemporalAddress string
	TaskQueue       string

	SupplierID       string
	SupplierBaseURL  string
	SupplierDeadline time.Duration
	SupplierMock     bool

	CallbackToken   string
	CallbackBaseURL string

	CreateAttempts       int
	ParkTimeout          time.Duration
	CreateRetryDelay     time.Duration
	ActivityStartToClose time.Duration
	MockTimeoutHold      time.Duration
	MockCallbackDelay    time.Duration

	SweepInterval          time.Duration
	SweepReceivedThreshold time.Duration
	SweepInFlightThreshold time.Duration

	ShutdownTimeout     time.Duration
	WorkerRetryInterval time.Duration

	SwaggerEnabled bool
}
