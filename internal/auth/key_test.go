package auth_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/ridwanakf/booking-orchestration-service/internal/auth"
)

type KeySuite struct {
	suite.Suite
}

func TestKey(t *testing.T) {
	suite.Run(t, new(KeySuite))
}

func (s *KeySuite) TestParse() {
	key, err := auth.Parse("bok_demo01_s3cret-value")

	s.Require().NoError(err)
	s.Equal("demo01", key.ID)
	s.Equal("s3cret-value", key.Secret, "a secret containing separators survives intact")
}

func (s *KeySuite) TestParseRejectsMalformedKeys() {
	for name, raw := range map[string]string{
		"wrong prefix":  "key_demo01_secret",
		"missing parts": "bok_demo01",
		"empty id":      "bok__secret",
		"empty secret":  "bok_demo01_",
		"empty string":  "",
	} {
		s.Run(name, func() {
			_, err := auth.Parse(raw)
			s.ErrorIs(err, auth.ErrMalformedKey)
		})
	}
}

// The id half exists so a key can be looked up: a salted hash cannot be
// indexed, so without it authenticating would mean hashing the candidate
// against every key in the table.
func (s *KeySuite) TestHashRoundTrips() {
	encoded, err := auth.Hash("local-demo-secret")

	s.Require().NoError(err)
	s.True(auth.Verify("local-demo-secret", encoded))
	s.False(auth.Verify("wrong-secret", encoded))
}

func (s *KeySuite) TestHashIsSaltedPerKey() {
	first, err := auth.Hash("same-secret")
	s.Require().NoError(err)
	second, err := auth.Hash("same-secret")
	s.Require().NoError(err)

	s.NotEqual(first, second, "two keys with the same secret must not share a digest")
	s.True(auth.Verify("same-secret", first))
	s.True(auth.Verify("same-secret", second))
}

func (s *KeySuite) TestVerifyRejectsGarbage() {
	for name, encoded := range map[string]string{
		"empty":             "",
		"not argon":         "$2y$10$abcdefghijklmnop",
		"truncated":         "$argon2id$v=19$m=65536,t=1,p=4$onlysalt",
		"bad base64":        "$argon2id$v=19$m=65536,t=1,p=4$!!!!$!!!!",
		"missing separator": "$argon2id$v=19$m=65536,t=1,p=4$saltonly",
	} {
		s.Run(name, func() {
			s.False(auth.Verify("anything", encoded))
		})
	}
}
