package plane

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

var snapshotNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func newTrustKey(t *testing.T) (TrustKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return TrustKey{
		PublicKey: pub,
		NotBefore: snapshotNow.Add(-time.Hour),
		NotAfter:  snapshotNow.Add(time.Hour),
	}, priv
}

func sampleSnapshot(version uint64) Snapshot {
	return Snapshot{
		GatewayID: "gw-1",
		Version:   version,
		IssuedAt:  snapshotNow,
		Routes: []Route{
			{
				ID:    "route-b",
				Label: Label{DB: "orders", Branch: "dev"},
				State: RouteStatePaused,
				Backend: Backend{
					Kind: "postgres", Host: "10.0.0.2", Port: 5432, Database: "orders",
					SNI: "db.internal", SSLMode: "verify-full",
					Credential: Sealed{Ephemeral: []byte{1}, Nonce: []byte{2}, Ciphertext: []byte{3}},
				},
				Verifiers: []Verifier{{
					Role: "app", Salt: []byte("salt"), Iterations: 4096,
					StoredKey: []byte("stored"), ServerKey: []byte("server"),
				}},
				Params:      map[string]string{"timezone": "UTC", "application_name": "x"},
				IdleTimeout: 5 * time.Minute,
			},
			{
				ID:    "route-a",
				Label: Label{DB: "orders"},
				State: RouteStateActive,
				Backend: Backend{
					Kind: "postgres", Host: "10.0.0.1", Port: 5432, Database: "orders",
				},
			},
		},
	}
}

func signed(t *testing.T, s Snapshot, k TrustKey, priv ed25519.PrivateKey) SignedSnapshot {
	t.Helper()
	out, err := SignSnapshot(s, priv, k.ID())
	if err != nil {
		t.Fatalf("SignSnapshot: %v", err)
	}
	return out
}

// signRaw signs payload as it stands, the way a control plane of another
// version might, and names k as the key.
func signRaw(payload []byte, k TrustKey, priv ed25519.PrivateKey) SignedSnapshot {
	return SignedSnapshot{
		Payload:   payload,
		KeyID:     k.ID(),
		Signature: ed25519.Sign(priv, append([]byte(snapshotContext), payload...)),
	}
}

func TestVerifySnapshot(t *testing.T) {
	key, priv := newTrustKey(t)
	other, otherPriv := newTrustKey(t)
	good := signed(t, sampleSnapshot(1), key, priv)

	t.Run("valid", func(t *testing.T) {
		got, err := VerifySnapshot(good, []TrustKey{other, key}, "gw-1", snapshotNow)
		if err != nil {
			t.Fatalf("VerifySnapshot: %v", err)
		}
		if got.Version != 1 || got.GatewayID != "gw-1" || len(got.Routes) != 2 || got.KeyID != key.ID() {
			t.Fatalf("VerifySnapshot decoded %+v", got)
		}
	})

	t.Run("at not_before", func(t *testing.T) {
		if _, err := VerifySnapshot(good, []TrustKey{key}, "gw-1", key.NotBefore); err != nil {
			t.Fatalf("VerifySnapshot at not_before: %v", err)
		}
	})

	t.Run("all four states", func(t *testing.T) {
		s := sampleSnapshot(1)
		s.Routes = nil
		for i, st := range []RouteState{RouteStateActive, RouteStatePaused, RouteStateDraining, RouteStateHolding} {
			s.Routes = append(s.Routes, Route{ID: string(rune('a' + i)), Label: Label{DB: "db" + string(rune('a'+i))}, State: st})
		}
		if _, err := VerifySnapshot(signed(t, s, key, priv), []TrustKey{key}, "gw-1", snapshotNow); err != nil {
			t.Fatalf("VerifySnapshot: %v", err)
		}
	})

	mutate := func(f func(*SignedSnapshot)) SignedSnapshot {
		s := good
		s.Payload = bytes.Clone(good.Payload)
		s.Signature = bytes.Clone(good.Signature)
		f(&s)
		return s
	}
	reencode := func(f func(*Snapshot)) func(*SignedSnapshot) {
		return func(env *SignedSnapshot) {
			var s Snapshot
			if err := json.Unmarshal(env.Payload, &s); err != nil {
				t.Fatal(err)
			}
			f(&s)
			p, err := SnapshotPayload(s)
			if err != nil {
				t.Fatal(err)
			}
			env.Payload = p
		}
	}
	badState := sampleSnapshot(1)
	badState.Routes[0].State = "melting"
	shortKey := TrustKey{PublicKey: ed25519.PublicKey("short"), NotBefore: key.NotBefore, NotAfter: key.NotAfter}
	shortKeySnap := good
	shortKeySnap.KeyID = shortKey.ID()
	// The payload names other as its key while the envelope names key.
	otherNamed := sampleSnapshot(1)
	otherNamed.KeyID = other.ID()
	otherPayload, err := SnapshotPayload(otherNamed)
	if err != nil {
		t.Fatal(err)
	}
	// Signed by other, but the envelope claims key.
	wrongSigner := signed(t, sampleSnapshot(1), other, otherPriv)
	wrongSigner.KeyID = key.ID()

	tests := []struct {
		name string
		snap SignedSnapshot
		keys []TrustKey
		gw   string
		now  time.Time
	}{
		{"wrong gateway", good, []TrustKey{key}, "gw-2", snapshotNow},
		{"before not_before", good, []TrustKey{key}, "gw-1", key.NotBefore.Add(-time.Nanosecond)},
		{"at not_after", good, []TrustKey{key}, "gw-1", key.NotAfter},
		{"after not_after", good, []TrustKey{key}, "gw-1", key.NotAfter.Add(time.Second)},
		{"unknown key id", mutate(func(s *SignedSnapshot) { s.KeyID = "nope" }), []TrustKey{key}, "gw-1", snapshotNow},
		{"no trusted keys", good, nil, "gw-1", snapshotNow},
		{"only another key", good, []TrustKey{other}, "gw-1", snapshotNow},
		{"signed by another key", wrongSigner, []TrustKey{key, other}, "gw-1", snapshotNow},
		{"mutated route", mutate(reencode(func(s *Snapshot) { s.Routes[0].Backend.Host = "10.9.9.9" })), []TrustKey{key}, "gw-1", snapshotNow},
		{"mutated version", mutate(reencode(func(s *Snapshot) { s.Version = 99 })), []TrustKey{key}, "gw-1", snapshotNow},
		{"payload names another key", signRaw(otherPayload, key, priv), []TrustKey{key, other}, "gw-1", snapshotNow},
		{"signed payload that isn't JSON", signRaw([]byte("not json"), key, priv), []TrustKey{key}, "gw-1", snapshotNow},
		{"flipped payload byte", mutate(func(s *SignedSnapshot) { s.Payload[len(s.Payload)-2] ^= 1 }), []TrustKey{key}, "gw-1", snapshotNow},
		{"flipped signature", mutate(func(s *SignedSnapshot) { s.Signature[0] ^= 1 }), []TrustKey{key}, "gw-1", snapshotNow},
		{"empty signature", mutate(func(s *SignedSnapshot) { s.Signature = nil }), []TrustKey{key}, "gw-1", snapshotNow},
		{"invalid state", signed(t, badState, key, priv), []TrustKey{key}, "gw-1", snapshotNow},
		{"malformed public key", shortKeySnap, []TrustKey{shortKey}, "gw-1", snapshotNow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := VerifySnapshot(tt.snap, tt.keys, tt.gw, tt.now); !errors.Is(err, ErrSnapshot) {
				t.Fatalf("VerifySnapshot = %v, want ErrSnapshot", err)
			}
		})
	}
}

// A newer control plane may add fields this gateway doesn't know. The
// signature covers the bytes as sent, so they still verify, and the unknown
// fields are dropped when the payload is decoded.
func TestVerifySnapshotIgnoresUnknownFields(t *testing.T) {
	key, priv := newTrustKey(t)
	base, err := SnapshotPayload(func() Snapshot { s := sampleSnapshot(3); s.KeyID = key.ID(); return s }())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(base, &m); err != nil {
		t.Fatal(err)
	}
	m["placement"] = map[string]any{"region": "us-east-1", "weight": 3}
	routes, ok := m["routes"].([]any)
	if !ok {
		t.Fatalf("routes = %T", m["routes"])
	}
	route, ok := routes[0].(map[string]any)
	if !ok {
		t.Fatalf("route = %T", routes[0])
	}
	route["read_replicas"] = []string{"10.0.0.9"}
	payload, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	got, err := VerifySnapshot(signRaw(payload, key, priv), []TrustKey{key}, "gw-1", snapshotNow)
	if err != nil {
		t.Fatalf("VerifySnapshot with unknown fields: %v", err)
	}
	if got.Version != 3 || len(got.Routes) != 2 || got.Routes[0].ID != "route-a" || got.Routes[1].Label.Branch != "dev" {
		t.Fatalf("VerifySnapshot decoded %+v", got)
	}
}

func TestSnapshotPayloadIsCanonical(t *testing.T) {
	key, priv := newTrustKey(t)
	a := sampleSnapshot(1)

	// Same content with the routes in the other order: same payload, so the
	// same signature.
	b := a
	b.Routes = []Route{a.Routes[1], a.Routes[0]}
	pa, err := SnapshotPayload(a)
	if err != nil {
		t.Fatalf("SnapshotPayload: %v", err)
	}
	pb, err := SnapshotPayload(b)
	if err != nil {
		t.Fatalf("SnapshotPayload: %v", err)
	}
	if !bytes.Equal(pa, pb) {
		t.Fatalf("payload depends on route order:\n%s\n%s", pa, pb)
	}
	sa, sb := signed(t, a, key, priv), signed(t, b, key, priv)
	if !bytes.Equal(sa.Payload, sb.Payload) || !bytes.Equal(sa.Signature, sb.Signature) {
		t.Fatal("reordered routes signed differently")
	}

	// The signed payload carries the key ID, so the signature covers it.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(sa.Payload, &m); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if string(m["key_id"]) != `"`+key.ID()+`"` {
		t.Fatalf("payload key_id = %s", m["key_id"])
	}
	if _, ok := m["signature"]; ok {
		t.Fatal("payload carries a signature field")
	}

	// Neither SnapshotPayload nor SignSnapshot touches its input.
	in := sampleSnapshot(1)
	if _, err := SnapshotPayload(in); err != nil {
		t.Fatalf("SnapshotPayload: %v", err)
	}
	if in.Routes[0].ID != "route-b" {
		t.Fatal("SnapshotPayload sorted the caller's routes")
	}
	if _, err := SignSnapshot(in, priv, key.ID()); err != nil {
		t.Fatalf("SignSnapshot: %v", err)
	}
	if in.Routes[0].ID != "route-b" || in.KeyID != "" {
		t.Fatal("SignSnapshot changed its input")
	}
}

func TestSnapshotWireFormat(t *testing.T) {
	key, priv := newTrustKey(t)
	env := signed(t, sampleSnapshot(7), key, priv)
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	fields := func(raw []byte) map[string]json.RawMessage {
		t.Helper()
		var m map[string]json.RawMessage
		if uerr := json.Unmarshal(raw, &m); uerr != nil {
			t.Fatalf("unmarshal %s: %v", raw, uerr)
		}
		return m
	}
	wantKeys := func(what string, m map[string]json.RawMessage, keys ...string) {
		t.Helper()
		for _, k := range keys {
			if _, ok := m[k]; !ok {
				t.Errorf("%s JSON lacks %q", what, k)
			}
		}
	}

	// The envelope: payload and signature are base64 strings.
	outer := fields(raw)
	wantKeys("envelope", outer, "payload", "key_id", "signature")
	var payloadB64 string
	if err = json.Unmarshal(outer["payload"], &payloadB64); err != nil {
		t.Fatalf("payload is not a string: %v", err)
	}
	payload, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil || !bytes.Equal(payload, env.Payload) {
		t.Fatalf("payload is not the standard base64 of the signed bytes: %v", err)
	}

	top := fields(payload)
	wantKeys("snapshot", top, "gateway_id", "version", "issued_at", "routes", "key_id")
	var routes []json.RawMessage
	if err = json.Unmarshal(top["routes"], &routes); err != nil {
		t.Fatal(err)
	}
	route := fields(routes[1])
	wantKeys("route", route, "id", "label", "state", "backend", "verifiers", "params", "idle_timeout")
	if string(route["label"]) != `"orders--dev"` {
		t.Errorf("label = %s, want \"orders--dev\"", route["label"])
	}
	if string(route["idle_timeout"]) != "300000000000" {
		t.Errorf("idle_timeout = %s, want integer nanoseconds", route["idle_timeout"])
	}
	wantKeys("backend", fields(route["backend"]), "kind", "host", "port", "database", "sni", "ssl_mode", "credential")
	var verifiers []json.RawMessage
	if err = json.Unmarshal(route["verifiers"], &verifiers); err != nil {
		t.Fatal(err)
	}
	wantKeys("verifier", fields(verifiers[0]), "role", "salt", "iterations", "stored_key", "server_key")

	tk, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys("trust key", fields(tk), "public_key", "not_before", "not_after")

	// A signed snapshot still verifies after a trip through JSON.
	var back SignedSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, err := VerifySnapshot(back, []TrustKey{key}, "gw-1", snapshotNow); err != nil {
		t.Fatalf("snapshot does not verify after a JSON round trip: %v", err)
	}

	var bad Route
	if err := json.Unmarshal([]byte(`{"label":"Bad--Label"}`), &bad); err == nil {
		t.Fatal("Unmarshal accepted an invalid label")
	}
}

func TestTrustKeyID(t *testing.T) {
	a, _ := newTrustKey(t)
	b, _ := newTrustKey(t)
	if len(a.ID()) != 64 {
		t.Fatalf("ID = %q, want 64 hex characters", a.ID())
	}
	if a.ID() == b.ID() {
		t.Fatal("two keys share an ID")
	}
}

func TestStoreSwap(t *testing.T) {
	var st Store
	if st.Current() != nil {
		t.Fatal("Current before the first Swap is not nil")
	}
	if err := st.Swap(sampleSnapshot(2)); err != nil {
		t.Fatalf("Swap v2: %v", err)
	}
	if err := st.Swap(sampleSnapshot(1)); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("Swap v1 = %v, want ErrStaleSnapshot", err)
	}
	if err := st.Swap(sampleSnapshot(2)); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("Swap v2 again = %v, want ErrStaleSnapshot", err)
	}
	if got := st.Current(); got == nil || got.Version != 2 {
		t.Fatalf("Current = %+v, want version 2", got)
	}
	if err := st.Swap(sampleSnapshot(3)); err != nil {
		t.Fatalf("Swap v3: %v", err)
	}
	if got := st.Current(); got.Version != 3 {
		t.Fatalf("Current.Version = %d, want 3", got.Version)
	}
}

func TestStoreSwapDoesNotVerify(t *testing.T) {
	var st Store
	if err := st.Swap(sampleSnapshot(1)); err != nil { // unsigned
		t.Fatalf("Swap: %v", err)
	}
}

func TestStoreConcurrent(t *testing.T) {
	var st Store
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var last uint64
			for i := 0; i < 500; i++ {
				if s := st.Current(); s != nil {
					if s.Version < last {
						t.Errorf("version went backwards: %d after %d", s.Version, last)
						return
					}
					last = s.Version
				}
			}
		}()
	}
	for v := uint64(1); v <= 200; v++ {
		if err := st.Swap(sampleSnapshot(v)); err != nil {
			t.Errorf("Swap v%d: %v", v, err)
		}
	}
	wg.Wait()
	if got := st.Current(); got.Version != 200 {
		t.Fatalf("Current.Version = %d, want 200", got.Version)
	}
}
