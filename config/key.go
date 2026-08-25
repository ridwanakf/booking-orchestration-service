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
	keySweepMarkerThreshold   = "SWEEP_MARKER_THRESHOLD"
	keySweepReceivedThreshold = "SWEEP_RECEIVED_THRESHOLD"
	keySweepIdleThreshold     = "SWEEP_IDLE_THRESHOLD"

	keyShutdownTimeout     = "SHUTDOWN_TIMEOUT"
	keyWorkerRetryInterval = "WORKER_RETRY_INTERVAL"

	keySwaggerEnabled = "SWAGGER_ENABLED"
)
