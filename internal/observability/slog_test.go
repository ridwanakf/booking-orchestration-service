package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/ridwanakf/booking-orchestration-service/internal/observability"
)

type HandlerSuite struct {
	suite.Suite
	buf bytes.Buffer
	log *slog.Logger
	ctx context.Context
}

func TestHandler(t *testing.T) {
	suite.Run(t, new(HandlerSuite))
}

func (s *HandlerSuite) SetupTest() {
	s.buf.Reset()
	s.log = slog.New(observability.NewHandler(slog.NewJSONHandler(&s.buf, nil)))
	s.ctx = observability.WithRequestID(context.Background(), "REQ-123")
}

func (s *HandlerSuite) emitted() map[string]any {
	var out map[string]any
	s.Require().NoError(json.Unmarshal(s.buf.Bytes(), &out))
	return out
}

func (s *HandlerSuite) TestRequestIDStaysTopLevel() {
	cases := []struct {
		name  string
		build func(*slog.Logger) *slog.Logger
	}{
		{"plain", func(l *slog.Logger) *slog.Logger { return l }},
		{"with attrs", func(l *slog.Logger) *slog.Logger { return l.With("booking_id", "b-1") }},
		{"inside a group", func(l *slog.Logger) *slog.Logger { return l.WithGroup("http") }},
		{"group then attrs", func(l *slog.Logger) *slog.Logger { return l.WithGroup("http").With("method", "POST") }},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			s.buf.Reset()
			tc.build(s.log).InfoContext(s.ctx, "hello")
			s.Equal("REQ-123", s.emitted()["request_id"],
				"a query on top-level request_id must match every record")
		})
	}
}

func (s *HandlerSuite) TestGroupedAttributesStayGrouped() {
	s.log.WithGroup("http").With("method", "POST").InfoContext(s.ctx, "hello")

	group, ok := s.emitted()["http"].(map[string]any)
	s.Require().True(ok, "the group itself must survive")
	s.Equal("POST", group["method"])
	s.NotContains(group, "request_id")
}

func (s *HandlerSuite) TestNoRequestIDInContext() {
	s.log.Info("no ctx")
	s.NotContains(s.emitted(), "request_id")
}
