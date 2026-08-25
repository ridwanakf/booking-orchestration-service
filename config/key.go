package config

const (
	keyHTTPAddr        = "HTTP_ADDR"
	keyPostgresDSN     = "POSTGRES_DSN"
	keyTemporalAddress = "TEMPORAL_ADDRESS"
	keyTaskQueue       = "TASK_QUEUE"

	keySupplierID       = "SUPPLIER_ID"
	keySupplierBaseURL  = "SUPPLIER_BASE_URL"
	keySupplierDeadline = "SUPPLIER_DEADLINE"
	keySupplierMock     = "SUPPLIER_MOCK_ENABLED"

	keyCallbackToken   = "CALLBACK_TOKEN"
	keyCallbackBaseURL = "CALLBACK_BASE_URL"

	keyCreateAttempts    = "CREATE_ATTEMPTS"
	keyCreateRetryDelay  = "CREATE_RETRY_DELAY"
	keyParkTimeout       = "PARK_TIMEOUT"
	keyMockTimeoutHold   = "MOCK_TIMEOUT_HOLD"
	keyMockCallbackDelay = "MOCK_CALLBACK_DELAY"

	keySweepInterval          = "SWEEP_INTERVAL"
	keySweepReceivedThreshold = "SWEEP_RECEIVED_THRESHOLD"
	keySweepInFlightThreshold = "SWEEP_INFLIGHT_THRESHOLD"

	keyShutdownTimeout     = "SHUTDOWN_TIMEOUT"
	keyWorkerRetryInterval = "WORKER_RETRY_INTERVAL"

	keySwaggerEnabled = "SWAGGER_ENABLED"
)
