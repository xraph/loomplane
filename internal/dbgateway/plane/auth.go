package plane

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgproto3"
)

// ErrAuth is returned when a client fails to authenticate. The caller turns it
// into SQLSTATE 28P01. It carries no detail on purpose: a client must not learn
// whether the role, the password or the message framing was wrong.
var ErrAuth = errors.New("password authentication failed")

const scramMechanism = "SCRAM-SHA-256"

// NewVerifier derives the SCRAM-SHA-256 verifier for a role's password. The
// control plane calls this; the gateway only ever holds the result.
//
// pbkdf2 can only fail for an invalid key length or a FIPS-restricted input.
// In that case the returned verifier has no keys, so every password check
// against it fails.
func NewVerifier(role, password string, salt []byte, iterations int) Verifier {
	v := Verifier{Role: role, Salt: salt, Iterations: iterations}
	salted, err := saltedPassword(password, salt, iterations)
	if err != nil {
		return v
	}
	clientKey := hmacSHA256(salted, []byte("Client Key"))
	stored := sha256.Sum256(clientKey)
	v.StoredKey = stored[:]
	v.ServerKey = hmacSHA256(salted, []byte("Server Key"))
	return v
}

// VerifyPassword checks a cleartext password against a verifier in constant
// time. It returns exactly ErrAuth on a mismatch.
func VerifyPassword(v Verifier, password string) error {
	// Postgres falls back to the raw bytes when SASLprep fails on the password,
	// so we use the raw bytes and skip normalisation.
	salted, err := saltedPassword(password, v.Salt, v.Iterations)
	if err != nil {
		return ErrAuth
	}
	clientKey := hmacSHA256(salted, []byte("Client Key"))
	stored := sha256.Sum256(clientKey)
	if subtle.ConstantTimeCompare(stored[:], v.StoredKey) != 1 {
		return ErrAuth
	}
	return nil
}

// AuthenticateCleartext checks a cleartext password against the verifier and
// sends AuthenticationOk on success. WebSocket clients pipeline their password
// right behind the StartupMessage, so pipelined is non-nil for them and no
// password request is sent. When it is nil, the client is asked for one.
//
// On failure it returns ErrAuth and sends nothing; the caller sends 28P01.
func AuthenticateCleartext(be *pgproto3.Backend, v Verifier, pipelined *pgproto3.PasswordMessage) error {
	pw := pipelined
	if pw == nil {
		if err := be.SetAuthType(pgproto3.AuthTypeCleartextPassword); err != nil {
			return err
		}
		be.Send(&pgproto3.AuthenticationCleartextPassword{})
		if err := be.Flush(); err != nil {
			return err
		}
		msg, err := be.Receive()
		if err != nil {
			return err
		}
		var ok bool
		if pw, ok = msg.(*pgproto3.PasswordMessage); !ok {
			return ErrAuth
		}
	}
	if err := VerifyPassword(v, pw.Password); err != nil {
		return err
	}
	be.Send(&pgproto3.AuthenticationOk{})
	return be.Flush()
}

// AuthenticateSCRAM runs the server side of SCRAM-SHA-256 (RFC 5802, RFC 7677)
// against the verifier and sends AuthenticationOk on success. Only the plain
// mechanism is offered; channel binding is TCP-only and not supported here.
//
// On failure it returns ErrAuth and sends nothing; the caller sends 28P01.
// Transport errors are returned as they are.
func AuthenticateSCRAM(be *pgproto3.Backend, v Verifier) error {
	return authenticateSCRAM(be, v, randomNonce)
}

func randomNonce() string {
	b := make([]byte, 18)
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func authenticateSCRAM(be *pgproto3.Backend, v Verifier, serverNonce func() string) error {
	if err := be.SetAuthType(pgproto3.AuthTypeSASL); err != nil {
		return err
	}
	be.Send(&pgproto3.AuthenticationSASL{AuthMechanisms: []string{scramMechanism}})
	if err := be.Flush(); err != nil {
		return err
	}

	msg, err := be.Receive()
	if err != nil {
		return err
	}
	initial, ok := msg.(*pgproto3.SASLInitialResponse)
	if !ok || initial.AuthMechanism != scramMechanism {
		return ErrAuth
	}
	gs2Header, clientFirstBare, clientNonce, ok := parseClientFirst(initial.Data)
	if !ok {
		return ErrAuth
	}

	nonce := clientNonce + serverNonce()
	serverFirst := "r=" + nonce +
		",s=" + base64.StdEncoding.EncodeToString(v.Salt) +
		",i=" + strconv.Itoa(v.Iterations)

	if serr := be.SetAuthType(pgproto3.AuthTypeSASLContinue); serr != nil {
		return serr
	}
	be.Send(&pgproto3.AuthenticationSASLContinue{Data: []byte(serverFirst)})
	if ferr := be.Flush(); ferr != nil {
		return ferr
	}

	msg, err = be.Receive()
	if err != nil {
		return err
	}
	final, ok := msg.(*pgproto3.SASLResponse)
	if !ok {
		return ErrAuth
	}
	withoutProof, proof, ok := parseClientFinal(final.Data, gs2Header, nonce)
	if !ok {
		return ErrAuth
	}

	authMessage := clientFirstBare + "," + serverFirst + "," + withoutProof
	clientSignature := hmacSHA256(v.StoredKey, []byte(authMessage))
	if len(proof) != len(clientSignature) || len(v.StoredKey) != sha256.Size {
		return ErrAuth
	}
	clientKey := make([]byte, len(proof))
	for i := range proof {
		clientKey[i] = proof[i] ^ clientSignature[i]
	}
	stored := sha256.Sum256(clientKey)
	if subtle.ConstantTimeCompare(stored[:], v.StoredKey) != 1 {
		return ErrAuth
	}

	serverSignature := hmacSHA256(v.ServerKey, []byte(authMessage))
	be.Send(&pgproto3.AuthenticationSASLFinal{
		Data: []byte("v=" + base64.StdEncoding.EncodeToString(serverSignature)),
	})
	be.Send(&pgproto3.AuthenticationOk{})
	return be.Flush()
}

// parseClientFirst splits "gs2-header client-first-bare". Postgres ignores the
// SCRAM username (the startup user wins), so n= is accepted as it is. It
// rejects channel binding (p=), an authorization identity (a=) and mandatory
// extensions (m=).
func parseClientFirst(data []byte) (gs2Header, bare, nonce string, ok bool) {
	s := string(data)
	parts := strings.SplitN(s, ",", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	// parts[0] is the channel binding flag: "n" or "y". "p=..." is refused.
	if parts[0] != "n" && parts[0] != "y" {
		return "", "", "", false
	}
	// parts[1] is the authorization identity, which we do not support.
	if parts[1] != "" {
		return "", "", "", false
	}
	gs2Header = parts[0] + "," + parts[1] + ","
	bare = parts[2]

	attrs := strings.Split(bare, ",")
	if len(attrs) < 2 || !strings.HasPrefix(attrs[0], "n=") {
		return "", "", "", false
	}
	if !strings.HasPrefix(attrs[1], "r=") || len(attrs[1]) == 2 {
		return "", "", "", false
	}
	return gs2Header, bare, attrs[1][2:], true
}

// parseClientFinal checks the channel binding and nonce of the final message
// and returns it without the proof, together with the decoded proof.
func parseClientFinal(data []byte, gs2Header, nonce string) (withoutProof string, proof []byte, ok bool) {
	s := string(data)
	i := strings.LastIndex(s, ",p=")
	if i < 0 {
		return "", nil, false
	}
	withoutProof = s[:i]
	proof, err := base64.StdEncoding.DecodeString(s[i+len(",p="):])
	if err != nil {
		return "", nil, false
	}

	attrs := strings.Split(withoutProof, ",")
	if len(attrs) < 2 {
		return "", nil, false
	}
	wantBinding := "c=" + base64.StdEncoding.EncodeToString([]byte(gs2Header))
	if attrs[0] != wantBinding {
		return "", nil, false
	}
	if attrs[1] != "r="+nonce {
		return "", nil, false
	}
	return withoutProof, proof, true
}

func saltedPassword(password string, salt []byte, iterations int) ([]byte, error) {
	if iterations < 1 {
		return nil, fmt.Errorf("invalid iteration count %d", iterations)
	}
	return pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
}

func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}
