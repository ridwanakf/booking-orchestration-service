package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Keys carry their own lookup id because a salted hash cannot be indexed:
// without the id half, authenticating would mean hashing the candidate against
// every key in the table.
//
//	bok_<key_id>_<secret>
const (
	prefix       = "bok"
	parts        = 3
	saltBytes    = 16
	hashBytes    = 32
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
)

var ErrMalformedKey = errors.New("malformed api key")

type Key struct {
	ID     string
	Secret string
}

func Parse(raw string) (Key, error) {
	segments := strings.SplitN(raw, "_", parts)
	if len(segments) != parts || segments[0] != prefix || segments[1] == "" || segments[2] == "" {
		return Key{}, ErrMalformedKey
	}
	return Key{ID: segments[1], Secret: segments[2]}, nil
}

// Hash encodes the parameters alongside the digest, so the cost can be raised
// later without invalidating keys already issued.
func Hash(secret string) (string, error) {
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	digest := argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, hashBytes)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest)), nil
}

func Verify(secret, encoded string) bool {
	var memory, time uint32
	var threads uint8
	var saltB64, hashB64 string

	if _, err := fmt.Sscanf(encoded, "$argon2id$v=19$m=%d,t=%d,p=%d$%s",
		&memory, &time, &threads, &saltB64); err != nil {
		return false
	}
	if idx := strings.LastIndex(saltB64, "$"); idx >= 0 {
		saltB64, hashB64 = saltB64[:idx], saltB64[idx+1:]
	} else {
		return false
	}

	salt, err := base64.RawStdEncoding.DecodeString(saltB64)
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(hashB64)
	if err != nil {
		return false
	}

	got := argon2.IDKey([]byte(secret), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
