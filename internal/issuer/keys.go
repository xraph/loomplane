package issuer

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/pglock"
)

// Key is one of the issuer's signing keys as the database keeps it: its
// kid, its public half as a JWK, and when it was made and retired. Its
// private half is only ever in Vault, under its kid.
type Key struct {
	KID       string
	Public    json.RawMessage
	CreatedAt time.Time
	RetiredAt *time.Time
}

// KeyStore keeps the issuer's public keys. WithKeyLock runs fn while no
// other replica holds the issuer's key lock, so replicas that rotate at
// once take turns.
type KeyStore interface {
	Keys(ctx context.Context) ([]Key, error)
	AddKey(ctx context.Context, k Key) error
	RetireKey(ctx context.Context, kid string, at time.Time) error
	DeleteKey(ctx context.Context, kid string) error
	WithKeyLock(ctx context.Context, fn func() error) error
}

// keyLock is the advisory lock rotation takes.
const keyLock = "oidc-issuer-keys"

// keyBits is the size of every signing key.
const keyBits = 2048

// PgKeyStore is the Postgres KeyStore, over loomplane_oidc_keys.
type PgKeyStore struct {
	db *pgdriver.PgDB
}

var _ KeyStore = (*PgKeyStore)(nil)

// NewPgKeyStore returns a KeyStore over db.
func NewPgKeyStore(db *pgdriver.PgDB) *PgKeyStore { return &PgKeyStore{db: db} }

// Keys implements KeyStore, oldest first.
func (s *PgKeyStore) Keys(ctx context.Context) ([]Key, error) {
	rows, err := s.db.Query(ctx, `SELECT kid, public_jwk, created_at, retired_at FROM loomplane_oidc_keys ORDER BY created_at, kid`)
	if err != nil {
		return nil, fmt.Errorf("list issuer keys: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []Key{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("scan issuer key: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list issuer keys: %w", err)
	}
	return out, nil
}

func scanKey(row pgx.Row) (Key, error) {
	var k Key
	var public []byte
	if err := row.Scan(&k.KID, &public, &k.CreatedAt, &k.RetiredAt); err != nil {
		return Key{}, err
	}
	k.Public = json.RawMessage(public)
	k.CreatedAt = k.CreatedAt.UTC()
	if k.RetiredAt != nil {
		at := k.RetiredAt.UTC()
		k.RetiredAt = &at
	}
	return k, nil
}

// AddKey implements KeyStore.
func (s *PgKeyStore) AddKey(ctx context.Context, k Key) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_oidc_keys (kid, public_jwk, created_at, retired_at) VALUES ($1, $2, $3, $4)`,
		k.KID, []byte(k.Public), k.CreatedAt, k.RetiredAt)
	if err != nil {
		return fmt.Errorf("save issuer key: %w", err)
	}
	return nil
}

// RetireKey implements KeyStore. A key retired already keeps its time.
func (s *PgKeyStore) RetireKey(ctx context.Context, kid string, at time.Time) error {
	if _, err := s.db.Exec(ctx, `UPDATE loomplane_oidc_keys SET retired_at = $2 WHERE kid = $1 AND retired_at IS NULL`, kid, at); err != nil {
		return fmt.Errorf("retire issuer key: %w", err)
	}
	return nil
}

// DeleteKey implements KeyStore.
func (s *PgKeyStore) DeleteKey(ctx context.Context, kid string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM loomplane_oidc_keys WHERE kid = $1`, kid); err != nil {
		return fmt.Errorf("delete issuer key: %w", err)
	}
	return nil
}

// WithKeyLock implements KeyStore with the oidc-issuer-keys advisory lock.
func (s *PgKeyStore) WithKeyLock(ctx context.Context, fn func() error) error {
	return pglock.Do(ctx, s.db, keyLock, func(context.Context) error { return fn() })
}

// Rotate keeps the issuer's keys in order, under the key lock. With no
// current key it makes one, and the ticket key if the issuer is new. A
// current key older than rotateEvery is retired for a new one. A retired
// key stays in the JWKS for publishRetired, so tokens it signed still
// verify, and after that its row and its private key are deleted.
//
// Mint and Ticket call it when they find no key. Otherwise it's for the
// background job, and any replica can run it: the lock makes them take
// turns, and each looks again at what the last one left.
func (s *Service) Rotate(ctx context.Context) error {
	defer s.forget()
	return s.keys.WithKeyLock(ctx, func() error { return s.rotate(ctx) })
}

func (s *Service) rotate(ctx context.Context) error {
	keys, err := s.keys.Keys(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	current := currentKey(keys)
	if current == nil {
		if err := s.ensureTicketKey(ctx, len(keys) == 0); err != nil {
			return err
		}
	}
	if current == nil || now.Sub(current.CreatedAt) >= rotateEvery {
		k, addErr := s.addKey(ctx, now)
		if addErr != nil {
			return addErr
		}
		current = &k
	}
	// Every key but the current one is retired, so one a crash left
	// unretired after its replacement is caught here.
	for _, k := range keys {
		if k.RetiredAt == nil && k.KID != current.KID {
			if err := s.keys.RetireKey(ctx, k.KID, now); err != nil {
				return err
			}
		}
	}
	for _, k := range keys {
		if k.RetiredAt == nil || now.Sub(*k.RetiredAt) <= publishRetired {
			continue
		}
		// Vault first: a row left behind is retired and retried next
		// time, while a private key left behind would be forgotten.
		if err := s.secrets.Delete(ctx, vaultOrg, k.KID); err != nil {
			return fmt.Errorf("delete a retired issuer key from Vault: %w", err)
		}
		if err := s.keys.DeleteKey(ctx, k.KID); err != nil {
			return err
		}
	}
	return nil
}

// ensureTicketKey makes the ticket key when it can't be read and the
// issuer has never had a key. An issuer that has keys made its ticket key
// with the first one, so failing to read it then is an error to report,
// not a reason to make another: a new ticket key would break every
// handout Loomplane has given out.
func (s *Service) ensureTicketKey(ctx context.Context, fresh bool) error {
	v, err := s.secrets.Reveal(ctx, vaultOrg, ticketKeyID)
	if err == nil && len(v) >= ticketKeyBytes {
		return nil
	}
	if !fresh {
		if err == nil {
			return errors.New("the issuer's ticket key is too short")
		}
		return fmt.Errorf("read the issuer's ticket key: %w", err)
	}
	key := make([]byte, ticketKeyBytes)
	if _, err = rand.Read(key); err != nil {
		return fmt.Errorf("make the issuer's ticket key: %w", err)
	}
	if err = s.secrets.Put(ctx, vaultOrg, ticketKeyID, key); err != nil {
		return fmt.Errorf("store the issuer's ticket key: %w", err)
	}
	return nil
}

// addKey makes a signing key, puts its private half in Vault, and only
// then adds its row, so every key another replica can see can sign.
func (s *Service) addKey(ctx context.Context, now time.Time) (Key, error) {
	private, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return Key{}, fmt.Errorf("make an issuer key: %w", err)
	}
	kid, public, err := publicJWK(&private.PublicKey)
	if err != nil {
		return Key{}, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return Key{}, fmt.Errorf("encode an issuer key: %w", err)
	}
	if err = s.secrets.Put(ctx, vaultOrg, kid, der); err != nil {
		return Key{}, fmt.Errorf("store an issuer key in Vault: %w", err)
	}
	k := Key{KID: kid, Public: public, CreatedAt: now}
	if err = s.keys.AddKey(ctx, k); err != nil {
		return Key{}, errors.Join(err, s.secrets.Delete(ctx, vaultOrg, kid))
	}
	return k, nil
}

// currentKey is the newest key that isn't retired, or nil.
func currentKey(keys []Key) *Key {
	var current *Key
	for i := range keys {
		k := &keys[i]
		if k.RetiredAt != nil {
			continue
		}
		if current == nil || k.CreatedAt.After(current.CreatedAt) || (k.CreatedAt.Equal(current.CreatedAt) && k.KID > current.KID) {
			current = k
		}
	}
	return current
}

// jwk is a public RSA signing key in the order clouds print it.
type jwk struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	KID string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// publicJWK returns the key's kid, its RFC 7638 thumbprint, and its JWK.
func publicJWK(pub *rsa.PublicKey) (string, json.RawMessage, error) {
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	// RFC 7638: the required members, in lexical order, with no spaces.
	thumb, err := json.Marshal(struct {
		E   string `json:"e"`
		Kty string `json:"kty"`
		N   string `json:"n"`
	}{e, "RSA", n})
	if err != nil {
		return "", nil, fmt.Errorf("encode an issuer key thumbprint: %w", err)
	}
	sum := sha256.Sum256(thumb)
	kid := base64.RawURLEncoding.EncodeToString(sum[:])
	public, err := json.Marshal(jwk{Kty: "RSA", Use: "sig", Alg: "RS256", KID: kid, N: n, E: e})
	if err != nil {
		return "", nil, fmt.Errorf("encode an issuer key: %w", err)
	}
	return kid, public, nil
}
