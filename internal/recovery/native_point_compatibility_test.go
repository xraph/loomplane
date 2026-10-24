package recovery

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNativeSnapshotPointJSONKeepsLegacyScopeBytes(t *testing.T) {
	at := time.Date(2026, 10, 22, 0, 0, 0, 1, time.UTC)
	point := NativePoint{ID: "point", SourceID: "source", ProviderID: "arn:point", Name: "snapshot", Kind: "manual", Status: "available", Engine: "postgres", Version: "17.2", CreatedAt: at, Encrypted: true, Present: true, LastSeenAt: at}
	encoded, err := json.Marshal(point)
	if err != nil {
		t.Fatal(err)
	}
	const expected = `{"id":"point","source_id":"source","provider_id":"arn:point","name":"snapshot","kind":"manual","status":"available","engine":"postgres","version":"17.2","created_at":"2026-10-22T00:00:00.000000001Z","expires_at":null,"encrypted":true,"present":true,"last_seen_at":"2026-10-22T00:00:00.000000001Z"}`
	if string(encoded) != expected {
		t.Fatal("native snapshot JSON changed existing signed scopes", string(encoded))
	}
}
