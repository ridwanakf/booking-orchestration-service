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

const distributorKey ctxKey = iota + 1

// WithDistributor carries the authenticated tenant, which is the only source of
// distributor identity once a request is past the middleware.
func WithDistributor(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, distributorKey, id)
}

func Distributor(ctx context.Context) string {
	id, _ := ctx.Value(distributorKey).(string)
	return id
}
