package issuer

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/keyless"
)

// memKeys is a KeyStore in memory. Its lock is a mutex, so tests that
// rotate from two goroutines take turns the way replicas do.
type memKeys struct {
	mu   sync.Mutex
	lock sync.Mutex
	keys []Key
	// addErr, when set, is what AddKey fails with.
	addErr error
}

func (m *memKeys) Keys(context.Context) ([]Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.keys), nil
}

func (m *memKeys) AddKey(_ context.Context, k Key) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.addErr != nil {
		return m.addErr
	}
	m.keys = append(m.keys, k)
	return nil
}

func (m *memKeys) RetireKey(_ context.Context, kid string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.keys {
		if m.keys[i].KID == kid {
			m.keys[i].RetiredAt = &at
		}
	}
	return nil
}

func (m *memKeys) DeleteKey(_ context.Context, kid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys = slices.DeleteFunc(m.keys, func(k Key) bool { return k.KID == kid })
	return nil
}

func (m *memKeys) WithKeyLock(_ context.Context, fn func() error) error {
	m.lock.Lock()
	defer m.lock.Unlock()
	return fn()
}

// memSecrets stands in for Vault.
type memSecrets struct {
	mu sync.Mutex
	m  map[string][]byte
	// deleteErr, when set, is what Delete fails with.
	deleteErr error
}

var errNoSecret = errors.New("secret not found")

func (s *memSecrets) Put(_ context.Context, orgID, id string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string][]byte{}
	}
	s.m[orgID+"/"+id] = slices.Clone(v)
	return nil
}

func (s *memSecrets) Reveal(_ context.Context, orgID, id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[orgID+"/"+id]
	if !ok {
		return nil, errNoSecret
	}
	return slices.Clone(v), nil
}

func (s *memSecrets) Delete(_ context.Context, orgID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.m, orgID+"/"+id)
	return nil
}

// has says whether the issuer keeps a secret called id.
func (s *memSecrets) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[vaultOrg+"/"+id]
	return ok
}

// clock is a time that tests move by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

const testIssuer = "https://loomplane.example.com"

func newTestService(t *testing.T) (*Service, *memKeys, *memSecrets, *clock) {
	t.Helper()
	keys, secrets := &memKeys{}, &memSecrets{}
	c := &clock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	return New(testIssuer+"/", "production", keys, secrets, c.now), keys, secrets, c
}

func handout(t *testing.T, s *Service, subject, audience string) keyless.Handout {
	t.Helper()
	ticket, err := s.Ticket(context.Background(), subject, audience)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	return keyless.Handout{Type: keyless.Type, Subject: subject, Audience: audience, Ticket: ticket}
}

// verified checks token's signature against the JWKS, the way a cloud
// does, and returns its header and claims.
func verified(t *testing.T, s *Service, token string) (header, claims map[string]any) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}
	decode := func(part string, into any) {
		raw, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			t.Fatalf("token part isn't base64url: %v", err)
		}
		if err = json.Unmarshal(raw, into); err != nil {
			t.Fatalf("token part isn't JSON: %v", err)
		}
	}
	decode(parts[0], &header)
	decode(parts[1], &claims)
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Fatalf("header alg = %v, typ = %v", header["alg"], header["typ"])
	}

	set, err := s.JWKS(context.Background())
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	var pub *rsa.PublicKey
	for _, k := range jwks(t, set) {
		if k["kid"] != header["kid"] {
			continue
		}
		if k["kty"] != "RSA" || k["use"] != "sig" || k["alg"] != "RS256" {
			t.Fatalf("JWK = %v", k)
		}
		n, nerr := base64.RawURLEncoding.DecodeString(k["n"].(string))
		e, eerr := base64.RawURLEncoding.DecodeString(k["e"].(string))
		if nerr != nil || eerr != nil {
			t.Fatal("JWK n or e isn't base64url")
		}
		pub = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if pub == nil {
		t.Fatalf("no JWK has the token's kid %v", header["kid"])
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal("signature isn't base64url")
	}
	if err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("the token's signature doesn't verify: %v", err)
	}
	return header, claims
}

// jwks reads the published keys through JSON, as a cloud would.
func jwks(t *testing.T, set map[string]any) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Keys []map[string]any `json:"keys"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.Keys
}

func TestMintSignsAVerifiableToken(t *testing.T) {
	s, _, _, c := newTestService(t)
	ctx := context.Background()
	h := handout(t, s, keyless.Subject("org_1", "conn_1"), "sts.amazonaws.com")

	token, err := s.Mint(ctx, h)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	header, claims := verified(t, s, token)

	keys := jwks(t, mustJWKS(t, s))
	if len(keys) != 1 || header["kid"] != keys[0]["kid"] {
		t.Fatalf("kid = %v, want the one published key's", header["kid"])
	}
	if claims["iss"] != testIssuer {
		t.Errorf("iss = %v, want %s", claims["iss"], testIssuer)
	}
	if claims["sub"] != "org:org_1:connection:conn_1" {
		t.Errorf("sub = %v", claims["sub"])
	}
	if claims["aud"] != "sts.amazonaws.com" {
		t.Errorf("aud = %v, want the string sts.amazonaws.com", claims["aud"])
	}
	// iat and nbf are a minute back, so a cloud whose clock is a little
	// behind still takes a fresh token. It lapses ten minutes from now.
	iat, exp, nbf := claims["iat"].(float64), claims["exp"].(float64), claims["nbf"].(float64)
	if exp-iat != 660 {
		t.Errorf("exp - iat = %v, want 660", exp-iat)
	}
	if int64(iat) != c.now().Unix()-60 || nbf != iat {
		t.Errorf("iat = %v, nbf = %v, want %d for both", iat, nbf, c.now().Unix()-60)
	}
	if int64(exp) != c.now().Unix()+600 {
		t.Errorf("exp = %v, want %d", exp, c.now().Unix()+600)
	}
	jti, _ := claims["jti"].(string)
	if raw, jerr := base64.RawURLEncoding.DecodeString(jti); jerr != nil || len(raw) != 16 {
		t.Errorf("jti should be 16 random bytes in base64url")
	}

	again, err := s.Mint(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if _, claims2 := verified(t, s, again); claims2["jti"] == claims["jti"] {
		t.Error("two tokens share a jti")
	}
}

func mustJWKS(t *testing.T, s *Service) map[string]any {
	t.Helper()
	set, err := s.JWKS(context.Background())
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	return set
}

func TestMintRefusesAForgedTicket(t *testing.T) {
	s, _, _, _ := newTestService(t)
	ctx := context.Background()
	mine := handout(t, s, keyless.Subject("org_1", "conn_1"), "sts.amazonaws.com")

	otherSubject := mine
	otherSubject.Subject = keyless.Subject("org_2", "conn_9")
	otherAudience := mine
	otherAudience.Audience = "api://AzureADTokenExchange"
	garbage := mine
	garbage.Ticket = "not-a-ticket!"

	for name, h := range map[string]keyless.Handout{
		"another subject":  otherSubject,
		"another audience": otherAudience,
		"garbage":          garbage,
	} {
		token, err := s.Mint(ctx, h)
		if !errors.Is(err, ErrForged) {
			t.Errorf("%s: err = %v, want ErrForged", name, err)
		}
		if token != "" {
			t.Errorf("%s: a forged ticket got a token", name)
		}
		if err != nil && strings.Contains(err.Error(), mine.Ticket) {
			t.Errorf("%s: the error repeats the ticket", name)
		}
	}
}

// mintedKID mints for h and returns the kid the token was signed with,
// after checking the signature against the JWKS.
func mintedKID(t *testing.T, s *Service, h keyless.Handout) string {
	t.Helper()
	token, err := s.Mint(context.Background(), h)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	header, _ := verified(t, s, token)
	kid, _ := header["kid"].(string)
	return kid
}

func kids(t *testing.T, s *Service) []string {
	t.Helper()
	set := jwks(t, mustJWKS(t, s))
	out := make([]string, 0, len(set))
	for _, k := range set {
		out = append(out, k["kid"].(string))
	}
	slices.Sort(out)
	return out
}

// A key's successor is published prePublish before it signs, so a cloud
// that cached the JWKS before the successor existed has fetched it again
// by the time a token needs it. The key it replaces stays publishRetired
// after it's retired, then it's gone from the JWKS and from Vault.
func TestRotationKeepsThePreviousKeyForADay(t *testing.T) {
	s, keys, secrets, c := newTestService(t)
	ctx := context.Background()
	if err := s.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	first := kids(t, s)
	if len(first) != 1 {
		t.Fatalf("after the first rotation JWKS has %d keys, want 1", len(first))
	}
	oldKID := first[0]
	if !secrets.has(oldKID) || !secrets.has(ticketKeyID) {
		t.Fatal("the first rotation should keep a private key and the ticket key in Vault")
	}
	h := handout(t, s, keyless.Subject("org_1", "conn_1"), "//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/loomplane/providers/loomplane")
	// A new issuer has nobody's cached keys to wait for, so it signs at once.
	if kid := mintedKID(t, s, h); kid != oldKID {
		t.Fatalf("a new issuer signed with %s, want its only key %s", kid, oldKID)
	}

	// A day before the key is due, its successor is made and published,
	// and tokens still carry the old kid. Rotating again adds no other.
	c.advance(rotateEvery - prePublish + time.Minute)
	for range 2 {
		if err := s.Rotate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	published := kids(t, s)
	if len(published) != 2 {
		t.Fatalf("with a successor published JWKS has %d keys, want 2", len(published))
	}
	newKID := published[0]
	if newKID == oldKID {
		newKID = published[1]
	}
	if kid := mintedKID(t, s, h); kid != oldKID {
		t.Fatal("the successor signed before it had been published for a day")
	}
	all, _ := keys.Keys(ctx)
	for _, k := range all {
		if k.RetiredAt != nil {
			t.Fatalf("%s was retired before its successor took over", k.KID)
		}
	}

	// A day on, the successor signs and the old key is retired but still
	// published.
	c.advance(prePublish)
	if err := s.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	if kid := mintedKID(t, s, h); kid != newKID {
		t.Fatalf("after the switch tokens carry %s, want the successor %s", kid, newKID)
	}
	if got := kids(t, s); len(got) != 2 {
		t.Fatalf("just after the switch JWKS has %d keys, want 2", len(got))
	}

	// A day after it was retired, the old key is gone.
	c.advance(25 * time.Hour)
	if err := s.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	if got := kids(t, s); len(got) != 1 || got[0] != newKID {
		t.Fatalf("a day after retiring JWKS = %v, want only %v", got, newKID)
	}
	if secrets.has(oldKID) {
		t.Fatal("the retired private key is still in Vault")
	}
	if !secrets.has(ticketKeyID) {
		t.Fatal("rotation dropped the ticket key")
	}
	// The ticket key outlives signing keys, so a handout keeps working.
	if kid := mintedKID(t, s, h); kid != newKID {
		t.Fatalf("the first handout after two rotations signed with %s", kid)
	}
}

// A Service that finds no key makes one, once, when it's first asked.
func TestMintMakesTheFirstKey(t *testing.T) {
	s, keys, _, _ := newTestService(t)
	h := handout(t, s, keyless.Subject("org_1", "conn_1"), "sts.amazonaws.com")
	if _, err := s.Mint(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	got, _ := keys.Keys(context.Background())
	if len(got) != 1 {
		t.Fatalf("%d keys, want 1", len(got))
	}
}

// Mint takes only handouts: a keyless JSON with nothing in it is refused.
func TestMintNeedsAHandout(t *testing.T) {
	s, _, _, _ := newTestService(t)
	if _, err := s.Mint(context.Background(), keyless.Handout{Type: keyless.Type}); err == nil {
		t.Fatal("an empty handout got a token")
	}
}

// Two Services sharing a store and a lock make one key between them.
func TestRotateTakesTurns(t *testing.T) {
	keys, secrets := &memKeys{}, &memSecrets{}
	c := &clock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	a := New(testIssuer, "production", keys, secrets, c.now)
	b := New(testIssuer, "production", keys, secrets, c.now)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, s := range []*Service{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.Rotate(context.Background())
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	got, _ := keys.Keys(context.Background())
	if len(got) != 1 {
		t.Fatalf("%d keys, want 1", len(got))
	}
}

func TestDiscoveryNamesTheIssuer(t *testing.T) {
	s, _, _, _ := newTestService(t)
	if s.URL() != testIssuer {
		t.Fatalf("URL = %q, want %q without the trailing slash", s.URL(), testIssuer)
	}
	d := s.Discovery()
	if d["issuer"] != testIssuer {
		t.Errorf("issuer = %v", d["issuer"])
	}
	if d["jwks_uri"] != testIssuer+"/.well-known/jwks.json" {
		t.Errorf("jwks_uri = %v", d["jwks_uri"])
	}
	for field, want := range map[string][]string{
		"response_types_supported":              {"id_token"},
		"subject_types_supported":               {"public"},
		"id_token_signing_alg_values_supported": {"RS256"},
		"claims_supported":                      {"sub", "aud", "exp", "iat", "iss", "jti", "nbf"},
	} {
		if got, _ := d[field].([]string); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", field, d[field], want)
		}
	}
}

func TestUsable(t *testing.T) {
	for _, tc := range []struct {
		url, env string
		ok       bool
		reason   string
	}{
		{"https://loomplane.example.com", "production", true, ""},
		{"https://loomplane.example.com/", "development", true, ""},
		{"http://loomplane.example.com", "production", false, "public_url isn't HTTPS"},
		{"http://loomplane.example.com", "development", false, "public_url isn't HTTPS"},
		{"https://localhost", "production", false, "localhost isn't reachable from the internet"},
		{"https://10.0.0.5", "production", false, "10.0.0.5 isn't reachable from the internet"},
		{"https://192.168.1.4:8443", "production", false, "192.168.1.4 isn't reachable from the internet"},
		{"https://loomplane.internal", "production", false, "loomplane.internal isn't reachable from the internet"},
		{"http://127.0.0.1:8780", "development", true, "development only: no cloud can reach this issuer"},
		{"http://localhost:8780", "development", true, "development only: no cloud can reach this issuer"},
		{"http://127.0.0.1:8780", "production", false, "public_url isn't HTTPS"},
		{"http://127.0.0.1:8780", "staging", false, "public_url isn't HTTPS"},
		{"", "production", false, "public_url isn't set"},
		{"https://ops:hunter2@loomplane.example.com", "production", false, "public_url can't have a user name or password"},
		{"https://ops@loomplane.example.com", "production", false, "public_url can't have a user name or password"},
		{"https://loomplane.example.com/?tenant=1", "production", false, "public_url can't have a query"},
		{"https://loomplane.example.com?", "production", false, "public_url can't have a query"},
		{"https://loomplane.example.com/#top", "production", false, "public_url can't have a fragment"},
		{"https://loomplane.example.com#", "production", false, "public_url can't have a fragment"},
		{"http://127.0.0.1:8780/?a=b", "development", false, "public_url can't have a query"},
		{"https://loomplane.example.com/loomplane", "production", false, "public_url must not have a path for keyless"},
		{"https://loomplane.example.com/loomplane/", "production", false, "public_url must not have a path for keyless"},
		{"http://127.0.0.1:8780/api", "development", false, "public_url must not have a path for keyless"},
	} {
		ok, reason := Usable(tc.url, tc.env)
		if ok != tc.ok || reason != tc.reason {
			t.Errorf("Usable(%q, %q) = %v, %q; want %v, %q", tc.url, tc.env, ok, reason, tc.ok, tc.reason)
		}
	}

	s := New("http://127.0.0.1:8780", "development", &memKeys{}, &memSecrets{}, time.Now)
	if ok, reason := s.Usable(); !ok || reason == "" {
		t.Errorf("Service.Usable = %v, %q; want true with a warning", ok, reason)
	}
}

// A new issuer signs with its first key at once, so every other replica's
// JWKS has to show that key straight away, not after a cache runs out.
func TestJWKSShowsAnotherReplicasNewKey(t *testing.T) {
	keys, secrets := &memKeys{}, &memSecrets{}
	c := &clock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	a := New(testIssuer, "production", keys, secrets, c.now)
	b := New(testIssuer, "production", keys, secrets, c.now)
	ctx := context.Background()
	// b has just served the keys, none yet, when a makes the first one.
	if got := jwks(t, mustJWKS(t, b)); len(got) != 0 {
		t.Fatalf("b's JWKS has %d keys, want none", len(got))
	}
	h := handout(t, a, keyless.Subject("org_1", "conn_1"), "sts.amazonaws.com")
	token, err := a.Mint(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	// b checks the token as a cloud asking b would.
	verified(t, b, token)
}

// The ticket covers a versioned label, the subject and the audience, one
// to a line, so neither may hold a line break: otherwise two different
// pairs could share one ticket.
func TestTicketRefusesLineBreaks(t *testing.T) {
	s, _, _, _ := newTestService(t)
	ctx := context.Background()
	if _, err := s.Ticket(ctx, "org:o:connection:c\nsts.amazonaws.com", "x"); err == nil {
		t.Error("a subject with a line break got a ticket")
	}
	if _, err := s.Ticket(ctx, "org:o:connection:c", "sts.amazonaws.com\nx"); err == nil {
		t.Error("an audience with a line break got a ticket")
	}
}

func TestTicketIsVersioned(t *testing.T) {
	s, _, secrets, _ := newTestService(t)
	ticket, err := s.Ticket(context.Background(), "org:o:connection:c", "sts.amazonaws.com")
	if err != nil {
		t.Fatal(err)
	}
	key, err := secrets.Reveal(context.Background(), vaultOrg, ticketKeyID)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("loomplane-keyless-ticket-v1\norg:o:connection:c\nsts.amazonaws.com"))
	if ticket != base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) {
		t.Fatal("the ticket isn't the HMAC of the v1 label, subject and audience")
	}
}

// countingKeys counts reads of the key table.
type countingKeys struct {
	*memKeys
	mu    sync.Mutex
	reads int
}

func (c *countingKeys) Keys(ctx context.Context) ([]Key, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	return c.memKeys.Keys(ctx)
}

// Clouds fetch the JWKS often, so a replica reads its keys for it at most
// every ten seconds. Rotating here reads them again at once.
func TestJWKSReadsTheKeysAtMostEveryTenSeconds(t *testing.T) {
	keys := &countingKeys{memKeys: &memKeys{}}
	c := &clock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	s := New(testIssuer, "production", keys, &memSecrets{}, c.now)
	ctx := context.Background()
	if err := s.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	first := mustJWKS(t, s)
	before := keys.reads
	c.advance(9 * time.Second)
	if got := mustJWKS(t, s); keys.reads != before || len(jwks(t, got)) != len(jwks(t, first)) {
		t.Fatalf("read the keys %d more times within ten seconds", keys.reads-before)
	}
	c.advance(time.Second)
	mustJWKS(t, s)
	if keys.reads != before+1 {
		t.Fatalf("read the keys %d more times after ten seconds, want 1", keys.reads-before)
	}
	c.advance(rotateEvery)
	if err := s.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	if got := jwks(t, mustJWKS(t, s)); len(got) != 2 {
		t.Fatalf("just after rotating JWKS has %d keys, want 2", len(got))
	}
}

// selfServed is s served at its own URL the way the API serves it, with
// broken to change what a cloud would see.
func selfServed(t *testing.T, broken func(path string, body map[string]any) map[string]any) (*Service, *httptest.Server) {
	t.Helper()
	var s *Service
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			body = s.Discovery()
		case "/.well-known/jwks.json":
			var err error
			if body, err = s.JWKS(r.Context()); err != nil {
				http.Error(w, "no keys", http.StatusInternalServerError)
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		if broken != nil {
			if body = broken(r.URL.Path, body); body == nil {
				http.Error(w, "down", http.StatusBadGateway)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	s = New(srv.URL, "development", &memKeys{}, &memSecrets{}, nil)
	if err := s.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, srv
}

func TestServingFindsTheSigningKeyAtThePublicURL(t *testing.T) {
	s, srv := selfServed(t, nil)
	if err := s.Serving(t.Context(), srv.Client()); err != nil {
		t.Fatalf("serving: %v", err)
	}
}

func TestServingFailsWhenACloudWouldntFindTheKey(t *testing.T) {
	for name, broken := range map[string]func(string, map[string]any) map[string]any{
		"jwks down": func(path string, body map[string]any) map[string]any {
			if path == "/.well-known/jwks.json" {
				return nil
			}
			return body
		},
		"discovery down": func(path string, body map[string]any) map[string]any {
			if path == "/.well-known/openid-configuration" {
				return nil
			}
			return body
		},
		"another issuer": func(path string, body map[string]any) map[string]any {
			if path == "/.well-known/openid-configuration" {
				body["issuer"] = "https://elsewhere.example.com"
			}
			return body
		},
		"no signing key": func(path string, body map[string]any) map[string]any {
			if path == "/.well-known/jwks.json" {
				body["keys"] = []json.RawMessage{}
			}
			return body
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, srv := selfServed(t, broken)
			if err := s.Serving(t.Context(), srv.Client()); err == nil {
				t.Fatal("serving passed")
			}
		})
	}
}

func TestAKeyRowThatFailsTakesItsPrivateKeyOutOfVault(t *testing.T) {
	s, keys, secrets, _ := newTestService(t)
	keys.addErr = errors.New("database down")
	if err := s.Rotate(t.Context()); err == nil {
		t.Fatal("Rotate succeeded without a key row")
	}
	secrets.mu.Lock()
	defer secrets.mu.Unlock()
	for id := range secrets.m {
		if id != vaultOrg+"/"+ticketKeyID {
			t.Errorf("Vault kept %s with no row to find it by", id)
		}
	}
}

func TestARetiredKeyStaysListedWhileVaultWontDeleteIt(t *testing.T) {
	s, keys, secrets, c := newTestService(t)
	stored := func(kid string) bool {
		list, _ := keys.Keys(t.Context()) //nolint:errcheck // memKeys never fails to list
		return slices.ContainsFunc(list, func(k Key) bool { return k.KID == kid })
	}
	if err := s.Rotate(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := kids(t, s)
	c.advance(rotateEvery)
	if err := s.Rotate(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.advance(prePublish + time.Minute)
	if err := s.Rotate(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.advance(publishRetired + time.Minute)
	secrets.deleteErr = errors.New("vault down")
	if err := s.Rotate(t.Context()); err == nil {
		t.Fatal("Rotate succeeded while Vault refused the delete")
	}
	if !stored(first[0]) || !secrets.has(first[0]) {
		t.Fatal("the retired key's row went before its private key")
	}
	secrets.deleteErr = nil
	if err := s.Rotate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stored(first[0]) || secrets.has(first[0]) {
		t.Fatal("the retired key outlived a working Vault")
	}
}
