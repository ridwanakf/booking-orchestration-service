package supplier

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// The byte counter sits on the raw connection, and TLS writes its handshake to
// that same socket. The baseline is therefore taken once the connection is
// ready, so handshake bytes are never mistaken for request bytes.
//
// Worth being precise about the blast radius: a dial that fails never completes
// a handshake, so the common not-sent case works either way. The baseline
// matters for a connection that is established and then fails before the
// request is written, and it is the difference between an honest terminal
// answer and a booking parked in doubt that never left.
type TLSSuite struct {
	suite.Suite
	req BookRequest
}

func TestTLS(t *testing.T) {
	suite.Run(t, new(TLSSuite))
}

func (s *TLSSuite) SetupTest() {
	s.req = BookRequest{
		BookingID:      "0198f2c4-6d1a-7c3e-9f4b-2f6f0a1d9b10",
		IdempotencyKey: "0198F2C46D1A7C3E9F4B2F6F0A1D9B10",
		PropertyID:     "hotel-001",
		RoomTypeID:     "room-x",
		CheckIn:        time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		CheckOut:       time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC),
		GuestFirstName: "Taro",
		GuestLastName:  "Yamada",
	}
}

// A client that trusts the test server, otherwise identical to the real one.
func (s *TLSSuite) clientFor(srv *httptest.Server) *HTTPClient {
	c := NewHTTPClient(srv.URL, 2*time.Second)
	transport, ok := c.http.Transport.(*http.Transport)
	s.Require().True(ok)
	transport.TLSClientConfig = &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	return c
}

func (s *TLSSuite) TestHandshakeBytesAreNotCountedAsTheRequest() {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"CONFIRMED","reference":"SUP-1"}`))
	}))
	defer srv.Close()

	got := s.clientFor(srv).Book(context.Background(), s.req)

	s.Equal(OutcomeConfirmed, got.Outcome, "TLS must not break the happy path")
}

// A handshake that completes and a server that then hangs up is AMBIGUOUS, not
// NOT_SENT, and that is correct: the request bytes were written to the socket,
// so we cannot prove the supplier did not receive them. This pins the boundary,
// because the tempting mistake is to treat any failed request as not-sent.
// A certificate the client will not accept: the handshake writes a ClientHello
// and then fails, so bytes reached the socket but the request never did. This
// must be NOT_SENT. Reading the raw counter alone reports AMBIGUOUS, which
// turns a cert expiry or CA rotation into an outage of bookings parked in
// permanent doubt when every one of them could have been retried cleanly.
func (s *TLSSuite) TestAFailedHandshakeIsProvablyNotSent() {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"CONFIRMED","reference":"SUP-1"}`))
	}))
	defer srv.Close()

	// No RootCAs installed, so verification fails after the ClientHello.
	got := NewHTTPClient(srv.URL, 2*time.Second).Book(context.Background(), s.req)

	s.Equal(OutcomeNotSent, got.Outcome,
		"the handshake wrote to the socket; the request did not")
}

func (s *TLSSuite) TestHandshakeThenHangUpIsAmbiguousBecauseBytesLeft() {
	cert, err := tls.X509KeyPair(testCertPEM, testKeyPEM)
	s.Require().NoError(err)

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	s.Require().NoError(err)
	defer func() { _ = listener.Close() }()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Complete the handshake so the client writes its ClientHello and
			// receives ours, then hang up before reading the request.
			if tc, ok := conn.(*tls.Conn); ok {
				_ = tc.HandshakeContext(context.Background())
			}
			_ = conn.Close()
		}
	}()

	client := NewHTTPClient("https://"+listener.Addr().String(), 2*time.Second)
	client.http.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	got := client.Book(context.Background(), s.req)

	s.Equal(OutcomeAmbiguous, got.Outcome,
		"the request reached the socket, so nothing about the supplier is proven")
}

// A throwaway self-signed certificate so the handshake can complete against a
// listener this test controls.
var testCertPEM = []byte(`-----BEGIN CERTIFICATE-----
MIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dNuaTAKBggqhkjOPQQDAjASMRAw
DgYDVQQKEwdBY21lIENvMB4XDTE3MTAyMDE5NDMwNloXDTE4MTAyMDE5NDMwNlow
EjEQMA4GA1UEChMHQWNtZSBDbzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABD0d
7VNhbWvZLWPuj/RtHFjvtJBEwOkhbN/BnnE8rnZR8+sbwnc/KhCk3FhnpHZnQz7B
5aETbbIgmuvewdjvSBSjYzBhMA4GA1UdDwEB/wQEAwICpDATBgNVHSUEDDAKBggr
BgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MCkGA1UdEQQiMCCCDmxvY2FsaG9zdDo1
NDUzgg4xMjcuMC4wLjE6NTQ1MzAKBggqhkjOPQQDAgNIADBFAiEA2zpJEPQyz6/l
Wf86aX6PepsntZv2GYlA5UpabfT2EZICICpJ5h/iI+i341gBmLiAFQOyTDT+/wQc
6MF9+Yw1Yy0t
-----END CERTIFICATE-----`)

var testKeyPEM = []byte(`-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIIrYSSNQFaA2Hwf1duRSxKtLYX5CB04fSeQ6tF1aY/PuoAoGCCqGSM49
AwEHoUQDQgAEPR3tU2Fta9ktY+6P9G0cWO+0kETA6SFs38GecTyudlHz6xvCdz8q
EKTcWGekdmdDPsHloRNtsiCa697B2O9IFA==
-----END EC PRIVATE KEY-----`)
