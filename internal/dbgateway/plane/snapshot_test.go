package plane

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
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

func signed(t *testing.T, s Snapshot, k TrustKey, priv ed25519.PrivateKey) Snapshot {
	t.Helper()
	out, err := SignSnapshot(s, priv, k.ID())
	if err != nil {
		t.Fatalf("SignSnapshot: %v", err)
	}
	return out
}

func TestVerifySnapshot(t *testing.T) {
	key, priv := newTrustKey(t)
	other, _ := newTrustKey(t)
	good := signed(t, sampleSnapshot(1), key, priv)

	t.Run("valid", func(t *testing.T) {
		if err := VerifySnapshot(good, []TrustKey{other, key}, "gw-1", snapshotNow); err != nil {
			t.Fatalf("VerifySnapshot: %v", err)
		}
	})

	t.Run("at not_before", func(t *testing.T) {
		if err := VerifySnapshot(good, []TrustKey{key}, "gw-1", key.NotBefore); err != nil {
			t.Fatalf("VerifySnapshot at not_before: %v", err)
		}
	})

	t.Run("all four states", func(t *testing.T) {
		s := sampleSnapshot(1)
		s.Routes = nil
		for i, st := range []RouteState{RouteStateActive, RouteStatePaused, RouteStateDraining, RouteStateHolding} {
			s.Routes = append(s.Routes, Route{ID: string(rune('a' + i)), State: st})
		}
		if err := VerifySnapshot(signed(t, s, key, priv), []TrustKey{key}, "gw-1", snapshotNow); err != nil {
			t.Fatalf("VerifySnapshot: %v", err)
		}
	})

	mutate := func(f func(*Snapshot)) Snapshot {
		s := good
		s.Routes = append([]Route(nil), good.Routes...)
		s.Signature = append([]byte(nil), good.Signature...)
		f(&s)
		return s
	}
	badState := sampleSnapshot(1)
	badState.Routes[0].State = "melting"
	shortKey := TrustKey{PublicKey: ed25519.PublicKey("short"), NotBefore: key.NotBefore, NotAfter: key.NotAfter}
	shortKeySnap := good
	shortKeySnap.KeyID = shortKey.ID()

	tests := []struct {
		name string
		snap Snapshot
		keys []TrustKey
		gw   string
		now  time.Time
	}{
		{"wrong gateway", good, []TrustKey{key}, "gw-2", snapshotNow},
		{"before not_before", good, []TrustKey{key}, "gw-1", key.NotBefore.Add(-time.Nanosecond)},
		{"at not_after", good, []TrustKey{key}, "gw-1", key.NotAfter},
		{"after not_after", good, []TrustKey{key}, "gw-1", key.NotAfter.Add(time.Second)},
		{"unknown key id", mutate(func(s *Snapshot) { s.KeyID = "nope" }), []TrustKey{key}, "gw-1", snapshotNow},
		{"no trusted keys", good, nil, "gw-1", snapshotNow},
		{"only another key", good, []TrustKey{other}, "gw-1", snapshotNow},
		{"mutated route", mutate(func(s *Snapshot) { s.Routes[0].Backend.Host = "10.9.9.9" }), []TrustKey{key}, "gw-1", snapshotNow},
		{"mutated version", mutate(func(s *Snapshot) { s.Version = 99 }), []TrustKey{key}, "gw-1", snapshotNow},
		{"flipped signature", mutate(func(s *Snapshot) { s.Signature[0] ^= 1 }), []TrustKey{key}, "gw-1", snapshotNow},
		{"empty signature", mutate(func(s *Snapshot) { s.Signature = nil }), []TrustKey{key}, "gw-1", snapshotNow},
		{"invalid state", signed(t, badState, key, priv), []TrustKey{key}, "gw-1", snapshotNow},
		{"malformed public key", shortKeySnap, []TrustKey{shortKey}, "gw-1", snapshotNow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := VerifySnapshot(tt.snap, tt.keys, tt.gw, tt.now); !errors.Is(err, ErrSnapshot) {
				t.Fatalf("VerifySnapshot = %v, want ErrSnapshot", err)
			}
		})
	}
}

func TestSnapshotPayloadIsCanonical(t *testing.T) {
	key, priv := newTrustKey(t)
	a := signed(t, sampleSnapshot(1), key, priv)

	// Same content with the routes in the other order: same payload, and the
	// signature of one verifies the other.
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
	if err := VerifySnapshot(b, []TrustKey{key}, "gw-1", snapshotNow); err != nil {
		t.Fatalf("VerifySnapshot reordered: %v", err)
	}

	// The payload ignores the signature but keeps the key ID.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(pa, &m); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if string(m["signature"]) != "null" {
		t.Fatalf("payload signature = %s, want null", m["signature"])
	}
	if string(m["key_id"]) != `"`+key.ID()+`"` {
		t.Fatalf("payload key_id = %s", m["key_id"])
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
	if in.Routes[0].ID != "route-b" || in.KeyID != "" || in.Signature != nil {
		t.Fatal("SignSnapshot changed its input")
	}
}

func TestSnapshotWireFormat(t *testing.T) {
	key, priv := newTrustKey(t)
	s := signed(t, sampleSnapshot(7), key, priv)
	raw, err := json.Marshal(s)
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

	top := fields(raw)
	wantKeys("snapshot", top, "gateway_id", "version", "issued_at", "routes", "key_id", "signature")
	var routes []json.RawMessage
	if err = json.Unmarshal(top["routes"], &routes); err != nil {
		t.Fatal(err)
	}
	route := fields(routes[0])
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

	// A snapshot still verifies after a trip through JSON.
	var back Snapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := VerifySnapshot(back, []TrustKey{key}, "gw-1", snapshotNow); err != nil {
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
