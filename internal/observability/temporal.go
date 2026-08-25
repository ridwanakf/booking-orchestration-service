package observability

import (
	"log/slog"

	"go.temporal.io/sdk/log"
)

// Without this the SDK installs its own text logger and its output, which the
// worker will dominate, would not be JSON.
type temporalLogger struct {
	base *slog.Logger
}

func NewTemporalLogger(base *slog.Logger) log.Logger {
	return &temporalLogger{base: base}
}

func (t *temporalLogger) Debug(msg string, kv ...any) { t.base.Debug(msg, kv...) }
func (t *temporalLogger) Info(msg string, kv ...any)  { t.base.Info(msg, kv...) }
func (t *temporalLogger) Warn(msg string, kv ...any)  { t.base.Warn(msg, kv...) }
func (t *temporalLogger) Error(msg string, kv ...any) { t.base.Error(msg, kv...) }
