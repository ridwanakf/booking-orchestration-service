package observability

import "context"

type ctxKey int

const requestIDKey ctxKey = iota

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// RequestIDPtr returns the request id for a column that is null outside a
// request, such as a worker or sweep write.
func RequestIDPtr(ctx context.Context) *string {
	if id := RequestID(ctx); id != "" {
		return &id
	}
	return nil
}
