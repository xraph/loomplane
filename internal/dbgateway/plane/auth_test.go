package plane

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

var authSalt = []byte("0123456789abcdef")

// authPipe returns a gateway-side Backend and a client-side Frontend joined by
// net.Pipe, plus the gateway end of the pipe so a test can close it.
func authPipe(t *testing.T) (*pgproto3.Backend, *pgproto3.Frontend, net.Conn) {
	t.Helper()
	srv, cli := net.Pipe()
	t.Cleanup(func() {
		srv.Close()
		cli.Close()
	})
	_ = srv.SetDeadline(time.Now().Add(5 * time.Second))
	_ = cli.SetDeadline(time.Now().Add(5 * time.Second))
	return pgproto3.NewBackend(srv, srv), pgproto3.NewFrontend(cli, cli), srv
}

// serveAuth stands in for the session code that surrounds authentication. It
// reads the StartupMessage, runs auth, and answers with ReadyForQuery or a
// 28P01 error so that a pgconn client returns from Connect either way.
func serveAuth(srv net.Conn, auth func(be *pgproto3.Backend) error) <-chan error {
	done := make(chan error, 1)
	go func() {
		be := pgproto3.NewBackend(srv, srv)
		if _, err := be.ReceiveStartupMessage(); err != nil {
			done <- err
			return
		}
		err := auth(be)
		if err != nil {
			be.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: "28P01", Message: "password authentication failed"})
		} else {
			be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		}
		if ferr := be.Flush(); ferr != nil && err == nil {
			err = ferr
		}
		done <- err
		// Swallow the client's Terminate so its Close does not block on the pipe.
		_, _ = io.Copy(io.Discard, srv)
	}()
	return done
}

func connectPgconn(t *testing.T, cli net.Conn, password string) error {
	t.Helper()
	cfg, err := pgconn.ParseConfig("host=127.0.0.1 user=app dbname=app sslmode=disable password=" + password)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.DialFunc = func(context.Context, string, string) (net.Conn, error) { return cli, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	return conn.Close(ctx)
}

func TestSCRAMWithPgconnClient(t *testing.T) {
	v := NewVerifier("app", "pw", authSalt, 4096)

	t.Run("right password", func(t *testing.T) {
		srv, cli := net.Pipe()
		defer srv.Close()
		done := serveAuth(srv, func(be *pgproto3.Backend) error { return AuthenticateSCRAM(be, v) })
		if err := connectPgconn(t, cli, "pw"); err != nil {
			t.Fatalf("connect: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatalf("AuthenticateSCRAM: %v", err)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		srv, cli := net.Pipe()
		defer srv.Close()
		done := serveAuth(srv, func(be *pgproto3.Backend) error { return AuthenticateSCRAM(be, v) })
		err := connectPgconn(t, cli, "nope")
		if err == nil {
			t.Fatal("connect succeeded with the wrong password")
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "28P01" {
			t.Fatalf("connect error = %v, want SQLSTATE 28P01", err)
		}
		if got := <-done; !errors.Is(got, ErrAuth) {
			t.Fatalf("AuthenticateSCRAM = %v, want ErrAuth", got)
		}
	})
}

// RFC 7677 section 3 gives a complete SCRAM-SHA-256 exchange. The server nonce
// is injected so the exchange can be replayed byte for byte.
func TestSCRAMRFC7677Vector(t *testing.T) {
	salt, err := base64.StdEncoding.DecodeString("W22ZaJ0SNY7soEsUEjb6gQ==")
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier("user", "pencil", salt, 4096)
	be, fe, _ := authPipe(t)

	done := make(chan error, 1)
	go func() {
		done <- authenticateSCRAM(be, v, func() string { return "%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0" })
	}()

	msg, err := fe.Receive()
	if err != nil {
		t.Fatalf("receive mechanisms: %v", err)
	}
	sasl, ok := msg.(*pgproto3.AuthenticationSASL)
	if !ok || len(sasl.AuthMechanisms) != 1 || sasl.AuthMechanisms[0] != "SCRAM-SHA-256" {
		t.Fatalf("got %#v, want AuthenticationSASL advertising only SCRAM-SHA-256", msg)
	}

	fe.Send(&pgproto3.SASLInitialResponse{
		AuthMechanism: "SCRAM-SHA-256",
		Data:          []byte("n,,n=user,r=rOprNGfwEbeRWgbNEkqO"),
	})
	if err = fe.Flush(); err != nil {
		t.Fatal(err)
	}
	msg, err = fe.Receive()
	if err != nil {
		t.Fatalf("receive server-first: %v", err)
	}
	cont, ok := msg.(*pgproto3.AuthenticationSASLContinue)
	if !ok {
		t.Fatalf("got %T, want AuthenticationSASLContinue", msg)
	}
	wantFirst := "r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096"
	if string(cont.Data) != wantFirst {
		t.Fatalf("server-first = %q, want %q", cont.Data, wantFirst)
	}

	fe.Send(&pgproto3.SASLResponse{Data: []byte(
		"c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,p=dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ=")})
	if err = fe.Flush(); err != nil {
		t.Fatal(err)
	}
	msg, err = fe.Receive()
	if err != nil {
		t.Fatalf("receive server-final: %v", err)
	}
	fin, ok := msg.(*pgproto3.AuthenticationSASLFinal)
	if !ok {
		t.Fatalf("got %T, want AuthenticationSASLFinal", msg)
	}
	if want := "v=6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4="; string(fin.Data) != want {
		t.Fatalf("server-final = %q, want %q", fin.Data, want)
	}
	msg, err = fe.Receive()
	if err != nil {
		t.Fatalf("receive AuthenticationOk: %v", err)
	}
	if _, ok := msg.(*pgproto3.AuthenticationOk); !ok {
		t.Fatalf("got %T, want AuthenticationOk", msg)
	}
	if err := <-done; err != nil {
		t.Fatalf("authenticateSCRAM: %v", err)
	}
}

// scramProof computes the client proof for a client-final-without-proof the way
// RFC 5802 describes, independent of auth.go.
func scramProof(password string, salt []byte, iter int, authMessage string) string {
	salted, _ := pbkdf2.Key(sha256.New, password, salt, iter, sha256.Size)
	mac := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(msg))
		return h.Sum(nil)
	}
	clientKey := mac(salted, "Client Key")
	stored := sha256.Sum256(clientKey)
	sig := mac(stored[:], authMessage)
	proof := make([]byte, len(clientKey))
	for i := range proof {
		proof[i] = clientKey[i] ^ sig[i]
	}
	return base64.StdEncoding.EncodeToString(proof)
}

func TestSCRAMRejects(t *testing.T) {
	const bare = "n=,r=clientnonce"
	valid := func(nonce, proof string) string { return "c=biws,r=" + nonce + ",p=" + proof }

	cases := []struct {
		name  string
		mech  string
		first string
		// final builds the client-final message from the combined nonce and
		// the correct proof. A nil final means the server must reject the
		// client-first message itself.
		final func(nonce, proof string) string
	}{
		{name: "channel binding requested", mech: "SCRAM-SHA-256", first: "p=tls-server-end-point,," + bare},
		{name: "plus mechanism", mech: "SCRAM-SHA-256-PLUS", first: "n,," + bare},
		{name: "authorization identity", mech: "SCRAM-SHA-256", first: "n,a=someone," + bare},
		{name: "missing nonce", mech: "SCRAM-SHA-256", first: "n,,n=,x=1"},
		{name: "empty nonce", mech: "SCRAM-SHA-256", first: "n,,n=,r="},
		{name: "no gs2 header", mech: "SCRAM-SHA-256", first: "garbage"},
		{name: "wrong proof", mech: "SCRAM-SHA-256", first: "n,," + bare,
			final: func(nonce, _ string) string {
				return valid(nonce, scramProof("other", authSalt, 4096, "x"))
			}},
		{name: "proof not base64", mech: "SCRAM-SHA-256", first: "n,," + bare,
			final: func(nonce, _ string) string { return valid(nonce, "!!!") }},
		{name: "short proof", mech: "SCRAM-SHA-256", first: "n,," + bare,
			final: func(nonce, _ string) string { return valid(nonce, "AAAA") }},
		{name: "no proof", mech: "SCRAM-SHA-256", first: "n,," + bare,
			final: func(nonce, _ string) string { return "c=biws,r=" + nonce }},
		{name: "wrong nonce", mech: "SCRAM-SHA-256", first: "n,," + bare,
			final: func(nonce, proof string) string { return valid(nonce+"x", proof) }},
		{name: "wrong channel binding", mech: "SCRAM-SHA-256", first: "n,," + bare,
			final: func(nonce, proof string) string { return "c=eSws,r=" + nonce + ",p=" + proof }},
	}

	v := NewVerifier("app", "pw", authSalt, 4096)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			be, fe, srv := authPipe(t)
			done := make(chan error, 1)
			go func() { done <- AuthenticateSCRAM(be, v) }()

			if _, err := fe.Receive(); err != nil {
				t.Fatalf("receive mechanisms: %v", err)
			}
			fe.Send(&pgproto3.SASLInitialResponse{AuthMechanism: tc.mech, Data: []byte(tc.first)})
			if err := fe.Flush(); err != nil {
				t.Fatal(err)
			}

			if tc.final != nil {
				msg, err := fe.Receive()
				if err != nil {
					t.Fatalf("receive server-first: %v", err)
				}
				serverFirst := string(msg.(*pgproto3.AuthenticationSASLContinue).Data)
				nonce := strings.TrimPrefix(strings.SplitN(serverFirst, ",", 2)[0], "r=")
				withoutProof := "c=biws,r=" + nonce
				proof := scramProof("pw", authSalt, 4096, bare+","+serverFirst+","+withoutProof)
				fe.Send(&pgproto3.SASLResponse{Data: []byte(tc.final(nonce, proof))})
				if err = fe.Flush(); err != nil {
					t.Fatal(err)
				}
			}

			if err := <-done; err != ErrAuth { //nolint:errorlint // the test pins the unwrapped sentinel
				t.Fatalf("got %v, want exactly ErrAuth", err)
			}
			// Nothing may have followed the rejection.
			srv.Close()
			if msg, err := fe.Receive(); err == nil {
				t.Fatalf("server sent %T after rejecting", msg)
			}
		})
	}
}

func TestSCRAMAcceptsHandRolledClient(t *testing.T) {
	// A client that sends "y" (supports channel binding, thinks the server does
	// not) must be accepted, and the c= attribute must echo that header.
	v := NewVerifier("app", "pw", authSalt, 4096)
	be, fe, _ := authPipe(t)
	done := make(chan error, 1)
	go func() { done <- AuthenticateSCRAM(be, v) }()

	if _, err := fe.Receive(); err != nil {
		t.Fatal(err)
	}
	const bare = "n=ignored,r=abc"
	fe.Send(&pgproto3.SASLInitialResponse{AuthMechanism: "SCRAM-SHA-256", Data: []byte("y,," + bare)})
	if err := fe.Flush(); err != nil {
		t.Fatal(err)
	}
	msg, err := fe.Receive()
	if err != nil {
		t.Fatal(err)
	}
	serverFirst := string(msg.(*pgproto3.AuthenticationSASLContinue).Data)
	nonce := strings.TrimPrefix(strings.SplitN(serverFirst, ",", 2)[0], "r=")
	if !strings.HasPrefix(nonce, "abc") || len(nonce) <= len("abc") {
		t.Fatalf("server nonce %q does not extend the client nonce", nonce)
	}
	if !strings.HasSuffix(serverFirst, ",i=4096") {
		t.Fatalf("server-first %q lacks the iteration count", serverFirst)
	}
	withoutProof := "c=" + base64.StdEncoding.EncodeToString([]byte("y,,")) + ",r=" + nonce
	proof := scramProof("pw", authSalt, 4096, bare+","+serverFirst+","+withoutProof)
	fe.Send(&pgproto3.SASLResponse{Data: []byte(withoutProof + ",p=" + proof)})
	if err := fe.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := fe.Receive(); err != nil { // AuthenticationSASLFinal
		t.Fatal(err)
	}
	if _, err := fe.Receive(); err != nil { // AuthenticationOk
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("AuthenticateSCRAM: %v", err)
	}
}

func TestSCRAMNoncesDiffer(t *testing.T) {
	if a, b := randomNonce(), randomNonce(); a == b || len(a) == 0 {
		t.Fatalf("nonces %q and %q must be distinct and non-empty", a, b)
	}
}

// bufferBackend is a Backend whose input is empty and whose output is captured,
// for checking what the gateway wrote when it did not need to read.
func bufferBackend() (*pgproto3.Backend, *pgproto3.Frontend) {
	out := &syncBuffer{}
	be := pgproto3.NewBackend(strings.NewReader(""), out)
	fe := pgproto3.NewFrontend(out, nopWriter{})
	return be, fe
}

type syncBuffer struct{ b []byte }

func (s *syncBuffer) Write(p []byte) (int, error) { s.b = append(s.b, p...); return len(p), nil }
func (s *syncBuffer) Read(p []byte) (int, error) {
	if len(s.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.b)
	s.b = s.b[n:]
	return n, nil
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestCleartextPipelined(t *testing.T) {
	v := NewVerifier("app", "pw", authSalt, 4096)
	be, fe := bufferBackend()

	err := AuthenticateCleartext(be, v, &pgproto3.PasswordMessage{Password: "pw"})
	if err != nil {
		t.Fatalf("AuthenticateCleartext: %v", err)
	}
	// The only thing on the wire is AuthenticationOk: no password request.
	msg, err := fe.Receive()
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if _, ok := msg.(*pgproto3.AuthenticationOk); !ok {
		t.Fatalf("got %T, want AuthenticationOk", msg)
	}
	if msg, err := fe.Receive(); err == nil {
		t.Fatalf("extra message %T after AuthenticationOk", msg)
	}
}

func TestCleartextWrong(t *testing.T) {
	v := NewVerifier("app", "pw", authSalt, 4096)
	be, fe := bufferBackend()

	err := AuthenticateCleartext(be, v, &pgproto3.PasswordMessage{Password: "nope"})
	if err != ErrAuth { //nolint:errorlint // the test pins the unwrapped sentinel
		t.Fatalf("got %v, want exactly ErrAuth", err)
	}
	if msg, err := fe.Receive(); err == nil {
		t.Fatalf("sent %T on failure, want nothing", msg)
	}
}

func TestCleartextAsksWhenNotPipelined(t *testing.T) {
	v := NewVerifier("app", "pw", authSalt, 4096)
	for _, tc := range []struct {
		name, password string
		want           error
	}{
		{"right", "pw", nil},
		{"wrong", "nope", ErrAuth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			be, fe, _ := authPipe(t)
			done := make(chan error, 1)
			go func() { done <- AuthenticateCleartext(be, v, nil) }()

			msg, err := fe.Receive()
			if err != nil {
				t.Fatalf("receive: %v", err)
			}
			if _, ok := msg.(*pgproto3.AuthenticationCleartextPassword); !ok {
				t.Fatalf("got %T, want AuthenticationCleartextPassword", msg)
			}
			fe.Send(&pgproto3.PasswordMessage{Password: tc.password})
			if err = fe.Flush(); err != nil {
				t.Fatal(err)
			}
			if tc.want == nil {
				if msg, err = fe.Receive(); err != nil {
					t.Fatal(err)
				} else if _, ok := msg.(*pgproto3.AuthenticationOk); !ok {
					t.Fatalf("got %T, want AuthenticationOk", msg)
				}
			}
			if got := <-done; got != tc.want { //nolint:errorlint // the test pins the unwrapped sentinel
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestVerifyPassword(t *testing.T) {
	v := NewVerifier("app", "pw", authSalt, 4096)
	if err := VerifyPassword(v, "pw"); err != nil {
		t.Fatalf("right password: %v", err)
	}
	for _, pw := range []string{"", "p", "pw ", "PW", strings.Repeat("pw", 100)} {
		if err := VerifyPassword(v, pw); err != ErrAuth { //nolint:errorlint // the test pins the unwrapped sentinel
			t.Errorf("password %q: got %v, want exactly ErrAuth", pw, err)
		}
	}

	// A verifier that is missing its keys or has a bad iteration count never
	// matches, not even the empty password.
	for _, bad := range []Verifier{
		{Role: "app", Salt: authSalt, Iterations: 4096},
		{Role: "app", Salt: authSalt, Iterations: 0, StoredKey: v.StoredKey},
		NewVerifier("app", "pw", authSalt, -1),
	} {
		if err := VerifyPassword(bad, "pw"); err != ErrAuth { //nolint:errorlint // the test pins the unwrapped sentinel
			t.Errorf("verifier %+v: got %v, want ErrAuth", bad, err)
		}
	}
}

func TestNewVerifierShape(t *testing.T) {
	v := NewVerifier("app", "pw", authSalt, 4096)
	if v.Role != "app" || v.Iterations != 4096 || string(v.Salt) != string(authSalt) {
		t.Fatalf("unexpected verifier fields: %+v", v)
	}
	if len(v.StoredKey) != 32 || len(v.ServerKey) != 32 {
		t.Fatalf("key lengths %d and %d, want 32", len(v.StoredKey), len(v.ServerKey))
	}
	if string(v.StoredKey) == string(v.ServerKey) {
		t.Fatal("stored and server keys must differ")
	}
	other := NewVerifier("app", "pw2", authSalt, 4096)
	if string(other.StoredKey) == string(v.StoredKey) {
		t.Fatal("different passwords produced the same stored key")
	}
}
