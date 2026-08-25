package supplier

import (
	"context"
	"net"
	"sync/atomic"
)

type writtenKey struct{}

// countingConn records bytes that reach the socket, as opposed to bytes handed
// to the transport's buffer. The distinction decides whether a booking may
// leave doubt behind, so it has to be measured at the wire.
type countingConn struct {
	net.Conn
	written *atomic.Int64
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.written.Add(int64(n))
	return n, err
}

func withWriteCounter(ctx context.Context) (context.Context, *atomic.Int64) {
	written := new(atomic.Int64)
	return context.WithValue(ctx, writtenKey{}, written), written
}

func dialCounting(dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		written, ok := ctx.Value(writtenKey{}).(*atomic.Int64)
		if !ok {
			return conn, nil
		}
		return &countingConn{Conn: conn, written: written}, nil
	}
}
