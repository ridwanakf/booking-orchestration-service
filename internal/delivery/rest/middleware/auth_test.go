package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/ridwanakf/booking-orchestration-service/internal/auth"
	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/delivery/rest/middleware"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	"github.com/ridwanakf/booking-orchestration-service/internal/observability"
	repomocks "github.com/ridwanakf/booking-orchestration-service/internal/repository/mocks"
)

// This middleware is the only source of tenant identity: it decides the
// distributor half of the uniqueness key that makes idempotency per-tenant, and
// the scope that keeps one distributor from reading another's bookings.
type AuthSuite struct {
	suite.Suite
	ctrl   *gomock.Controller
	keys   *repomocks.MockAPIKeyRepository
	engine *gin.Engine
	seen   string
}

func TestAuth(t *testing.T) {
	suite.Run(t, new(AuthSuite))
}

func (s *AuthSuite) SetupTest() {
	gin.SetMode(gin.TestMode)
	s.ctrl = gomock.NewController(s.T())
	s.keys = repomocks.NewMockAPIKeyRepository(s.ctrl)
	s.seen = ""

	s.engine = gin.New()
	s.engine.GET("/guarded", middleware.Authenticate(s.keys), func(c *gin.Context) {
		s.seen = observability.Distributor(c.Request.Context())
		c.Status(http.StatusOK)
	})
}

func (s *AuthSuite) get(header string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/guarded", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	s.engine.ServeHTTP(rec, req)
	return rec
}

func (s *AuthSuite) storedKey(secret string) *model.APIKey {
	hash, err := auth.Hash(secret)
	s.Require().NoError(err)
	return &model.APIKey{KeyID: "demo01", DistributorID: "distributor-001", SecretHash: hash}
}

func (s *AuthSuite) TestAValidKeyResolvesTheDistributor() {
	s.keys.EXPECT().FindActive(gomock.Any(), "demo01").Return(s.storedKey("right-secret"), nil)

	rec := s.get("Bearer bok_demo01_right-secret")

	s.Equal(http.StatusOK, rec.Code)
	s.Equal("distributor-001", s.seen, "the tenant comes from the credential, never from the payload")
}

func (s *AuthSuite) TestARevokedOrUnknownKeyIsRefused() {
	s.keys.EXPECT().FindActive(gomock.Any(), "demo01").Return(nil, constant.ErrUnauthorized)

	s.Equal(http.StatusUnauthorized, s.get("Bearer bok_demo01_right-secret").Code)
}

func (s *AuthSuite) TestAWrongSecretIsRefusedEvenWhenTheKeyIdExists() {
	s.keys.EXPECT().FindActive(gomock.Any(), "demo01").Return(s.storedKey("right-secret"), nil)

	rec := s.get("Bearer bok_demo01_wrong-secret")

	s.Equal(http.StatusUnauthorized, rec.Code)
	s.Empty(s.seen, "a refused request must never reach the handler")
}

// Malformed credentials are rejected without a lookup, so a caller cannot use
// the endpoint to probe which key ids exist.
func (s *AuthSuite) TestMalformedCredentialsNeverReachTheStore() {
	for name, header := range map[string]string{
		"no header":      "",
		"not bearer":     "Basic abc123",
		"empty bearer":   "Bearer ",
		"wrong prefix":   "Bearer key_demo01_secret",
		"missing secret": "Bearer bok_demo01",
		"empty key id":   "Bearer bok__secret",
	} {
		s.Run(name, func() {
			s.Equal(http.StatusUnauthorized, s.get(header).Code)
		})
	}
}
