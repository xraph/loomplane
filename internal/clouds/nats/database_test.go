package nats

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	client "github.com/nats-io/nats.go"
	"github.com/xraph/trove"
	"github.com/xraph/trove/drivers/localdriver"

	"github.com/xraph/loomplane/internal/backupfiles"
)

func testFiles(t *testing.T) *backupfiles.Store {
	t.Helper()
	dsn := "file://" + t.TempDir()
	drv := localdriver.New()
	if err := drv.Open(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	files, err := trove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close(context.Background()) })
	if err = files.CreateBucket(t.Context(), "backups"); err != nil {
		t.Fatal(err)
	}
	store, err := backupfiles.New(files, "backups", dsn)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func testArchive(t *testing.T, m manifest, entry string) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = tarEntry(tw, "manifest.json", int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if _, err = tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if entry != "" {
		if err = tarEntry(tw, entry, 3); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write([]byte("abc")); err != nil {
			t.Fatal(err)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestRestoreRejectsSourceBeforeReadingOrConnecting(t *testing.T) {
	db := Database{Connect: func(context.Context, bool) (*client.Conn, error) { t.Fatal("connected to source"); return nil, nil }}
	if err := db.Restore(t.Context(), nil, backupfiles.Object{}); err == nil {
		t.Fatal("source restore accepted")
	}
}
func TestRestoreValidatesIntegrityAndEntireArchiveBeforeConnecting(t *testing.T) {
	m := manifest{Format: "nats-jetstream-v1", Streams: []snapshot{{Config: client.StreamConfig{Name: "ORDERS", Storage: client.FileStorage, Replicas: 1}, Size: 3}}}
	for _, scenario := range []string{"corrupt", "traversal", "missing", "duplicate", "memory", "oversize"} {
		t.Run(scenario, func(t *testing.T) {
			files := testFiles(t)
			candidate := m
			candidate.Streams = append([]snapshot(nil), m.Streams...)
			entry := "0.snapshot"
			switch scenario {
			case "traversal":
				entry = "../0.snapshot"
			case "missing":
				entry = ""
			case "duplicate":
				candidate.Streams = append(candidate.Streams, candidate.Streams[0])
			case "memory":
				candidate.Streams[0].Config.Storage = client.MemoryStorage
			}
			object, err := files.Put(t.Context(), "org/point", bytes.NewReader(testArchive(t, candidate, entry)))
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "corrupt" {
				object.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}
			if scenario == "oversize" {
				object.Size = maxArchive + 1
			}
			db := Database{Recovery: true, Connect: func(context.Context, bool) (*client.Conn, error) {
				t.Fatal("invalid archive reached destination")
				return nil, nil
			}}
			if err = db.Restore(t.Context(), files, object); err == nil {
				t.Fatal("invalid backup accepted")
			}
		})
	}
}
func TestValidArchiveReachesDestinationOnlyAfterVerification(t *testing.T) {
	files := testFiles(t)
	object, err := files.Put(t.Context(), "org/empty", bytes.NewReader(testArchive(t, manifest{Format: "nats-jetstream-v1"}, "")))
	if err != nil {
		t.Fatal(err)
	}
	refused := errors.New("destination unavailable")
	db := Database{Recovery: true, Connect: func(context.Context, bool) (*client.Conn, error) { return nil, refused }}
	if err = db.Restore(t.Context(), files, object); !errors.Is(err, refused) {
		t.Fatalf("valid archive did not reach destination: %v", err)
	}
}
func TestSnapshotProfilesRejectExternalAndDistributedState(t *testing.T) {
	for _, change := range []func(*snapshot){func(s *snapshot) { s.Config.Replicas = 3 }, func(s *snapshot) { s.Config.Mirror = &client.StreamSource{Name: "REMOTE"} }, func(s *snapshot) { s.Config.Sources = []*client.StreamSource{{Name: "REMOTE"}} }, func(s *snapshot) { s.Config.Name = "unsafe.name" }, func(s *snapshot) { s.State.Consumers = maxConsumers + 1 }} {
		s := snapshot{Config: client.StreamConfig{Name: "ORDERS", Storage: client.FileStorage, Replicas: 1}, Size: 1}
		change(&s)
		if validSnapshot(s) == nil {
			t.Fatal("unsupported profile accepted")
		}
	}
}
